"""DuckDB 2.0 (Quack protocol 3) behaviour: VARIANT, GEOMETRY and UUID
results, TLS with certificate pinning, cancellation and rows affected."""

from __future__ import annotations

import threading
import time

import adbc_driver_manager
import pyarrow as pa
import pytest

import adbc_driver_quack.dbapi as quack

pytestmark = pytest.mark.integration


def _connect(server, **db_kwargs):
    return quack.connect(uri=server.uri, db_kwargs={**server.db_kwargs, **db_kwargs}, autocommit=True)


def _ext_name(field: pa.Field) -> bytes | None:
    if isinstance(field.type, pa.BaseExtensionType):
        return field.type.extension_name.encode()
    return (field.metadata or {}).get(b"ARROW:extension:name")


# --- VARIANT / GEOMETRY / UUID -------------------------------------------------


@pytest.mark.parametrize(
    "expression",
    ["42::VARIANT", "{'v': 42::VARIANT}", "[42::VARIANT, NULL::VARIANT]"],
)
def test_variant_results_match_duckdbs_arrow_export(quack_server, expression):
    # The same bytes DuckDB's own Arrow export (and so Hardcharger) returns.
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute(f"SELECT {expression} AS v FROM range(7)")
        table = cur.fetch_arrow_table()
    assert table.num_rows == 7
    value = table.column("v").to_pylist()[0]
    if isinstance(value, list):
        assert value[1] is None
        value = value[0]
    elif "v" in value:
        value = value["v"]
    assert value == {"metadata": b"\x01\x00\x00", "value": b"\x14\x2a\x00\x00\x00"}


