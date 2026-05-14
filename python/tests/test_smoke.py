"""End-to-end ADBC tests against a spawned duckdb+quack server."""

from __future__ import annotations

import pyarrow as pa
import pytest

pytestmark = pytest.mark.integration


def _connect(server):
    import adbc_driver_quack.dbapi

    return adbc_driver_quack.dbapi.connect(server.uri, db_kwargs=server.db_kwargs, autocommit=True)


def test_connect_and_select(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("SELECT 42 AS answer, 'hello' AS greeting")
        table = cur.fetch_arrow_table()
        assert table.num_rows == 1
        assert table.column("answer").to_pylist() == [42]
        assert table.column("greeting").to_pylist() == ["hello"]


def test_create_insert_select(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_t1")
        cur.execute("CREATE TABLE adbc_it_t1 (id INTEGER, name VARCHAR)")
        cur.execute("INSERT INTO adbc_it_t1 VALUES (1,'a'),(2,'b'),(3,'c')")
        cur.execute("SELECT id, name FROM adbc_it_t1 ORDER BY id")
        rows = cur.fetch_arrow_table().to_pylist()
        assert rows == [
            {"id": 1, "name": "a"},
            {"id": 2, "name": "b"},
            {"id": 3, "name": "c"},
        ]
        cur.execute("DROP TABLE adbc_it_t1")


def test_bulk_ingest(quack_server):
    """ADBC bulk-ingest via Statement.BindStream → APPEND_REQUEST."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_ingest")
        cur.execute("CREATE TABLE adbc_it_ingest (id INTEGER, name VARCHAR)")

        table = pa.table({
            "id":   pa.array([10, 20, 30], type=pa.int32()),
            "name": pa.array(["alpha", "beta", "gamma"]),
        })
        cur.adbc_ingest("adbc_it_ingest", table, mode="append")

        cur.execute("SELECT COUNT(*) AS n, SUM(id) AS s FROM adbc_it_ingest")
        row = cur.fetch_arrow_table().to_pylist()[0]
        assert row["n"] == 3
        assert row["s"] == 60
        cur.execute("DROP TABLE adbc_it_ingest")


def test_get_table_types(quack_server):
    with _connect(quack_server) as conn:
        with conn.adbc_get_table_types() as types_iter:
            types = set()
            for batch in types_iter:
                types.update(batch.to_pylist())
        for expected in ("TABLE", "VIEW"):
            assert expected in types, f"expected {expected!r}, got {types}"


def test_get_table_schema(quack_server):
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_schema_probe")
        cur.execute("CREATE TABLE adbc_it_schema_probe (i INTEGER, v VARCHAR)")
        schema = conn.adbc_get_table_schema("adbc_it_schema_probe")
        field_names = [f.name for f in schema]
        assert field_names == ["i", "v"]
        cur.execute("DROP TABLE adbc_it_schema_probe")


def test_typed_arrow_columns(quack_server):
    """Verify primitive columns come back with the expected arrow types."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT 1::INTEGER AS i32, 2::BIGINT AS i64, 1.5::FLOAT AS f32, "
            "2.5::DOUBLE AS f64, TRUE AS b, 'x'::VARCHAR AS s"
        )
        schema = cur.fetch_arrow_table().schema
        assert schema.field("i32").type == pa.int32()
        assert schema.field("i64").type == pa.int64()
        assert schema.field("f32").type == pa.float32()
        assert schema.field("f64").type == pa.float64()
        assert schema.field("b").type == pa.bool_()
        assert schema.field("s").type == pa.string()


def test_get_info(quack_server):
    """GetInfo should return vendor + driver name/version."""
    with _connect(quack_server) as conn:
        with conn.adbc_get_info() as info_iter:
            collected: dict[int, object] = {}
            for batch in info_iter:
                for row in batch.to_pylist():
                    code = row["info_name"]
                    value = row["info_value"]
                    # info_value is a union; pyarrow surfaces it as a dict
                    if isinstance(value, dict):
                        for v in value.values():
                            if v is not None:
                                collected[code] = v
                                break
                    else:
                        collected[code] = value
    # info_name 0 = VendorName, 100 = DriverName
    assert collected.get(0), f"VendorName missing — got {collected}"
    assert "DuckDB" in str(collected[0])
    assert collected.get(100), f"DriverName missing — got {collected}"
    assert "Quack" in str(collected[100])


def test_get_objects_catalogs_depth(quack_server):
    """ObjectDepthCatalogs returns just catalog names with empty schemas lists."""
    with _connect(quack_server) as conn:
        with conn.adbc_get_objects(depth="catalogs") as it:
            rows = []
            for batch in it:
                rows.extend(batch.to_pylist())
    assert len(rows) >= 1
    assert any(r["catalog_name"] == "memory" for r in rows), f"no memory catalog: {rows}"
    # depth=catalogs should produce empty schemas lists
    for r in rows:
        assert r["catalog_db_schemas"] == []


def test_get_objects_all_depth_lists_tables(quack_server):
    """ObjectDepthAll should produce table + column info for our test table."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_objects_probe")
        cur.execute("CREATE TABLE adbc_it_objects_probe (id INTEGER, name VARCHAR)")
        try:
            with conn.adbc_get_objects(
                depth="all", table_name_filter="adbc_it_objects_probe"
            ) as it:
                rows = []
                for batch in it:
                    rows.extend(batch.to_pylist())
            tables = [
                t
                for r in rows
                for s in r.get("catalog_db_schemas") or []
                for t in s.get("db_schema_tables") or []
            ]
            ours = [t for t in tables if t["table_name"] == "adbc_it_objects_probe"]
            assert ours, f"adbc_it_objects_probe missing in {tables!r}"
            col_names = {c["column_name"] for c in ours[0].get("table_columns") or []}
            assert "id" in col_names and "name" in col_names, f"columns: {col_names}"
        finally:
            cur.execute("DROP TABLE adbc_it_objects_probe")


def test_bad_token_rejected(quack_server):
    import adbc_driver_quack.dbapi

    bad = adbc_driver_quack.dbapi.connect(
        quack_server.uri, db_kwargs={"adbc.quack.token": "wrong-token"}
    )
    with pytest.raises(Exception):
        with bad as conn, conn.cursor() as cur:
            cur.execute("SELECT 1")
