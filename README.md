# adbc-driver-quack

**An [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for [DuckDB's Quack remote protocol](https://duckdb.org/docs/current/quack/overview).**

[![PyPI](https://img.shields.io/pypi/v/adbc-driver-quack?label=PyPI&logo=pypi&logoColor=white&color=blue)](https://pypi.org/project/adbc-driver-quack/)
[![PyPI downloads](https://img.shields.io/pypi/dm/adbc-driver-quack?label=downloads&logo=pypi&logoColor=white)](https://pypistats.org/packages/adbc-driver-quack)
[![Python versions](https://img.shields.io/pypi/pyversions/adbc-driver-quack?label=Python&logo=python&logoColor=white)](https://pypi.org/project/adbc-driver-quack/)
[![Go module](https://img.shields.io/github/v/tag/gizmodata/adbc-driver-quack?label=Go%20module&logo=go&logoColor=white&sort=semver)](https://pkg.go.dev/github.com/gizmodata/adbc-driver-quack)
[![CI](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/python.yml/badge.svg)](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/python.yml)
[![GitHub Repo](https://img.shields.io/badge/github-gizmodata%2Fadbc--driver--quack-181717?logo=github)](https://github.com/gizmodata/adbc-driver-quack)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Returns Apache Arrow `RecordBatch`es directly from a remote DuckDB server
speaking Quack. Supports the standard ADBC bulk-ingest path
(`Statement.BindStream` → `APPEND_REQUEST`) for fast column-oriented loads.

Distributed as:
- a Go module — `github.com/gizmodata/adbc-driver-quack`
- a `pip install adbc-driver-quack` wheel for Python (macOS / Linux / Windows × x64 / arm64)

> **Status:** Alpha — `v0.1.0-alpha.1` is the first release. The companion
> [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc) JDBC
> driver is the same protocol from the JVM and is at
> [v0.1.0-alpha.1 on Maven Central](https://central.sonatype.com/artifact/com.gizmodata/quack-jdbc/0.1.0-alpha.1).

## Quickstart

### 1. Start a Quack server (any DuckDB v1.5.2+)

```sql
-- in any DuckDB session, with the unsigned extensions flag enabled (`duckdb -unsigned`)
INSTALL quack FROM core_nightly;
LOAD quack;
CALL quack_serve('quack:127.0.0.1:9494', token=>'my-secret-token');
```

The server stays running until the DuckDB session exits. Press Ctrl-C in
the DuckDB REPL to stop it.

### 2. Install the driver

**Python:**

```bash
pip install adbc-driver-quack
```

**Go:**

```bash
go get github.com/gizmodata/adbc-driver-quack@latest
```

### 3. Connect and query

```python
import adbc_driver_quack.dbapi as quack
import pyarrow

with quack.connect(
    uri="quack://127.0.0.1:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer, 'hello duckdb' AS greeting")
    table: pyarrow.Table = cur.fetch_arrow_table()
    print(table)
```

The result is a real `pyarrow.Table` — pass it straight to Polars, Pandas,
DuckDB-in-process, ibis, or anything else that consumes Arrow:

```python
import polars as pl
df = pl.from_arrow(table)
```

#### Alternative: drive `adbc_driver_manager` directly

If you prefer the [adbc-quickstarts](https://github.com/columnar-tech/adbc-quickstarts)
idiom — passing the driver to `adbc_driver_manager.dbapi.connect` rather
than going through our wrapper — point at the bundled shared library via
`_driver_path()`:

```python
from adbc_driver_manager import dbapi
import adbc_driver_quack

with dbapi.connect(
    driver=adbc_driver_quack._driver_path(),
    entrypoint="QuackDriverInit",
    db_kwargs={
        "uri": "quack://127.0.0.1:9494",
        "adbc.quack.token": "my-secret-token",
    },
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer")
    table = cur.fetch_arrow_table()
```

Both styles work the same on the wire — pick whichever reads better for
your codebase.

### Streaming large result sets

`Cursor.fetch_record_batch()` returns a `pyarrow.RecordBatchReader` that
pulls one server-side `DataChunk` per `read_next_batch()` call. Memory
stays bounded by the server's chunk size (~2k rows) even when the result
is millions of rows:

```python
with conn.cursor() as cur:
    cur.execute("SELECT * FROM lineitem")  # arbitrary size
    reader = cur.fetch_record_batch()
    for batch in reader:
        process(batch)  # one ~2k-row Arrow batch at a time
```

### Bulk ingest (Arrow → DuckDB)

```python
import pyarrow as pa
import adbc_driver_quack.dbapi as quack

table = pa.table({"id": [1, 2, 3], "name": ["alice", "bob", "carol"]})
with quack.connect(uri="quack://127.0.0.1:9494", db_kwargs={"adbc.quack.token": "..."}) as conn, conn.cursor() as cur:
    cur.adbc_ingest(table_name="customers", data=table, mode="append")  # one APPEND_REQUEST per RecordBatch
```

### Transactions (autocommit off)

```python
import adbc_driver_quack.dbapi as quack

with quack.connect(
    uri="quack://127.0.0.1:9494",
    db_kwargs={"adbc.quack.token": "..."},
    autocommit=False,
) as conn, conn.cursor() as cur:
    cur.execute("INSERT INTO orders VALUES (1, 'pending')")
    cur.execute("INSERT INTO order_items VALUES (1, 'widget', 2)")
    conn.commit()  # both inserts persist atomically
```

## Connection URL

```
quack://host[:port]
```

| Option              | Default | Notes                                                                    |
|---------------------|---------|--------------------------------------------------------------------------|
| `adbc.uri`          | —       | Required. Pass as the `uri=` kwarg to `quack.connect`.                   |
| `adbc.quack.token`  | (none)  | Authentication token. Server-side `token=>` argument to `quack_serve()`. |
| `adbc.quack.tls`    | `false` | `true` → use `https://` for the underlying HTTP transport.               |

The URI is its own kwarg; everything else goes through `db_kwargs`:

```python
import adbc_driver_quack.dbapi as quack

quack.connect(
    uri="quack://127.0.0.1:9494",
    db_kwargs={
        "adbc.quack.token": "my-secret-token",
        "adbc.quack.tls": "false",
    },
)
```

## Why ADBC and not JDBC?

Both drivers speak the same protocol to the same kind of server. Pick the
one that fits your runtime:

| You're using | Reach for |
|---|---|
| A JVM tool (DBeaver, IntelliJ, Spark, dbt-jdbc, plain `java.sql`) | [`quack-jdbc`](https://github.com/gizmodata/quack-jdbc) |
| Python (`pip install`), Go, Rust, R, anything via ADBC C ABI | this driver |
| You want **zero-copy Arrow data** end-to-end | this driver |

## Repo layout

```
adbc-driver-quack/
├── go.mod, go.sum
├── internal/
│   ├── codec/       — BinaryReader/Writer for DuckDB BinarySerializer
│   ├── quacktype/   — Logical / physical / extra type system + codec
│   ├── message/     — DataChunk, DecodedVector, MessageCodec, VectorCodec
│   └── transport/   — QuackURI parser + net/http transport (IPv4/IPv6 fallback)
├── driver/quack/    — pure-Go ADBC Driver/Database/Connection/Statement impl
├── pkg/quack/       — cgo c-shared wrapper (produces libadbc_driver_quack.{so,dylib,dll})
├── python/          — Python wheel sources (adbc_driver_quack)
└── .github/         — CI: go test, python tests, cibuildwheel matrix, PyPI publish
```

The `internal/` layer is a clean-room Go port of the matching Java
packages in [`quack-jdbc`](https://github.com/gizmodata/quack-jdbc).

## Credits

- Wire format: [DuckDB Quack protocol](https://duckdb.org/docs/current/quack/overview)
- Codec design: ported from
  [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc) (MIT),
  which itself was clean-room ported from
  [`tobilg/quack-protocol`](https://github.com/tobilg/quack-protocol) (MIT)
- ADBC framework: [Apache Arrow ADBC](https://github.com/apache/arrow-adbc) (Apache-2.0)

## License

[MIT](https://github.com/gizmodata/adbc-driver-quack/blob/main/LICENSE) — see `LICENSE` for full attribution.
