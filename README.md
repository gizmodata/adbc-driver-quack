# adbc-driver-quack

**An [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for [DuckDB's Quack remote protocol](https://duckdb.org/docs/current/quack/overview).**

[![PyPI](https://img.shields.io/pypi/v/adbc-driver-quack?label=PyPI&logo=pypi&logoColor=white&color=blue)](https://pypi.org/project/adbc-driver-quack/)
[![Python versions](https://img.shields.io/pypi/pyversions/adbc-driver-quack?label=Python&logo=python&logoColor=white)](https://pypi.org/project/adbc-driver-quack/)
[![Go module](https://img.shields.io/github/v/tag/gizmodata/adbc-driver-quack?label=Go%20module&logo=go&logoColor=white&sort=semver)](https://pkg.go.dev/github.com/gizmodata/adbc-driver-quack)
[![Go CI](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/go.yml/badge.svg)](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/go.yml)
[![Python CI](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/python.yml/badge.svg)](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/python.yml)
[![GitHub Repo](https://img.shields.io/badge/github-gizmodata%2Fadbc--driver--quack-181717?logo=github)](https://github.com/gizmodata/adbc-driver-quack)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Returns Apache Arrow `RecordBatch`es directly from a remote DuckDB server
speaking Quack. Supports the standard ADBC bulk-ingest path
(`Statement.BindStream` → `APPEND_REQUEST`) for fast column-oriented loads.

Distributed as:
- a Go module — `github.com/gizmodata/adbc-driver-quack`
- a `pip install adbc-driver-quack` wheel for Python (macOS / Linux / Windows × x64 / arm64)

> **Status:** Scaffolding — under active development. The companion
> [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc) JDBC
> driver is the same protocol from the JVM; that one is at
> [v0.1.0-alpha.1 on Maven Central](https://central.sonatype.com/artifact/com.gizmodata/quack-jdbc/0.1.0-alpha.1).

## Why ADBC and not JDBC?

Both drivers speak the same protocol to the same kind of server. Pick the
one that fits your runtime:

| You're using | Reach for |
|---|---|
| A JVM tool (DBeaver, IntelliJ, Spark, dbt-jdbc, plain `java.sql`) | [`quack-jdbc`](https://github.com/gizmodata/quack-jdbc) |
| Python (`pip install`), Go, Rust, R, anything via ADBC C ABI | this driver |
| You want **zero-copy Arrow data** end-to-end | this driver |

## Planned quickstart (after first release)

```python
import adbc_driver_quack.dbapi
import pyarrow

conn = adbc_driver_quack.dbapi.connect(
    "quack://127.0.0.1:9494",
    db_kwargs={"adbc.quack.token": "my-token"},
)
with conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer")
    table: pyarrow.Table = cur.fetch_arrow_table()
    print(table)
```

Bulk ingest:

```python
import pyarrow as pa
import adbc_driver_quack.dbapi

table = pa.table({"id": [1, 2, 3], "name": ["alice", "bob", "carol"]})
with adbc_driver_quack.dbapi.connect(...) as conn, conn.cursor() as cur:
    cur.adbc_ingest("customers", table, mode="append")  # one APPEND_REQUEST per RecordBatch
```

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

[MIT](LICENSE) — see `LICENSE` for full attribution.
