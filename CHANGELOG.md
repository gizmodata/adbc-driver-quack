# Changelog

All notable changes to **adbc-driver-quack** are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- `LICENSE` holds only the MIT License text, so GitHub (and the license badge) recognize it as MIT; the attributions that followed it are in `NOTICE`.
- CI: the windows/arm64 build downloads a pinned, checksummed llvm-mingw (20261104) instead
  of asking the GitHub API for the latest release, which can be rate-limited and fail a release.

## [1.0.3] - 2026-11-09

The c-shared library now meets the
[ADBC shared-library requirements](https://adbc-drivers.org/building-drivers/shared-library-requirements.html).

### Changed

- **The library exports only `Adbc*` symbols**, plus the deprecated `QuackDriverInit` alias.
  The wrapper's own Go exports (`QuackDatabaseNew`, ...) and cgo's glue are no longer
  visible to other libraries in the process. They are hidden by a linker export list per
  platform (`pkg/quack/exports_*`), applied by `scripts/build_lib.sh`, which CI, the Makefile
  and local builds all use. CI checks each build with `go run ./scripts/checkexports`.
- **The Python package and the driver manifest load the driver through `AdbcDriverQuackInit`**,
  the entrypoint the driver manager derives from the file name. `QuackDriverInit` still works
  (manifests written by earlier releases name it) and will be removed at the next major
  version.
- **GitHub Release libraries come as one `libadbc_driver_quack-<os>-<arch>.tar.gz` per
  platform**, holding `libadbc_driver_quack.<ext>` with LICENSE and NOTICE. The previous
  platform-suffixed file names (`libadbc_driver_quack-linux-amd64.so`) did not match the
  entrypoint the driver manager derives from a file name.

## [1.0.2] - 2026-11-09

### Fixed

- **A second query on a connection broke a result still being read.** The Quack server keeps
  one result per connection, and a new query discards an unfinished one, so reading the first
  result after another cursor (or a metadata call such as `GetObjects`) ran on the same
  connection failed with `Invalid Input Error: Result has been closed`. The driver now reads
  the rest of the open result into memory before sending the next statement, as quack-jdbc
  does, so both readers keep working; the cost is the memory for the rest of the older
  result. If that read fails, the older reader reports the error instead of ending early.

## [1.0.1] - 2026-11-09

The first stable release, for DuckDB 2.0 (1.0.0 was tagged but never published: its release
gate caught the cancel race fixed below).

### Fixed in 1.0.1

- **A statement sent right after a cancel could be interrupted.** The server-side CANCEL for
  an abandoned statement went out asynchronously; when it reached the server after the
  connection's next PREPARE had begun, DuckDB's per-connection interrupt stopped that next
  statement ("INTERRUPT Error: Interrupted!"). The next statement now waits for an in-flight
  CANCEL to finish. A live test delays the CANCEL to prove the ordering (it fails without
  the fix).

### Everything new in 1.0

The driver speaks Quack protocol version 3, the one DuckDB 2.0's quack extension serves. **DuckDB 1.5 servers (Quack protocol 1) are no
longer supported**; pin `adbc-driver-quack==0.1.0a11` for those.

### Added

- **DuckDB 2.0 / Quack protocol 3**: the new message layout (header, body and a raw chunk
  blob), random per-query ids, FETCH by dense batch index with acknowledgements and a
  checked batch total (a short result is an error, never a truncated one), DuckDB 2.0's
  VARCHAR vector encoding, and the TIMESTAMP_TZ_NS and TUPLE types.
- **Ingest through SEND_DATA**: Arrow record batches are encoded straight into DuckDB
  DataChunks and streamed into `INSERT ... SELECT * FROM scan_data_from_quack_client(...)`,
  several 2,048-row chunks per request, the way DuckDB's own client writes to an attached
  Quack database. Ingest now covers every Arrow type DuckDB's own Arrow import does
  (string/binary views, large types, dictionaries, decimals of every width, all time and
  timestamp units, durations and intervals, fixed-size binary, lists, list views,
  fixed-size lists, structs, maps) with DuckDB's own type mapping, plus `catalog_name`.
- **VARIANT** results as Arrow's canonical `arrow.parquet.variant` extension, byte-for-byte
  what DuckDB's Arrow export (and so Hardcharger) returns, at any nesting depth; ingest of
  `arrow.parquet.variant` columns into VARIANT. Members a Parquet Variant cannot hold
  (INTERVAL, BIT, BIGNUM, TIMETZ, TIME_NS, integers over 38 digits) are an error naming the
  type.
- **GEOMETRY** as `geoarrow.wkb` (WKB, CRS in the extension metadata) and **UUID** as
  `arrow.uuid`, both ways.
- **TLS like DuckDB 2.0**: HTTPS by default for every host but localhost / 127.0.0.1 / ::1
  (`adbc.quack.tls` overrides), and `adbc.quack.ssl_fingerprint` to pin a `quack_serve`
  self-signed certificate. `quack:host:port` (DuckDB's spelling) is accepted as a URI.
- **Heartbeat lease**: `adbc.quack.heartbeat_timeout_seconds` (default 60), renewed in the
  background so idle connections survive; `adbc.quack.client_id`.
- **Cancellation**: a cancelled or timed-out call cancels its statement on the server.
- **ADBC 1.1 surface**: `GetOption`/`SetOption` on database, connection and statement,
  current catalog and schema (`adbc.connection.catalog` / `db_schema`), `ExecuteSchema`
  (which `adbc_scanner`'s `adbc_scan` needs), rows affected for INSERT/UPDATE/DELETE/MERGE
  and ingest, UNIQUE constraints in `GetObjects`.
- **Server errors with ADBC statuses and SQLSTATEs** from DuckDB's exception type: a missing
  table is NOT_FOUND / 42S02, an existing one ALREADY_EXISTS / 42S01, a syntax error
  INVALID_ARGUMENT / 42601, a constraint violation INTEGRITY / 23000. The `EXCEPTION_WHAT`
  header is reported on HTTP failures.
- **Conformance**: apache/arrow-adbc's Go validation suite and the adbc-drivers.org
  conformance suite (`validation/`) run against a DuckDB 2.0 server in every CI run and gate
  every release. Live Go tests for the heartbeat lease (with its negative twin) and for
  multi-batch streaming.
- **Hardcharger**: tests stream a Quack table (VARIANT and GEOMETRY included) into a live
  Hardcharger and run `adbc_scanner` inside Hardcharger; the README documents both, and why
  Hardcharger and Chargewire suit production.
- Python 3.15 support, tested in CI. GizmoData branding, NOTICE, `docs/assets` logo.

### Changed

- Python 3.11 or later (3.10 reached end of life on 2026-10-01);
  `adbc-driver-manager >= 1.13.0`; test extras on duckdb 2.0, pytest 9.2, pandas 3.1 and
  hardcharger 2.10. Development Status: Production/Stable.
- TIMESTAMP_S / _MS / _NS keep their units in Arrow (`timestamp[s/ms/ns]`), TIME_NS is
  `time64[ns]`, TIMETZ is `time64[us]`.
- Go: arrow-adbc 1.13.0, arrow-go 18.8.0, testify 1.12.1, x/* modules; Go 1.27.3 in CI
  (`go.mod` floor 1.26).
- Unknown database and statement options are NOT_IMPLEMENTED instead of being ignored;
  closing a connection or statement twice is INVALID_STATE.
- `GetInfo` reports the driver version, the server's DuckDB version (from the handshake,
  without running a query) and the arrow-go version.
- CI: a conformance job, a DuckDB 2.0 server for the Go suites, Hardcharger on Linux, a
  wheel install check on AlmaLinux 8 (glibc 2.28), superseded runs cancelled,
  documentation-only changes skipped, Node 24 action majors.

### Fixed

- **Naive TIMESTAMP columns came back tagged UTC** (`timestamp[us, tz=UTC]`): arrow-go's
  `FixedWidthTypes.Timestamp_*` constants carry a time zone.
- **TIMESTAMP_NS lost its nanoseconds** (returned as microseconds).
- **Values nested in LIST/STRUCT/MAP** of type DATE, TIME, TIMESTAMP, UUID, INTERVAL or MAP
  came back NULL; an unexpected value is now an error.
- **ExecuteUpdate returned the number of result rows** (1) instead of the rows affected.
- **Ingest after a same-named temporary table** wrote into the temporary table; targets are
  now fully qualified.
- **Multi-row parameter bindings** failed with "mismatch schema" when a row held a NULL;
  bound values are cast to the bound column's type.
- **GetObjects primary/foreign keys** listed columns in table order instead of declared order.
- **GetTableSchema** guessed types from DuckDB's type names (TIMESTAMP_S, nested types and
  unsigned integers came back wrong); it now uses the server's own types.
- **Linux wheels** are built with `zig cc` against glibc 2.17, so the manylinux2014 tag is
  true (they needed the build runner's glibc), and the **macOS wheel** is tagged
  `macosx_13_0_arm64`: it claimed `universal2` with an arm64-only library inside, and macOS
  12, which Go 1.27 does not support.
- Exported VARIANT (and other extension) arrays no longer reach C Data consumers without
  their storage children.
- The Python test fixtures fail, instead of skipping, when the Quack server can't start.

### Removed

- DuckDB 1.5 / Quack protocol 1 support, and APPEND_REQUEST ingest (gone from the protocol).
- Python 3.10.


## [1.0.0] - 2026-11-09 [YANKED]

Tagged but never published: the tag's release gate failed on a cancel race. Its changes, and
the fix, shipped as 1.0.1.

## [0.1.0-alpha.11] — 2026-08-26

### Added
- Client-side parameter binding: values bound with `Bind`/`BindStream` (or
  DBAPI `parameters=`) are rendered as DuckDB literals in place of `?`, once per
  bound row. Makes DuckDB `adbc_scanner` filter pushdown work against Quack.
- README section and tests for DuckDB's `adbc_scanner` (`CREATE SECRET` +
  `ATTACH`) and Columnar's `adbc` extensions.

## [0.1.0-alpha.10] — 2026-08-26
### Fixed
- macOS wheel is built with `MACOSX_DEPLOYMENT_TARGET=12.0` so it installs on
  macOS 12+ instead of only the runner's release (was tagged `macosx_26_0`).

### Changed

- CI: suppressed the cosmetic `-Wdll-attribute-on-redeclaration` clang
  warnings from cgo's generated export header on the windows/arm64
  build (GitHub's problem matcher rendered them as red error
  annotations despite the build succeeding), and bumped
  `softprops/action-gh-release` v2 → v3 (Node 24).

## [0.1.0-alpha.9] — 2026-07-22

### Added

- **Extra HTTP headers**: `adbc.quack.http.header.<Name>` database
  options add static headers to every request (proxy/LB auth),
  mirroring the `EXTRA_HTTP_HEADERS` parameter duckdb-quack added to
  its client secret (duckdb/duckdb-quack#204). Accepted only as ADBC
  options, never as URL query parameters; protocol-owned headers
  (`Content-Type`, `Accept`, `Host`, `Content-Length`) are reserved;
  an empty value clears a previously-set header.
- **ADBC connection-profile enablement**: `python -m adbc_driver_quack
  install-manifest` (and `adbc_driver_quack.install_manifest()`) writes
  a `quack.toml` driver manifest — with the non-default
  `QuackDriverInit` entrypoint and the wheel's absolute library path —
  into the environment's `etc/adbc/drivers/` or the per-user ADBC
  config dir. With it installed, the driver resolves by name from
  connection profiles (`driver = "quack"`), by bare URI scheme
  (`adbc_driver_manager.dbapi.connect(uri="quack://...")`), and via
  `profile://` URIs. README documents the profile workflow.
- Integration tests covering the new surface end-to-end against the
  in-process Quack server: manifest name resolution, `quack://` scheme
  resolution, `profile=` connect with `{{ env_var(...) }}` token
  substitution, and extra-header options through the full stack. CI's
  Python job now runs the unit suite too (previously
  `-m integration` only), picking up the manifest-writer tests.

### Changed

- Python wheel now requires `adbc-driver-manager >= 1.11.0` (first
  release with connection-profile support; manifests landed in 1.7.0).
- Verified against DuckDB v1.5.5 (2026-07-22), which bumps the vendored
  quack extension: wire protocol unchanged (QUACK_VERSION 1); the
  server floor stays v1.5.3 and the `duckdb >=1.5.3` test dependency
  resolves to 1.5.5 automatically.
- CI: releases now ship a **windows/arm64 c-shared driver DLL** (built
  natively on `windows-11-arm` with llvm-mingw) for Go/C/driver-manifest
  consumers. A win_arm64 *wheel* is deliberately not published yet:
  `pyarrow` and `adbc-driver-manager` have no win_arm64 wheels on PyPI,
  so ours could never pip-install there — flip the matrix `dll_only`
  flag once upstream ships them.
- CI: silenced Homebrew tap-trust warnings on macOS runners by
  untapping the runner image's pre-installed `aws/tap`.

- Bumped `github.com/apache/arrow-go/v18` v18.6.0 → v18.7.0 and
  refreshed indirect Go module dependencies (otel v1.44.0, klauspost
  compress/cpuid, lz4, golang.org/x/* — see `go.mod`).
  `arrow-adbc/go/adbc` stays at v1.11.0 (latest release).

## [0.1.0-alpha.8] — 2026-06-12

### Fixed — parity with quack-jdbc `0.2.0-alpha.x`

- **HTTPS transport keeps the original hostname** in request URLs
  instead of replacing it with a resolved IP address, preserving TLS
  SNI and certificate hostname verification for gateways and load
  balancers that route by hostname. Plain HTTP endpoints still expand
  to resolved address candidates for the existing localhost IPv4/IPv6
  fallback. (Port of quack-jdbc `d942788`.)
- **Manual-commit transactions now open lazily.** Disabling autocommit
  no longer issues an immediate `BEGIN TRANSACTION`; the transaction
  opens just before the first statement actually executes, and
  `Commit`/`Rollback` with nothing pending are no-ops instead of
  leaving an idle server-side transaction pinned open between
  statements. This matters for the Python DBAPI, whose connections
  default to `autocommit=False`. (Port of quack-jdbc `8c25f6f`.)
- The documented `adbc.quack.tls` option is now actually honored —
  previously only the `tls` / `useEncryption` URL query parameters
  enabled HTTPS and the ADBC option was silently ignored.

### Added

- **Indirect token providers**: `adbc.quack.token_env` (read the token
  from an environment variable) and `adbc.quack.token_file` (read it
  from a local file), with the same precedence as quack-jdbc:
  `token` > `password` > `token_env` > `token_file`. Both are accepted
  only as ADBC options and rejected as URL query parameters, so a
  pasted `quack://` URL cannot exfiltrate a local secret. (Port of
  quack-jdbc `afb8249` + `995279c`.)
- **Configurable HTTP timeouts**: `adbc.quack.rpc.timeout_seconds.connect`
  (default 10) and `adbc.quack.rpc.timeout_seconds.request` (default 60),
  as plain seconds or a Go duration string. The JDBC-style
  `connectTimeout`/`requestTimeout` URL query parameters are accepted
  for interop with `jdbc:quack://` URLs. (Port of quack-jdbc `322740d`.)
- Python `DatabaseOptions` enum gained `TOKEN_ENV`, `TOKEN_FILE`,
  `CONNECT_TIMEOUT`, and `REQUEST_TIMEOUT`.

## [0.1.0-alpha.7] — 2026-05-24

### Changed — Quack is now a core DuckDB extension (v1.5.3+)

- DuckDB v1.5.3 (announced 2026-05-20) ships `quack` as a **core**
  extension. No more `core_nightly` repository and no more
  `-unsigned` / `allow_unsigned_extensions` requirement — `INSTALL
  quack; LOAD quack;` Just Works against any DuckDB v1.5.3+ session.
  README quickstart and the Python integration-test `conftest.py`
  are updated accordingly.
- Bumped the `test` extra to require `duckdb >=1.5.3` (was
  `>=1.5.2`).
- Verified protocol compatibility: the full Python integration suite
  (26 tests) passes against DuckDB 1.5.3 with no driver-side
  changes.

### CI — GitHub Release body now pulled from CHANGELOG

- The `github-release` job now checks out the repo, extracts the
  matching `## [VERSION]` section from `CHANGELOG.md` (same awk
  pattern as `gizmodata/gizmosql`), and passes it to
  `softprops/action-gh-release` via `body_path`.
  `generate_release_notes: true` is kept as a fallback for tags
  whose CHANGELOG section is missing.

## [0.1.0-alpha.6] — 2026-05-15

### Fixed — README bulk-ingest example silently lost data

- The `0.1.0-alpha.5` README "Bulk ingest" snippet used the default
  `quack.connect()`, which (per PEP 249) is **autocommit-off**. The
  `CREATE` + append ran in a transaction that was rolled back when the
  `with` block closed the connection: `adbc_ingest` returned the row
  count it sent, but nothing persisted. The example now passes
  `autocommit=True`, with a prominent note plus an explicit
  `conn.commit()` variant for the transactional path.
- Hardened the regression test: `test_readme_bulk_ingest` now re-reads
  from a **fresh connection** to prove persistence (the old test read
  back on the same uncommitted transaction, so it never caught this),
  and a new `test_readme_bulk_ingest_explicit_commit` pins both the
  rollback-on-close footgun and the `conn.commit()` path.

No driver code changes from `0.1.0-alpha.5` — the bulk-ingest modes
themselves were correct; only the documented usage was wrong.

## [0.1.0-alpha.5] — 2026-05-15

### Added — Bulk-ingest modes

- All four standard ADBC ingest modes now work, not just `append`:
  `create` (the default), `append`, `replace`, and `create_append`.
  Previously the `adbc.ingest.mode` option was silently ignored and
  every mode collapsed to append-only behavior, so any mode that
  needed table DDL failed against a non-existent table.
- Table DDL for the create-family modes is generated from the bound
  Arrow schema (`CREATE TABLE` / `CREATE OR REPLACE TABLE` /
  `CREATE TABLE IF NOT EXISTS`), mirroring DuckDB's own ADBC driver.
  `create` against an existing table now returns `ALREADY_EXISTS`.
- `adbc.ingest.temporary` is honored (`CREATE TEMPORARY TABLE`).
- Bulk ingest participates in the connection's transaction: with
  autocommit off, ingested rows roll back / commit with the txn.
- Regression coverage: hermetic Go DDL/option tests
  (`driver/quack/bulk_ingest_test.go`) plus per-mode and in-transaction
  end-to-end Python tests mirroring the README.

## [0.1.0-alpha.4] — 2026-05-14

README-only release. Switches the quickstart from `127.0.0.1` to
`localhost` everywhere so it matches quack's own server-side default
(`CALL quack_serve()` with no address binds to `localhost`). With our
transport's existing IPv4/IPv6 fallback, `localhost` is robust on
either family — whereas `127.0.0.1` on the client only works when the
server is also explicitly bound to IPv4.

Added a short troubleshooting note for the rare `connection refused`
case where someone's `/etc/hosts` makes the two ends pick different
address families.

No code changes from `0.1.0-alpha.3` — same wheels, just a fresh
release so PyPI's project page renders the updated README.

## [0.1.0-alpha.3] — 2026-05-14

Fixes the **packaging** for real this time. `0.1.0-alpha.2` shipped a
single `py3-none-any.whl` that contained whichever platform binary
happened to upload last (Windows `.dll`, as it turns out). macOS and
Linux users got a wheel with no usable binary and saw
`FileNotFoundError: ... Looked for: libadbc_driver_quack.dylib`.
`0.1.0-alpha.2` is yanked.

### Fixed — Wheel platform tagging

- `setup.py` now declares a `BinaryDistribution` subclass of
  `setuptools.dist.Distribution` whose `has_ext_modules()` returns
  `True`. setuptools picks up that signal and tags each wheel with
  the build host's platform (e.g. `macosx_11_0_arm64`,
  `manylinux2014_x86_64`, `manylinux2014_aarch64`, `win_amd64`)
  instead of the default `py3-none-any`.
- A `BinaryWheel(bdist_wheel)` subclass also pins the Python ABI tag
  to `py3-none` (not the build host's `cp<ver>-cp<ver>`) — our
  bundled lib isn't a CPython extension, any Python 3.10+ can load
  it, so one wheel per platform suffices (4 total vs 5×4=20).
- Linux tags are rewritten from bare `linux_*` to `manylinux2014_*`
  (PyPI rejects the former; Go-cgo binaries link only to ancient
  glibc symbols so manylinux2014 is honest, not optimistic).
- All four matrix wheels (linux x86_64, linux arm64, macOS arm64,
  windows x86_64) now have distinct filenames, all land on PyPI, and
  `pip install adbc-driver-quack` selects the right one per user
  platform.

### Fixed — `pyarrow` is now a core dependency

- `cursor.fetch_arrow_table()` / `cursor.fetch_record_batch()` (every
  quickstart example) require `pyarrow`. Previously it lived in the
  `dbapi` optional extra, which meant `pip install adbc-driver-quack`
  alone would import-error the first time a user called
  `fetch_arrow_table()`.
- Moved to the main `dependencies` list. The `dbapi` extra now only
  carries `pandas` (still optional — only needed for pandas-side
  conversions).

## [0.1.0-alpha.2] — 2026-05-14 — **yanked**

Fixes the Windows wheel (which was broken in `0.1.0-alpha.1`) and
puts the rich README on PyPI. `0.1.0-alpha.1` is yanked from PyPI.

### Changed — `pyproject.toml` and `setup.py` moved to repo root

- The Python build files are now at the repo root, matching the
  convention used by `adbc-driver-gizmosql` and `ibis-gizmosql`. The
  driving reason was the PyPI README: setuptools refuses to read
  `../README.md` (security sandbox), so the only clean way to ship
  the rich repo README to PyPI was to have `pyproject.toml` sit
  alongside it.
- The Python package source still lives in `python/adbc_driver_quack/`
  — that's wired up via `[tool.setuptools] packages = ["adbc_driver_quack"]`
  and `package-dir = { "" = "python" }`.
- Tests still live in `python/tests/`; `[tool.pytest.ini_options]
  testpaths = ["python/tests"]` keeps them discoverable.
- `python.yml` simplified: no more `cd python` in the install / test /
  wheel-build steps. `pip install -e ".[test]"` and `pytest` now run
  from the repo root.
- The PyPI project page now renders the full README — badges,
  3-step quickstart, alt-pattern snippet, streaming/ingest/tx
  examples, and connection-URL table — instead of a 40-line stub.
- One small README edit: the `[MIT](LICENSE)` link is now an
  absolute GitHub URL so PyPI's renderer can resolve it.

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
