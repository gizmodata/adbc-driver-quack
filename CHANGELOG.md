# Changelog

All notable changes to **adbc-driver-quack** are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0-alpha.2] — 2026-05-14

Fixes the Windows wheel (which was broken in `0.1.0-alpha.1`).
`0.1.0-alpha.1` is yanked from PyPI.

### Fixed — Windows wheel: MSYS path translation

- The CI build step that sets `ADBC_QUACK_LIBRARY` was emitting a
  Git Bash unix-style path (`/d/a/...`) on Windows runners. Python's
  `Path("/d/a/...")` on Windows rebases at the current drive root,
  causing `setup.py`'s `shutil.copy` to write the DLL to the wrong
  location. The Python wheel then shipped without the bundled DLL at
  the expected path, and `adbc_driver_manager` could not load the
  driver (with a confusing `Could not load \`D\`` error stemming from
  its URI-vs-path parser hitting a path it could not stat).
- `python.yml` now passes `$LIB_PATH` through `cygpath -m` on Windows
  to produce a native `D:/a/...` path before writing to `$GITHUB_ENV`.
- `setup.py` also defensively translates MSYS-style paths so local
  Windows builds (Git Bash) work without that workflow step.

### Fixed — Windows tests now block release

- Removed `continue-on-error: true` from the Windows leg of the
  `test` job. The reason for that override (DuckDB CLI install
  fragility) no longer applies now that we use the in-process
  `duckdb` Python package. If a Windows test fails, the wheel
  matrix and PyPI publish are now blocked — same gate as Linux and
  macOS.

### Fixed — Windows c-shared library artifact upload

- The `Upload c-shared driver library` step in `test` was conditional
  on `matrix.os != 'windows-latest'`, dating back to the same Windows
  best-effort era. Removed — Windows users now also get the DLL
  uploaded as a per-OS artifact for PR review.

## [0.1.0-alpha.1] — 2026-05-14 — **yanked**

First publishable release. Wheels + sdist on PyPI, Go module tag
`v0.1.0-alpha.1`, c-shared libraries (linux x86_64, linux arm64,
macOS arm64, windows x86_64) attached to the GitHub Release with
`SHA256SUMS`. Cuts the line between "internal scaffolding" and
"users can `pip install adbc-driver-quack`."

**Platform note:** macOS x86_64 (Intel) and linux/macOS 32-bit are
not built in this release. Apple silicon (arm64) is the present-day
default for Mac developers; Intel Mac users can build locally via
`go build -buildmode=c-shared` and point `ADBC_QUACK_LIBRARY` at the
result before `pip install`.

### Added — Nested types (LIST / STRUCT / ARRAY / MAP)

- `LogicalTypeIDList` → `arrow.List<T>`, `LogicalTypeIDArray` →
  `arrow.FixedSizeList<T, N>`, `LogicalTypeIDStruct` →
  `arrow.Struct<fields...>`, `LogicalTypeIDMap` →
  `arrow.Map<K, V>` (encoded as `LIST<STRUCT<key, value>>` on the wire,
  as DuckDB does internally).
- New `appendBoxedScalar` / `appendBoxedSlice` helpers in
  `arrow_conv.go` dispatch a single `interface{}` value into the right
  builder, recursing through nested types (`LIST<LIST<INTEGER>>`,
  `STRUCT<a: LIST<INTEGER>>`, etc.).
- The wire decoder already produced `[]interface{}` for list-likes and
  `map[string]interface{}` for structs, so no codec changes were
  needed — the gap was purely on the arrow conversion side.
- Python integration test
  `test_nested_types_round_trip` exercises all four nested-type
  surfaces in one query:
  `SELECT [1,2,3]::INTEGER[3], [10,20]::INTEGER[], {'a':1, 'b':'hello'}, MAP {'x':1, 'y':2}`
  and asserts each column comes back with the correct
  `pyarrow.types.is_*` check + value round-trip.

### Added — ADBC validation conformance suite scaffolding

- New `validation_test.go` plugs the driver into
  `apache/arrow-adbc/go/adbc/validation`'s generic conformance suite
  (DatabaseTests, ConnectionTests, StatementTests).
- `QuackQuirks` declares: bulk-ingest=append-only, concurrent
  statements, transactions, GetSetOptions, error-on-incompatible-
  ingest-schema. Disables: dynamic parameter binding (Quack 1.0 has
  no `PARAMETER_BIND` message — see Skipped below), partitioned data,
  statistics, execute-schema, current-catalog/schema, GetParameterSchema.
- Suite is **opt-in**: skipped unless `QUACK_VALIDATION_URI` env var
  points at a running Quack server. Hermetic for normal CI; available
  to developers verifying conformance against a real server.

### Skipped — Parameter binding (Quack 1.0 protocol limitation)

- Quack 1.0's message catalog (ConnectionRequest, PrepareRequest,
  FetchRequest, AppendRequest, …) has no `PARAMETER_BIND` /
  `BIND_REQUEST` message. Slots 5 and 6 are reserved, suggesting
  future expansion.
- Driver continues to inline parameters client-side (the same approach
  the JDBC sibling takes). Dynamic binding will land when the upstream
  protocol gains it.

### Added — Windows wheel CI (best-effort)

- `windows-latest` added to `python.yml`'s test matrix. Marked
  `continue-on-error: true` while the CGO + DuckDB-via-subprocess
  fixture story is hardened on Windows runners.
- Defaulted the shell to `bash` so the build steps run unchanged;
  DuckDB CLI install on Windows uses `Invoke-WebRequest` to fetch the
  official zip.

### Expanded — Python test matrix (3.10 → 3.14)

