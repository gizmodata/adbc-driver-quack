# Changelog

All notable changes to **adbc-driver-quack** are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
