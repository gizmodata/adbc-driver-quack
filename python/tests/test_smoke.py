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

        # SUM(INTEGER) in DuckDB returns HUGEINT, which surfaces as a string
        # via the driver (no native arrow Int128). Cast as a sanity check.
        cur.execute("SELECT COUNT(*) AS n, CAST(SUM(id) AS BIGINT) AS s FROM adbc_it_ingest")
        row = cur.fetch_arrow_table().to_pylist()[0]
        assert row["n"] == 3
        assert int(row["s"]) == 60
        cur.execute("DROP TABLE adbc_it_ingest")


def test_get_table_types(quack_server):
    """adbc_get_table_types returns a list[str] directly."""
    with _connect(quack_server) as conn:
        types = set(conn.adbc_get_table_types())
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
    """adbc_get_info() returns a dict mapping info name -> value."""
    with _connect(quack_server) as conn:
        info = conn.adbc_get_info()
    # Keys are either int codes or human-readable strings via _KNOWN_INFO_VALUES.
    vendor_name = info.get("vendor_name") or info.get(0)
    driver_name = info.get("driver_name") or info.get(100)
    assert vendor_name, f"VendorName missing — got {info}"
    assert "DuckDB" in str(vendor_name), f"unexpected vendor: {vendor_name!r}"
    assert driver_name, f"DriverName missing — got {info}"
    assert "Quack" in str(driver_name), f"unexpected driver: {driver_name!r}"


def test_get_objects_catalogs_depth(quack_server):
    """adbc_get_objects(depth='catalogs') returns catalogs with empty schemas lists."""
    with _connect(quack_server) as conn:
        reader = conn.adbc_get_objects(depth="catalogs")
        rows = []
        for batch in reader:
            rows.extend(batch.to_pylist())
    assert len(rows) >= 1
    assert any(r["catalog_name"] == "memory" for r in rows), f"no memory catalog: {rows}"
    for r in rows:
        assert r["catalog_db_schemas"] == []


def test_get_objects_all_depth_lists_tables(quack_server):
    """ObjectDepthAll should produce table + column info for our test table."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_objects_probe")
        cur.execute("CREATE TABLE adbc_it_objects_probe (id INTEGER, name VARCHAR)")
        try:
            reader = conn.adbc_get_objects(
                depth="all", table_name_filter="adbc_it_objects_probe"
            )
            rows = []
            for batch in reader:
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


def test_get_objects_returns_primary_and_foreign_keys(quack_server):
    """Verify table_constraints carries PK + FK info for a parent/child pair."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_orders")
        cur.execute("DROP TABLE IF EXISTS adbc_it_users")
        cur.execute("CREATE TABLE adbc_it_users (id INTEGER PRIMARY KEY, name VARCHAR)")
        cur.execute(
            "CREATE TABLE adbc_it_orders (order_id INTEGER PRIMARY KEY, "
            "user_id INTEGER REFERENCES adbc_it_users(id), amount DOUBLE)"
        )
        try:
            reader = conn.adbc_get_objects(
                depth="all",
                table_name_filter="adbc_it_%",
            )
            rows = []
            for batch in reader:
                rows.extend(batch.to_pylist())
            by_table = {
                t["table_name"]: t
                for r in rows
                for s in r.get("catalog_db_schemas") or []
                for t in s.get("db_schema_tables") or []
                if t["table_name"].startswith("adbc_it_")
            }
            assert "adbc_it_users" in by_table, f"missing users table; got {list(by_table)}"
            assert "adbc_it_orders" in by_table, f"missing orders table; got {list(by_table)}"

            users_cs = by_table["adbc_it_users"].get("table_constraints") or []
            pks = [c for c in users_cs if c["constraint_type"] == "PRIMARY KEY"]
            assert pks, f"no PK on users: {users_cs!r}"
            assert "id" in pks[0]["constraint_column_names"]

            orders_cs = by_table["adbc_it_orders"].get("table_constraints") or []
            fks = [c for c in orders_cs if c["constraint_type"] == "FOREIGN KEY"]
            assert fks, f"no FK on orders: {orders_cs!r}"
            assert "user_id" in fks[0]["constraint_column_names"]
            usages = fks[0].get("constraint_column_usage") or []
            assert any(
                u["fk_table"] == "adbc_it_users" and u["fk_column_name"] == "id"
                for u in usages
            ), f"expected usage referencing users.id, got {usages!r}"
        finally:
            cur.execute("DROP TABLE IF EXISTS adbc_it_orders")
            cur.execute("DROP TABLE IF EXISTS adbc_it_users")


def test_transaction_commit_and_rollback(quack_server):
    """When autocommit is off, Commit persists writes and Rollback discards them."""
    import adbc_driver_quack.dbapi

    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_tx")
        cur.execute("CREATE TABLE adbc_it_tx (id INTEGER)")

    # Disable autocommit, insert one row, commit; then insert + rollback.
    with adbc_driver_quack.dbapi.connect(
        quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=False
    ) as conn, conn.cursor() as cur:
        cur.execute("INSERT INTO adbc_it_tx VALUES (1)")
        conn.commit()

        cur.execute("INSERT INTO adbc_it_tx VALUES (2)")
        conn.rollback()

        cur.execute("INSERT INTO adbc_it_tx VALUES (3)")
        conn.commit()

    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("SELECT id FROM adbc_it_tx ORDER BY id")
        ids = [r["id"] for r in cur.fetch_arrow_table().to_pylist()]
        assert ids == [1, 3], f"expected [1, 3] (2 should have rolled back), got {ids}"
        cur.execute("DROP TABLE adbc_it_tx")


def test_bad_token_rejected(quack_server):
    """A wrong token must raise during connect (server fails CONNECTION_REQUEST)."""
    import adbc_driver_quack.dbapi

    with pytest.raises(Exception):
        # The driver raises during AdbcConnection construction because the
        # CONNECTION_REQUEST handshake gets rejected, so the wrapper is
        # the call we expect to throw.
        adbc_driver_quack.dbapi.connect(
            quack_server.uri, db_kwargs={"adbc.quack.token": "wrong-token"}
        )
