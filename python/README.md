# adbc-driver-quack

Apache Arrow ADBC driver for [DuckDB's Quack remote protocol](https://duckdb.org/docs/current/quack/overview).

See the main repo at <https://github.com/gizmodata/adbc-driver-quack>
for source, design notes, and the companion JDBC driver.

## Install

```
pip install adbc-driver-quack
```

## Quickstart

```python
import adbc_driver_quack.dbapi
import pyarrow

with adbc_driver_quack.dbapi.connect(
    "quack://127.0.0.1:9494",
    db_kwargs={"adbc.quack.token": "my-token"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer")
    table: pyarrow.Table = cur.fetch_arrow_table()
    print(table)
```

Bulk ingest:

```python
import pyarrow as pa
import adbc_driver_quack.dbapi

t = pa.table({"id": [1, 2, 3], "name": ["alice", "bob", "carol"]})
with adbc_driver_quack.dbapi.connect("quack://127.0.0.1:9494") as conn, conn.cursor() as cur:
    cur.adbc_ingest("customers", t, mode="append")
```

License: MIT.