def test_variant_is_the_canonical_extension(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("SELECT typed_value FROM duckdb_settings() WHERE name = 'threads'")
        table = cur.fetch_arrow_table()
    field = table.schema.field("typed_value")
    assert _ext_name(field) == b"arrow.parquet.variant"
    storage = field.type.storage_type if isinstance(field.type, pa.BaseExtensionType) else field.type
    assert [child.name for child in storage] == ["metadata", "value"]


def test_variant_value_kinds_survive_a_round_trip(quack_server):
    # Read every kind of member as Parquet Variant, ingest it back, and let
    # DuckDB render both sides: the values must be identical.
    expr = (
        "{'i': 1, 'big': 12345678901234::UBIGINT, 'h': 12345678901234567890123456789012345678::HUGEINT, "
        "'f': 1.5::FLOAT, 'dbl': 2.25::DOUBLE, 'dec': 123.45::DECIMAL(10,2), 's': 'hello', "
        "'long': repeat('x', 100), 'b': '\\x00\\xff'::BLOB, 'u': '6f3c1d4e-8b2a-4c1e-9f0d-123456789abc'::UUID, "
        "'d': DATE '2024-01-02', 't': TIME '03:04:05.123456', 'ts': TIMESTAMP '2024-01-02 03:04:05.123456', "
        "'tz': TIMESTAMPTZ '2024-01-02 03:04:05+00', 'ns': TIMESTAMP_NS '2024-01-02 03:04:05.123456789', "
        "'yes': true, 'no': false, 'nil': NULL, 'arr': [1, 2, 3], 'obj': {'z': 1, 'y': [true, NULL]}}::VARIANT"
    )
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute(f"SELECT {expr} AS v UNION ALL SELECT NULL UNION ALL SELECT 'plain'::VARIANT")
        table = cur.fetch_arrow_table()
        cur.adbc_ingest("variant_rt", table, mode="replace")
        cur.execute(
            f"SELECT (SELECT list(v::VARCHAR ORDER BY v::VARCHAR) FROM variant_rt), "
            f"(SELECT list(x::VARCHAR ORDER BY x::VARCHAR) FROM (SELECT {expr} UNION ALL SELECT NULL "
            f"UNION ALL SELECT 'plain'::VARIANT) t(x)), (SELECT typeof(v) FROM variant_rt LIMIT 1)"
        )
        got, want, typ = cur.fetchone()
        cur.execute("DROP TABLE variant_rt")
    assert typ == "VARIANT"
    assert got == want


@pytest.mark.parametrize(
    ("expression", "message"),
    [
        ("INTERVAL 1 DAY::VARIANT", "INTERVAL"),
        # 39 digits: past the Parquet Variant's widest number (decimal16)
        ("170141183460469231731687303715884105727::HUGEINT::VARIANT", "does not fit"),
    ],
)
def test_unrepresentable_variant_member_is_an_error_not_a_wrong_value(quack_server, expression, message):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        with pytest.raises(adbc_driver_manager.Error, match=message):
            cur.execute(f"SELECT {expression} AS v")
            cur.fetch_arrow_table()


def test_geometry_is_geoarrow_wkb_and_round_trips(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("SELECT 'POINT (1 2)'::GEOMETRY AS g UNION ALL SELECT NULL UNION ALL SELECT 'LINESTRING (0 0, 1 1)'::GEOMETRY")
        table = cur.fetch_arrow_table()
        field = table.schema.field("g")
        assert _ext_name(field) == b"geoarrow.wkb"
        # WKB point (1 2), little endian
        assert table.column("g").to_pylist()[0] == bytes.fromhex("0101000000000000000000f03f0000000000000040")
        cur.adbc_ingest("geom_rt", table, mode="replace")
        cur.execute("SELECT typeof(g), count(g) FROM geom_rt GROUP BY ALL")
        assert cur.fetchall() == [("GEOMETRY", 2)]
        cur.execute("SELECT g FROM geom_rt")
        assert cur.fetch_arrow_table().column("g").to_pylist() == table.column("g").to_pylist()
        cur.execute("DROP TABLE geom_rt")


def test_uuid_is_arrow_uuid_and_round_trips(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("SELECT '6f3c1d4e-8b2a-4c1e-9f0d-123456789abc'::UUID AS id")
        table = cur.fetch_arrow_table()
        assert _ext_name(table.schema.field("id")) == b"arrow.uuid"
        cur.adbc_ingest("uuid_rt", table, mode="replace")
        cur.execute("SELECT typeof(id), id::VARCHAR FROM uuid_rt")
        assert cur.fetchall() == [("UUID", "6f3c1d4e-8b2a-4c1e-9f0d-123456789abc")]
        cur.execute("DROP TABLE uuid_rt")


def test_timestamp_units_and_zones(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT TIMESTAMP '2024-01-02 03:04:05' AS ts, TIMESTAMP_S '2024-01-02 03:04:05' AS ts_s, "
            "TIMESTAMP_MS '2024-01-02 03:04:05.123' AS ts_ms, TIMESTAMP_NS '2024-01-02 03:04:05.123456789' AS ts_ns, "
            "TIMESTAMPTZ '2024-01-02 03:04:05+00' AS tz, '2024-01-02 03:04:05.123456789+00'::TIMESTAMPTZ_NS AS tz_ns"
        )
        schema = cur.fetch_arrow_table().schema
    # A naive TIMESTAMP must not come back tagged UTC.
    assert schema.field("ts").type == pa.timestamp("us")
    assert schema.field("ts_s").type == pa.timestamp("s")
    assert schema.field("ts_ms").type == pa.timestamp("ms")
    assert schema.field("ts_ns").type == pa.timestamp("ns")
    assert schema.field("tz").type == pa.timestamp("us", tz="UTC")
    assert schema.field("tz_ns").type == pa.timestamp("ns", tz="UTC")


def test_nested_dates_and_times_are_not_dropped(quack_server):
    # Values nested in LIST/STRUCT used to come back NULL for these types.
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT [DATE '2024-01-02'] AS d, {'t': TIMESTAMP '2024-01-02 03:04:05', 'i': INTERVAL 2 DAY} AS s, "
            "['6f3c1d4e-8b2a-4c1e-9f0d-123456789abc'::UUID] AS u"
        )
        row = cur.fetch_arrow_table().to_pylist()[0]
    assert row["d"][0] is not None
    assert row["s"]["t"] is not None and row["s"]["i"] is not None
    assert row["u"][0] is not None


# --- rows affected -------------------------------------------------------------


def test_rows_affected_for_dml_and_ingest(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("CREATE OR REPLACE TABLE ra_t (a INTEGER)")
        assert cur.adbc_ingest("ra_t", pa.table({"a": pa.array([1, 2, 3], pa.int32())}), mode="append") == 3
        cur.execute("INSERT INTO ra_t SELECT * FROM range(4)")
        assert cur.rowcount == 4
        cur.execute("UPDATE ra_t SET a = a + 1 WHERE a < 2")
        assert cur.rowcount == 3
        cur.execute("DELETE FROM ra_t")
        assert cur.rowcount == 7
        # A query whose one column happens to be called "Count" is not DML.
        cur.execute('SELECT 5 AS "Count"')
        assert cur.rowcount == -1
        cur.execute("DROP TABLE ra_t")


# --- cancellation --------------------------------------------------------------


def test_cancel_interrupts_the_server_statement(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        timer = threading.Timer(1.0, cur.adbc_cancel)
        timer.start()
        started = time.monotonic()
        with pytest.raises(adbc_driver_manager.Error):
            cur.execute("SELECT count(*) FROM range(100000000000) a")
            cur.fetchall()
        timer.cancel()
        assert time.monotonic() - started < 30
        # The connection is still usable, and the server is not still busy
        # with the abandoned statement.
        cur.execute("SELECT 1")
        assert cur.fetchone() == (1,)


# --- TLS ----------------------------------------------------------------------


def test_tls_with_pinned_fingerprint(quack_tls_server):
    with quack.connect(
        uri=quack_tls_server.uri,
        db_kwargs={**quack_tls_server.db_kwargs, "adbc.quack.ssl_fingerprint": quack_tls_server.fingerprint},
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 42")
        assert cur.fetchone() == (42,)


def test_tls_wrong_fingerprint_is_refused(quack_tls_server):
    wrong = "AA:" * 31 + "AA"
    with pytest.raises(adbc_driver_manager.Error, match="fingerprint"):
        quack.connect(
            uri=quack_tls_server.uri,
            db_kwargs={**quack_tls_server.db_kwargs, "adbc.quack.ssl_fingerprint": wrong},
        )


def test_tls_self_signed_without_a_pin_is_refused(quack_tls_server):
    with pytest.raises(adbc_driver_manager.Error, match="certificate"):
        quack.connect(uri=quack_tls_server.uri, db_kwargs={**quack_tls_server.db_kwargs, "adbc.quack.tls": "true"})


def test_plain_http_to_a_tls_server_fails(quack_tls_server):
    # 127.0.0.1 defaults to plain HTTP, which a TLS listener refuses.
    with pytest.raises(adbc_driver_manager.Error):
        quack.connect(uri=quack_tls_server.uri, db_kwargs=quack_tls_server.db_kwargs)
