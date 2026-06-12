"""End-to-end ADBC tests against a spawned duckdb+quack server.

Several tests below mirror snippets in the project README — see each
test's docstring for the section it covers. Keeping these in lockstep
matters: if the README claims a pattern works, a test in this file
must prove it does.
"""

from __future__ import annotations

import pyarrow as pa
import pytest

pytestmark = pytest.mark.integration


def _connect(server):
    import adbc_driver_quack.dbapi

    return adbc_driver_quack.dbapi.connect(server.uri, db_kwargs=server.db_kwargs, autocommit=True)


def test_readme_quickstart_step3_connect_and_query(quack_server):
    """README "Step 3: Connect and query" — the headline example."""
    import adbc_driver_quack.dbapi as quack

    with quack.connect(
        uri=quack_server.uri,
        db_kwargs=quack_server.db_kwargs,
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 42 AS answer, 'hello duckdb' AS greeting")
        table = cur.fetch_arrow_table()
        assert table.num_rows == 1
        assert table.column("answer").to_pylist() == [42]
        assert table.column("greeting").to_pylist() == ["hello duckdb"]


def test_readme_alternative_manager_pattern(quack_server):
    """README "Alternative: drive adbc_driver_manager directly".

    Verifies that passing the bundled driver path + entrypoint to
    adbc_driver_manager.dbapi.connect works identically to the wrapper.
    """
    from adbc_driver_manager import dbapi
    import adbc_driver_quack

    with dbapi.connect(
        driver=adbc_driver_quack._driver_path(),
        entrypoint="QuackDriverInit",
        db_kwargs={
            "uri": quack_server.uri,
            **quack_server.db_kwargs,
        },
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 42 AS answer")
        rows = cur.fetch_arrow_table().to_pylist()
        assert rows == [{"answer": 42}]


def test_readme_streaming_large_result_set(quack_server):
    """README "Streaming large result sets" — fetch_record_batch loop."""
    import adbc_driver_quack.dbapi as quack

    with quack.connect(
        uri=quack_server.uri,
        db_kwargs=quack_server.db_kwargs,
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT i AS n FROM range(0, 50000) t(i)")
        reader = cur.fetch_record_batch()
        total = 0
        for batch in reader:
            total += batch.num_rows  # one ~2k-row Arrow batch at a time
        assert total == 50_000


def test_readme_bulk_ingest(quack_server):
    """README "Bulk ingest (Arrow → DuckDB)" — the autocommit=True snippet.

    Mirrors the README exactly (create_append, no manual CREATE TABLE)
    AND verifies the rows survive the connection close by re-reading
    from a *fresh* connection. ADBC connections are autocommit-OFF by
    default, so without autocommit=True the ingest would roll back on
    close — reading back on the same connection would hide that.
    """
    import adbc_driver_quack.dbapi as quack

    table = pa.table({"id": [1, 2, 3], "name": ["alice", "bob", "carol"]})
    with quack.connect(
        uri=quack_server.uri,
        db_kwargs=quack_server.db_kwargs,
        autocommit=True,
    ) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS customers")
        cur.adbc_ingest(table_name="customers", data=table, mode="create_append")

    # Fresh connection — proves the data actually persisted (regression
    # guard for the autocommit-off footgun the README warns about).
    try:
        with _connect(quack_server) as conn, conn.cursor() as cur:
            cur.execute("SELECT COUNT(*) AS n FROM customers")
            assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 3
    finally:
        with _connect(quack_server) as conn, conn.cursor() as cur:
            cur.execute("DROP TABLE IF EXISTS customers")


def test_readme_bulk_ingest_explicit_commit(quack_server):
    """README bulk-ingest "explicit transaction" variant: default
    autocommit-off connection + conn.commit() after adbc_ingest.

    Also pins the documented footgun: without the commit, a fresh
    connection must NOT see the table (rolled back on close).
    """
    import adbc_driver_quack.dbapi as quack

    table = pa.table({"id": [1, 2, 3], "name": ["alice", "bob", "carol"]})

    # No commit → rolled back on close → table must not exist afterwards.
    with quack.connect(
        uri=quack_server.uri, db_kwargs=quack_server.db_kwargs,
    ) as conn, conn.cursor() as cur:
        cur.adbc_ingest(table_name="customers", data=table, mode="create_append")
    with _connect(quack_server) as conn, conn.cursor() as cur:
        with pytest.raises(Exception):
            cur.execute("SELECT COUNT(*) AS n FROM customers")
            cur.fetch_arrow_table()

    # With conn.commit() → persists.
    try:
        with quack.connect(
            uri=quack_server.uri, db_kwargs=quack_server.db_kwargs,
        ) as conn, conn.cursor() as cur:
            cur.adbc_ingest(table_name="customers", data=table, mode="create_append")
            conn.commit()
        with _connect(quack_server) as conn, conn.cursor() as cur:
            cur.execute("SELECT COUNT(*) AS n FROM customers")
            assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 3
    finally:
        with _connect(quack_server) as conn, conn.cursor() as cur:
            cur.execute("DROP TABLE IF EXISTS customers")


def test_bulk_ingest_mode_create(quack_server):
    """mode='create' (the default) builds the table from the Arrow schema,
    then errors if asked to create the same table again."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_mode_create")
        try:
            table = pa.table({"id": pa.array([1, 2], type=pa.int32()), "name": ["a", "b"]})
            # No mode arg → ADBC default is "create".
            cur.adbc_ingest("adbc_it_mode_create", table)
            cur.execute("SELECT COUNT(*) AS n FROM adbc_it_mode_create")
            assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 2

            # Re-creating an existing table must fail.
            with pytest.raises(Exception):
                cur.adbc_ingest("adbc_it_mode_create", table, mode="create")
        finally:
            cur.execute("DROP TABLE IF EXISTS adbc_it_mode_create")


def test_bulk_ingest_mode_append_requires_existing_table(quack_server):
    """mode='append' performs no DDL — appending to a missing table errors."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_mode_append")
        table = pa.table({"id": pa.array([1], type=pa.int32())})
        with pytest.raises(Exception):
            cur.adbc_ingest("adbc_it_mode_append", table, mode="append")

        cur.execute("CREATE TABLE adbc_it_mode_append (id INTEGER)")
        try:
            cur.adbc_ingest("adbc_it_mode_append", table, mode="append")
            cur.adbc_ingest("adbc_it_mode_append", table, mode="append")
            cur.execute("SELECT COUNT(*) AS n FROM adbc_it_mode_append")
            assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 2
        finally:
            cur.execute("DROP TABLE IF EXISTS adbc_it_mode_append")


def test_bulk_ingest_mode_replace(quack_server):
    """mode='replace' drops + recreates the table — old rows and the old
    schema are gone, replaced by the incoming Arrow schema."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_mode_replace")
        try:
            first = pa.table({"id": pa.array([1, 2, 3], type=pa.int32())})
            cur.adbc_ingest("adbc_it_mode_replace", first, mode="create")

            # Replace with a different schema + different rows.
            second = pa.table({"label": ["x"]})
            cur.adbc_ingest("adbc_it_mode_replace", second, mode="replace")

            cur.execute("SELECT * FROM adbc_it_mode_replace")
            out = cur.fetch_arrow_table()
            assert out.column_names == ["label"]
            assert out.to_pylist() == [{"label": "x"}]
        finally:
            cur.execute("DROP TABLE IF EXISTS adbc_it_mode_replace")


def test_bulk_ingest_mode_create_append(quack_server):
    """mode='create_append' creates on first call, appends on the next."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_mode_ca")
        try:
            table = pa.table({"id": pa.array([42], type=pa.int64())})
            cur.adbc_ingest("adbc_it_mode_ca", table, mode="create_append")
            cur.adbc_ingest("adbc_it_mode_ca", table, mode="create_append")
            cur.execute("SELECT COUNT(*) AS n FROM adbc_it_mode_ca")
            assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 2
        finally:
            cur.execute("DROP TABLE IF EXISTS adbc_it_mode_ca")


def test_readme_transactions(quack_server):
    """README "Transactions (autocommit off)" — explicit commit example."""
    import adbc_driver_quack.dbapi as quack

    with quack.connect(
        uri=quack_server.uri,
        db_kwargs=quack_server.db_kwargs,
        autocommit=True,
    ) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS orders")
        cur.execute("CREATE TABLE orders (id INTEGER, status VARCHAR)")
        cur.execute("DROP TABLE IF EXISTS order_items")
        cur.execute("CREATE TABLE order_items (order_id INTEGER, name VARCHAR, qty INTEGER)")

    with quack.connect(
        uri=quack_server.uri,
        db_kwargs=quack_server.db_kwargs,
        autocommit=False,
    ) as conn, conn.cursor() as cur:
        cur.execute("INSERT INTO orders VALUES (1, 'pending')")
        cur.execute("INSERT INTO order_items VALUES (1, 'widget', 2)")
        conn.commit()  # both inserts persist atomically

    with quack.connect(
        uri=quack_server.uri,
        db_kwargs=quack_server.db_kwargs,
        autocommit=True,
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT COUNT(*) AS n FROM orders")
        assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 1
        cur.execute("SELECT COUNT(*) AS n FROM order_items")
        assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 1
        cur.execute("DROP TABLE order_items")
        cur.execute("DROP TABLE orders")


def test_bulk_ingest_in_transaction(quack_server):
    """Bulk ingest participates in the connection's transaction: with
    autocommit off, ingested rows roll back and only survive on commit."""
    import adbc_driver_quack.dbapi

    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS adbc_it_ingest_tx")
        cur.execute("CREATE TABLE adbc_it_ingest_tx (id INTEGER)")

    with adbc_driver_quack.dbapi.connect(
        quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=False
    ) as conn, conn.cursor() as cur:
        table = pa.table({"id": pa.array([1, 2, 3], type=pa.int32())})

        # Ingest then roll back — rows must vanish.
        cur.adbc_ingest("adbc_it_ingest_tx", table, mode="append")
        conn.rollback()
        cur.execute("SELECT COUNT(*) AS n FROM adbc_it_ingest_tx")
        assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 0

        # Ingest then commit — rows must persist.
        cur.adbc_ingest("adbc_it_ingest_tx", table, mode="append")
        conn.commit()

    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute("SELECT COUNT(*) AS n FROM adbc_it_ingest_tx")
        assert cur.fetch_arrow_table().to_pylist()[0]["n"] == 3
        cur.execute("DROP TABLE adbc_it_ingest_tx")


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

        # SUM(INTEGER) in DuckDB returns HUGEINT. The driver maps HUGEINT
        # to arrow Decimal128(38, 0), so the result is a decimal.Decimal.
        cur.execute("SELECT COUNT(*) AS n, SUM(id) AS s FROM adbc_it_ingest")
        table = cur.fetch_arrow_table()
        assert pa.types.is_decimal128(table.schema.field("s").type), \
            f"SUM(INTEGER) should map to Decimal128, got {table.schema.field('s').type}"
        row = table.to_pylist()[0]
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


def test_nested_types_round_trip(quack_server):
    """LIST, STRUCT, ARRAY, MAP all surface as native nested arrow types."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT "
            "  [1, 2, 3]::INTEGER[3] AS arr, "  # fixed-size ARRAY
            "  [10, 20]::INTEGER[] AS lst, "    # variable-size LIST
            "  {'a': 1, 'b': 'hello'} AS strct, "
            "  MAP {'x': 1, 'y': 2} AS m"
        )
        table = cur.fetch_arrow_table()
        schema = table.schema

        # ARRAY<INTEGER>[3] → FixedSizeList<int32, 3>
        assert pa.types.is_fixed_size_list(schema.field("arr").type), \
            f"expected fixed-size list, got {schema.field('arr').type}"
        assert schema.field("arr").type.list_size == 3

        # LIST<INTEGER> → list<int32>
        assert pa.types.is_list(schema.field("lst").type), \
            f"expected list, got {schema.field('lst').type}"

        # STRUCT(a INTEGER, b VARCHAR) → struct<a:int32, b:string>
        assert pa.types.is_struct(schema.field("strct").type), \
            f"expected struct, got {schema.field('strct').type}"

        # MAP<VARCHAR, INTEGER> → map<string, int32>
        assert pa.types.is_map(schema.field("m").type), \
            f"expected map, got {schema.field('m').type}"

        row = table.to_pylist()[0]
        assert row["arr"] == [1, 2, 3]
        assert row["lst"] == [10, 20]
        assert row["strct"]["a"] == 1
        assert row["strct"]["b"] == "hello"
        m = dict(row["m"])
        assert m == {"x": 1, "y": 2}


def test_streaming_large_result_set(quack_server):
    """ExecuteQuery streams chunks lazily — verify a >server-batch result reads
    cleanly without OOM-ing or short-circuiting the row count."""
    with _connect(quack_server) as conn, conn.cursor() as cur:
        # range(0, 100_000) is comfortably larger than DuckDB's standard 2048
        # row batch (~48 server chunks) — exercising fetchMore at least once.
        cur.execute("SELECT i AS n FROM range(0, 100000) t(i)")
        reader = cur.fetch_record_batch()
        total = 0
        batch_count = 0
        for batch in reader:
            total += batch.num_rows
            batch_count += 1
        assert total == 100_000, f"expected 100k rows, got {total}"
        # Sanity: the result *did* come back as multiple batches, not one
        # giant materialized blob.
        assert batch_count > 1, (
            f"expected multiple streamed batches, got {batch_count} — "
            "streaming may have regressed"
        )


def test_connection_pool_friendliness(quack_server):
    """Rapid open/close cycles + concurrent connections against one server."""
    import concurrent.futures

    # 50 sequential open/close cycles — each one runs handshake +
    # DISCONNECT. If we leak fds, sockets, or server-side connection ids,
    # the back end will start refusing well before 50 iterations.
    for _ in range(50):
        with _connect(quack_server) as conn, conn.cursor() as cur:
            cur.execute("SELECT 1 AS x")
            row = cur.fetch_arrow_table().to_pylist()[0]
            assert row["x"] == 1

    # 16 concurrent connections each doing a small SELECT. This exposes
    # any race in the session/connection-id allocator and ensures the
    # server-side handler tolerates parallel handshakes.
    def _worker(i):
        with _connect(quack_server) as conn, conn.cursor() as cur:
            cur.execute(f"SELECT {i} AS x")
            return cur.fetch_arrow_table().to_pylist()[0]["x"]

    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as ex:
        results = list(ex.map(_worker, range(16)))
    assert sorted(results) == list(range(16)), f"lost results: {results}"


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


def test_commit_with_nothing_pending_is_noop(quack_server):
    """With autocommit off, commit()/rollback() before any statement must
    be harmless no-ops (the server-side BEGIN is lazy), not the JDBC-era
    "cannot commit - no transaction is active" server error."""
    import adbc_driver_quack.dbapi

    with adbc_driver_quack.dbapi.connect(
        quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=False
    ) as conn:
        conn.commit()
        conn.rollback()
        with conn.cursor() as cur:
            cur.execute("SELECT 1 AS x")
            assert cur.fetch_arrow_table().to_pylist()[0]["x"] == 1
        conn.commit()


def test_token_env(quack_server):
    """adbc.quack.token_env reads the auth token from an env var.

    Runs in a subprocess because the Go runtime inside the driver
    shared library snapshots the process environment when it first
    loads -- an env var set from Python *after* that (monkeypatch.setenv)
    is invisible to the Go side. Real usage sets the variable before
    the process starts, which is exactly what the subprocess models.
    """
    import os
    import subprocess
    import sys
    import textwrap

    script = textwrap.dedent(
        f"""
        import adbc_driver_quack.dbapi

        with adbc_driver_quack.dbapi.connect(
            {quack_server.uri!r}, db_kwargs={{"adbc.quack.token_env": "QUACK_IT_TOKEN"}}
        ) as conn, conn.cursor() as cur:
            cur.execute("SELECT 1 AS x")
            assert cur.fetch_arrow_table().to_pylist()[0]["x"] == 1
        """
    )
    env = dict(os.environ, QUACK_IT_TOKEN=quack_server.token)
    result = subprocess.run(
        [sys.executable, "-c", script], env=env, capture_output=True, text=True
    )
    assert result.returncode == 0, result.stderr


def test_token_file(quack_server, tmp_path):
    """adbc.quack.token_file reads the auth token from a local file."""
    import adbc_driver_quack.dbapi

    token_file = tmp_path / "token"
    token_file.write_text(quack_server.token + "\n")
    with adbc_driver_quack.dbapi.connect(
        quack_server.uri, db_kwargs={"adbc.quack.token_file": str(token_file)}
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 1 AS x")
        assert cur.fetch_arrow_table().to_pylist()[0]["x"] == 1


def test_token_env_rejected_on_url(quack_server):
    """tokenEnv/tokenFile on the URL must be rejected outright — a pasted
    URL must not be able to read a local secret."""
    import adbc_driver_quack.dbapi

    with pytest.raises(Exception, match="ADBC option"):
        adbc_driver_quack.dbapi.connect(quack_server.uri + "?tokenEnv=SOME_VAR")
