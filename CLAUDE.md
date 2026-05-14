# CLAUDE.md

Project notes for Claude and other contributors.

## What this is

A Go-based [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver
for [DuckDB's Quack remote protocol](https://duckdb.org/docs/current/quack/overview),
distributed as a Go module and as a Python wheel
(`pip install adbc-driver-quack`).

Companion to [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc)
(JDBC driver, MIT, on Maven Central as `com.gizmodata:quack-jdbc`).

## Reference material

Cached under `/tmp/adbc-ref/`:
- `arrow-adbc/` — full shallow clone of `apache/arrow-adbc`. Read
  `go/adbc/adbc.go` for interface contracts; `go/adbc/driver/flightsql/`
  for the canonical Go driver shape; `go/adbc/validation/` for the
  conformance suite to plug into `go test`; `go/adbc/pkg/_tmpl/` for the
  cgo wrapper templates.
- `adbc-driver-gizmosql/` — shape reference for Python-side packaging,
  CI conventions, integration test conftest.
- `duckdb_adbc.cpp`, `duckdb_adbc.h` — DuckDB's own ADBC driver (C++,
  baked into duckdb/duckdb). See `Ingest()` at line 1215 for the
  Arrow → DataChunk → appender pattern Quack mirrors via APPEND_REQUEST.

Java source to port from: `/Users/philip/LocalOnly/git/quack-jdbc/src/main/java/com/gizmodata/quack/jdbc/`.

The full research document lives at `/tmp/adbc-research.md` (584 lines)
and `/tmp/duckdb-jdbc-metadata-notes.md` for the SQL queries to port
into ADBC's `GetObjects`/`GetTableSchema`.

## Build order

Pinned in CHANGELOG `[Unreleased]` and tracked in the task list. Roughly:

1. Scaffold + CI placeholders (this commit)
2. `internal/codec` — BinaryReader/Writer, Validity, HugeIntParts
3. `internal/quacktype` — LogicalType, PhysicalType, ExtraTypeInfo
4. `internal/message` — DataChunk, DecodedVector, MessageCodec, VectorCodec
5. `internal/transport` — QuackURI, HTTP client (IPv4/IPv6 fallback)
6. `driver/quack` — minimal ADBC read path
7. `pkg/quack` — cgo wrapper
8. `driver/quack` metadata methods
9. Bulk ingest (Statement.Bind/BindStream + executeIngest)
10. `python/` Python wheel + DBAPI
11. Integration tests
12. Packaging matrix CI

## Conventions

- **Go 1.23 floor.** Use generics where they help; avoid `any` when a
  concrete type fits.
- **No `panic` across the cgo boundary.** Mirror flightsql's
  `globalPoison` + `defer/recover` from the start.
- **Apache Arrow Go pin: v18.** Match upstream flightsql and the ADBC
  framework. Don't lag behind.
- **Option naming:** `adbc.quack.<noun>` (e.g. `adbc.quack.token`,
  `adbc.quack.tls`, `adbc.quack.rpc.timeout_seconds.fetch`).
- **DriverInfoName / vendor string:** "ADBC Quack Driver - Go".
- **Python floor:** `>=3.10`; pyarrow `>=14.0.1`.

## DBeaver compatibility — port from JDBC

The SQL queries that drive ADBC's `Connection.GetObjects` /
`GetTableSchema` / `GetTableTypes` should be lifted directly from
`QuackDatabaseMetaData.java` in the JDBC repo. Those queries are in turn
modeled query-for-query on DuckDB's own JDBC driver, so the
introspection shape is consistent across DBeaver / DataGrip / etc.
