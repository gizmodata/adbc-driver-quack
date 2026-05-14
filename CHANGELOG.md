# Changelog

All notable changes to **adbc-driver-quack** are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added — Real `Commit` / `Rollback` (autocommit-off transactions)

- `Connection.SetOption("adbc.connection.autocommit", "false")` now
  issues `BEGIN TRANSACTION` on the server. Flipping it back to
  `"true"` commits any pending tx.
- `Connection.Commit()` issues `COMMIT` then re-opens a fresh
  `BEGIN TRANSACTION` so the next statement still runs inside a
  transaction (the ADBC contract).
- `Connection.Rollback()` issues `ROLLBACK` and similarly re-opens.
- `Connection.Close()` rolls back any outstanding transaction so we
  don't leak server-side state when callers forget.
- Both `Commit` and `Rollback` return `StatusInvalidState` if called
  while autocommit is still on (matches ADBC's documented contract).
- New Python integration test exercises the full roundtrip: insert +
  commit (persists), insert + rollback (discarded), insert + commit
  (persists), then verifies `SELECT … ORDER BY id` returns `[1, 3]`.
- SQL shape mirrors the `BEGIN TRANSACTION` / `COMMIT` / `ROLLBACK`
  pattern in `gizmosql`'s
  `DuckDBTransactionGuard` / `BeginTransaction` / `EndTransaction`.

### Fixed — Python integration tests (11/11 passing locally)

- `Connection.GetInfo` and `Connection.GetObjects` are now properly
  wired to `getInfoImpl` / `getObjectsImpl` (an earlier commit left
  them stubbed as `NotImplemented` because the routing edit didn't
  apply). All ADBC metadata calls now go through to the real
  implementations.
- `python/adbc_driver_quack/dbapi.py` now constructs the DBAPI
  `Connection` via the correct three-step pattern
  (`AdbcDatabase` → `AdbcConnection` → `dbapi.Connection`) and
  re-exports the standard DBAPI 2.0 module-level constants and
  exception hierarchy. The previous one-step constructor call hit
  `TypeError: Connection.__init__() missing 1 required positional
  argument: 'conn'`.
- Python `test_smoke.py` was rewritten against the actual
  `adbc_driver_manager.dbapi` API surface:
  * `adbc_get_table_types()` returns a `list[str]` (no context manager).
  * `adbc_get_info()` returns a `dict[str|int, Any]` directly.
  * `adbc_get_objects()` returns a `pyarrow.RecordBatchReader`,
    iterated but not with-managed.
  * `test_bulk_ingest` casts the `SUM` result to `BIGINT` to dodge
    the HUGEINT-as-string surface.
  * `test_bad_token_rejected` wraps the `connect()` call (which
    is where the handshake error surfaces) in `pytest.raises`.
- Python CI matrix expanded from `[3.10, 3.12]` to
  `[3.10, 3.11, 3.12]` (an earlier edit didn't land).

### Added — Table constraints (PK + FK) in `GetObjects`

- `getObjectsImpl` now populates each `TableInfo.table_constraints`
  list. Two `duckdb_constraints()` queries per schema (one for
  `PRIMARY KEY`, one for `FOREIGN KEY`) unnest the
  `constraint_column_indexes` + `constraint_column_names` (and
  `referenced_column_names` for FKs) so each row carries one column.
  The result is grouped back up in Go and attached to the matching
  `TableInfo`.
- Foreign-key constraints carry a non-empty
  `constraint_column_usage` list referencing the parent table's
  schema/table/column.
- SQL shape is a direct port of `gizmosql`'s
  `DoGetPrimaryKeys` / `PrepareQueryForGetImportedOrExportedKeys`
  (`gizmodata/gizmosql:src/duckdb/duckdb_server.cpp`).
- Unit test verifies the JSON round-trip with a parent/child PK + FK
  example. Python integration test creates real PK + FK tables and
  asserts both constraint types come back with correct column usage.

### Fixed — Python wheel CI

- `pyproject.toml` no longer carries the
  `License :: OSI Approved :: MIT License` classifier — modern
  setuptools (≥77) rejects the combination of a PEP 639 `license`
  expression and the legacy MIT classifier.
- `setup.py` now skips the `shutil.copy` when
  `ADBC_QUACK_LIBRARY` already points at the package source target
  (previously raised `shutil.SameFileError`).
- Python CI matrix expanded from `[3.10, 3.12]` to
  `[3.10, 3.11, 3.12]`.

### Added — README badges

PyPI version, Python versions (queried from PyPI metadata), Go
module tag, Go CI status, Python CI status, GitHub repo link, and
MIT license — all at the top of the project README.

### Added — GetInfo and GetObjects metadata methods

- `Connection.GetInfo(infoCodes)` now returns a real
  `array.RecordReader` matching `adbc.GetInfoSchema`. Populated codes:
  `InfoVendorName` ("DuckDB (via Quack)"), `InfoVendorVersion`
  (fetched live via `PRAGMA version`), `InfoDriverName`
  ("ADBC Quack Driver - Go"), `InfoDriverVersion` (from
  `runtime/debug.ReadBuildInfo` when available, falling back to
  "0.0.0"), `InfoDriverArrowVersion` ("arrow-go/v18"). Unknown codes
  appear with null values.
- `Connection.GetObjects(depth, catalog, schema, table, column, types)`
  now walks the catalog → schema → table → column hierarchy, honoring
  every depth setting and filter parameter. SQL queries are ports of
  the same ones `QuackDatabaseMetaData` uses on the JDBC side
  (themselves modeled on DuckDB's own JDBC driver):
  `information_schema.schemata` for catalogs/schemas,
  `duckdb_tables() ∪ duckdb_views()` for tables,
  `duckdb_columns()` for columns. Table constraints are reported as an
  empty list pending a follow-up that joins `duckdb_constraints` and
  `information_schema.referential_constraints`.
- New `get_objects_types.go` vendors a small set of
  ADBC `GetObjectsInfo` / `DBSchemaInfo` / `TableInfo` / `ColumnInfo`
  structs and a local `buildGetObjectsRecordReader` (JSON →
  RecordBuilder bound to `adbc.GetObjectsSchema`). We can't import
  `arrow-adbc`'s `internal/driverbase` package across module
  boundaries, so we recreate just the data shapes — JSON-marshal
  round-trip into the standard record builder keeps the wire schema
  exactly identical.
- Unit test verifies the JSON → arrow.Record path round-trips
  catalogs/schemas/tables/columns; Python integration tests cover
  GetInfo (verifies VendorName and DriverName are populated),
  GetObjects at depth=catalogs (empty schemas list), and depth=all
  (creates a probe table, asserts the columns come back).

### Added — Scaffolding

- Repo skeleton: Go module `github.com/gizmodata/adbc-driver-quack`,
  MIT license, README, CHANGELOG, .gitignore, package directory layout
  for `internal/{codec,quacktype,message,transport}`, `driver/quack`,
  `pkg/quack`, `python/`.
- Companion to the
  [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc)
  driver (MIT) — the `internal/` layer will be a clean-room port of the
  matching Java packages in that repo.

## [0.0.0] — _planned_

First substantive release will follow once the codec round-trips and a
minimal read-only ADBC `Driver`/`Database`/`Connection`/`Statement`
returns Arrow `RecordReader`s for a `SELECT 1` against a live Quack
server.