- Test matrix expanded from `[3.10, 3.11, 3.12]` to
  `[3.10, 3.11, 3.12, 3.13, 3.14]` so we don't drift behind the
  Python release cadence. `pyproject.toml` classifiers updated to
  match.

### Changed — Integration test fixture uses in-process DuckDB

- `python/tests/conftest.py` now spins up DuckDB via the official
  [`duckdb`](https://pypi.org/project/duckdb/) Python package
  (`duckdb.connect(":memory:", config={"allow_unsigned_extensions": "true"})`)
  instead of spawning the `duckdb` CLI as a subprocess. The Quack
  listener runs in a background thread inside DuckDB; the test client
  connects to `quack://127.0.0.1:<port>` exactly as it would against a
  real deployed server.
- Removes the `Install DuckDB CLI` steps from `python.yml` (which was
  the most fragile part of the Windows CI leg — `Invoke-WebRequest` →
  unzip → prepend to PATH). Windows CI now does the same `pip install`
  every other platform does.
- New `duckdb >=1.5.2` entry in the `test` optional-dependencies group
  in `pyproject.toml` so the fixture's dependency is hermetic and
  pinnable.
- All 20 integration tests pass unchanged — the only thing that
  changed is *how the server starts*, not the wire protocol or the
  driver behavior.

### Quickstart in README

- Top-of-README quickstart with `pip install`, a 3-line connect +
  fetch snippet, a streaming-large-result example, a bulk-ingest
  example, and an explicit transactions example. Replaces the
  placeholder "Planned quickstart" section.

### Added — Streaming `ExecuteQuery` (`#1`)

- `session.cursor()` returns a streaming `*cursor`. Only the chunks
  delivered with the initial `PREPARE_RESPONSE` are eagerly buffered;
  subsequent chunks are pulled via `FETCH_REQUEST` on demand as the
  caller iterates the `RecordReader`.
- New `record_reader.go` implements `array.RecordReader` over the
  cursor — one `Next()` call drives at most one Quack `FETCH_REQUEST`.
- `Statement.ExecuteQuery` now returns this streaming reader instead of
  draining every chunk up front. Memory for a million-row result is
  bounded by one DuckDB DataChunk (~2k rows).
- ADBC driver-manager wraps the Go `RecordReader` as an
  `ArrowArrayStream` (C Data Interface), preserving streaming all the
  way through to pyarrow's `RecordBatchReader` — the Python side
  pulls one batch per server fetch.
- `Statement.ExecuteUpdate` and every metadata query continue to use
  the new `session.drainPrepared` convenience (eager drain) because
  their results are tiny.
- Python integration test
  `test_streaming_large_result_set` walks a 100k-row `range()` query
  and asserts the reader yields multiple batches (not one giant
  materialized record) — would fail loudly if streaming regressed.

### Added — `HUGEINT` / `UHUGEINT` → arrow `Decimal128(38, 0)` (`#2`)

- Replaced the placeholder "string-format the int128" path with an
  exact `Decimal128(38, 0)` mapping. Precision 38 holds any signed
  int128 losslessly (max int128 ≈ 1.7×10³⁸ < 10³⁸).
- `Decimal128Builder` case in `buildColumn` now accepts both
  `*big.Rat` (DuckDB `DECIMAL`) and `*big.Int` (HUGEINT / UHUGEINT).
- `SUM(INTEGER)`, `COUNT_STAR()`, and every other DuckDB function that
  returns HUGEINT now flow through as native decimal columns instead
  of strings — `int(row["s"])` in the existing
  `test_bulk_ingest` works without the `CAST(... AS BIGINT)` workaround.
- Documented UHUGEINT precision caveat: values above 10³⁸-1 (uint128's
  39th decimal digit) lose precision; users wanting full unsigned
  range should `CAST(... AS DECIMAL(39, 0))` in SQL (uses Decimal256
  on the server). DBeaver / Polars / ibis don't hit this in practice.

### Added — Expanded `GetInfo` codes (`#9`)

- `InfoVendorSql` reports `true` (Quack speaks SQL end-to-end).
- `InfoVendorSubstrait` reports `false`.
- `InfoDriverADBCVersion` reports `AdbcVersion1_1_0` (1_001_000) — the
  framework version Go driver-base targets, so driver-manager and
  other ADBC consumers can choose the right call surface.
- Default info-code set now covers all eight codes, so a no-argument
  `Connection.GetInfo()` returns the full driver/vendor probe in one
  call.

### Added — Connection pool friendliness (`#13`)

- Python integration test
  `test_connection_pool_friendliness` exercises 50 sequential
  open/close cycles (catching connection-id, socket, or server-side
  state leaks) and then 16 concurrent connections in a thread pool
  (catching races in the session/connection-id allocator).
- No driver code changes were needed — the session already issues
  `DISCONNECT` on `Connection.Close()` and the connection-id sequence
  is atomic — but the test locks in the contract.

### Added — CI artifact uploads + GitHub Release bundle

- `python.yml` now uploads `libadbc_driver_quack.{so,dylib,dll}` for
  every PR/main run (one upload per OS, keyed off the 3.12 job).
  Anyone reviewing a PR can grab the c-shared lib without running
  `make -C pkg/quack` locally.
- `packaging.yml` (tag triggered) builds the c-shared lib for every
  release target (linux-amd64, darwin-amd64, darwin-arm64,
  windows-amd64), checksums each binary, and attaches them — plus a
  combined `SHA256SUMS` — to the GitHub Release. The PyPI publish
  step is unchanged.
- Pre-release detection now flags `-alpha` / `-beta` / `-rc` tags as
  GitHub pre-releases automatically.

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
