# adbc-driver-quack - by [GizmoData™](https://gizmodata.com)

**An [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for [DuckDB's Quack remote protocol](https://duckdb.org/docs/current/quack/overview): Arrow straight from a remote DuckDB 2.0, in Python, Go, C, Rust, R and DuckDB itself.**

> An independent, open-source project. Not affiliated with, endorsed by, or sponsored by the DuckDB Foundation or DuckDB Labs. "DuckDB" is a trademark of the DuckDB Foundation, used here only to describe compatibility.

[<img src="https://img.shields.io/badge/GitHub-gizmodata%2Fadbc--driver--quack-blue.svg?logo=Github">](https://github.com/gizmodata/adbc-driver-quack)
[![CI](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/python.yml/badge.svg)](https://github.com/gizmodata/adbc-driver-quack/actions/workflows/python.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/gizmodata/adbc-driver-quack.svg)](https://pkg.go.dev/github.com/gizmodata/adbc-driver-quack)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gizmodata/adbc-driver-quack)](https://github.com/gizmodata/adbc-driver-quack/blob/main/go.mod)
[![Supported Python Versions](https://img.shields.io/pypi/pyversions/adbc-driver-quack)](https://pypi.org/project/adbc-driver-quack/)
[![PyPI version](https://badge.fury.io/py/adbc-driver-quack.svg)](https://badge.fury.io/py/adbc-driver-quack)
[![PyPI Downloads](https://img.shields.io/pepy/dt/adbc-driver-quack.svg)](https://pypi.org/project/adbc-driver-quack/)
[![DuckDB 2.0](https://img.shields.io/badge/DuckDB-2.0-fff000.svg?logo=duckdb&logoColor=black)](https://duckdb.org)
[![ADBC conformance](https://img.shields.io/badge/ADBC-conformance%20suites%20passing-2ea44f.svg)](#conformance)
[![License](https://img.shields.io/github/license/gizmodata/adbc-driver-quack)](https://github.com/gizmodata/adbc-driver-quack/blob/main/LICENSE)
[![Hardcharger](https://img.shields.io/badge/works%20with-Hardcharger%E2%84%A2-f15a24.svg)](https://gizmodata.com/hardcharger)

A pure-Go client for Quack, the remote protocol DuckDB 2.0 serves with `quack_serve`. It
returns Apache Arrow record batches straight from the server's results, streams large
results in batches, and bulk-loads Arrow data into DuckDB tables. No DuckDB library on the
client: just `pip install`.

Distributed as:
- a Go module: `github.com/gizmodata/adbc-driver-quack`
- a `pip install adbc-driver-quack` wheel for Python 3.11 to 3.15 (macOS / Linux / Windows × x64 / arm64)
- a c-shared library (`libadbc_driver_quack.{so,dylib,dll}`) attached to each GitHub
  Release, for C / C++ / Rust / R / driver-manifest consumers and DuckDB's `adbc_scanner`

> **Status: stable (1.0).** Speaks Quack protocol version 3, the one DuckDB 2.0 ships. It
> passes [apache/arrow-adbc's Go validation suite and the adbc-drivers.org conformance
> suite](#conformance). DuckDB 1.5's preview Quack (protocol version 1) is not compatible;
> for those servers, pin `adbc-driver-quack==0.1.0a11`.

## Quickstart

### 1. Start a Quack server (DuckDB 2.0)

```sql
-- in any DuckDB 2.0 session (quack is a core extension)
INSTALL quack;
LOAD quack;
CALL quack_serve('quack:localhost:9494', token => 'my-secret-token');
```

The server runs until the DuckDB session exits. On `localhost` it serves plain HTTP; on any
other address it serves HTTPS ([below](#tls-and-production-quack)).

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
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer, 'hello duckdb' AS greeting")
    table: pyarrow.Table = cur.fetch_arrow_table()
    print(table)
```

The result is a real `pyarrow.Table`: hand it to Polars, pandas, DuckDB, ibis or anything
else that reads Arrow.

From Go, with the ADBC API:

```go
package main

import (
	"context"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/gizmodata/adbc-driver-quack/driver/quack"
)

func main() {
	ctx := context.Background()
	db, err := quack.NewDriver(memory.DefaultAllocator).NewDatabase(map[string]string{
		"uri":              "quack://localhost:9494",
		"adbc.quack.token": "my-secret-token",
	})
	if err != nil {
		panic(err)
	}
	defer db.Close()
	conn, err := db.Open(ctx)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	stmt, _ := conn.NewStatement()
	defer stmt.Close()
	_ = stmt.SetSqlQuery("SELECT 42 AS answer")
	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		panic(err)
	}
	defer reader.Release()
	for reader.Next() {
		fmt.Println(reader.RecordBatch())
	}
}
```

You can also skip the wrapper and point `adbc_driver_manager.dbapi.connect` at the bundled
library (`driver=adbc_driver_quack._driver_path(), entrypoint="QuackDriverInit"`), or, after
`python -m adbc_driver_quack install-manifest`, at the driver by name
([below](#connection-profiles--driver-manifests)).

### Streaming large results

`Cursor.fetch_record_batch()` returns a `pyarrow.RecordBatchReader` that pulls one server
batch at a time, so memory stays bounded by the batch size, not the result size:

```python
with conn.cursor() as cur:
    cur.execute("SELECT * FROM lineitem")  # any size
    for batch in cur.fetch_record_batch():
        process(batch)
```

Each batch carries an index and the server's final answer carries the batch total, so a
result that loses a batch fails loudly instead of coming back short.

### Bulk ingest (Arrow → DuckDB)

```python
import pyarrow as pa
import adbc_driver_quack.dbapi as quack

table = pa.table({"id": [1, 2, 3], "name": ["alice", "bob", "carol"]})
with quack.connect(
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
    autocommit=True,  # ADBC connections start in a transaction; opt in so the ingest persists
) as conn, conn.cursor() as cur:
    rows = cur.adbc_ingest(table_name="customers", data=table, mode="create_append")
    print(f"loaded {rows} rows")
```

The driver streams the batches into
`INSERT INTO … SELECT * FROM scan_data_from_quack_client(…)`, the way DuckDB's own client
writes to an attached Quack database: several 2,048-row chunks per request, numbered so the
server can check that none is missing, with the row count coming back from the server.
Without `autocommit=True`, call `conn.commit()` after `adbc_ingest()`, or the load is rolled
back when the connection closes.

`mode` takes the four ADBC ingest modes:

| mode            | behavior                                                                    |
|-----------------|-----------------------------------------------------------------------------|
| `create`        | create the table (`ALREADY_EXISTS` if it exists), then load; the default    |
| `append`        | load into an existing table (`NOT_FOUND` if missing, `ALREADY_EXISTS` if the columns don't match) |
| `replace`       | `CREATE OR REPLACE` the table, then load                                    |
| `create_append` | create the table if it doesn't exist, then load                             |

Column types follow DuckDB's own Arrow import, so a table loaded through Quack looks the
same as one loaded with DuckDB's ADBC driver. `db_schema_name=…`, `catalog_name=…` and
`temporary=True` work as in ADBC.

### Transactions

```python
with quack.connect(
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
    autocommit=False,
) as conn, conn.cursor() as cur:
    cur.execute("INSERT INTO orders VALUES (1, 'pending')")
    cur.execute("INSERT INTO order_items VALUES (1, 'widget', 2)")
    conn.commit()  # both inserts persist atomically
```

`cur.rowcount` reports the rows an `INSERT`, `UPDATE`, `DELETE` or `MERGE` changed.

## TLS and production Quack

DuckDB 2.0 serves Quack over **HTTPS on any address but localhost**, and the driver follows
the same rule: plain HTTP to `localhost` / `127.0.0.1` / `::1`, HTTPS everywhere else.
`adbc.quack.tls` overrides it either way.

`quack_serve` generates a self-signed certificate on first use. A client authenticates such
a server by **pinning its certificate's SHA-256 fingerprint**, as DuckDB's own client does:

```sql
-- on the server
SELECT fingerprint FROM quack_generate_keys();   -- or the info of quack_server_list()
CALL quack_serve('quack:0.0.0.0:9494', token => 'my-secret-token', allow_other_hostname => true);
```

```python
quack.connect(
    uri="quack://db.example.com:9494",
    db_kwargs={
        "adbc.quack.token": "my-secret-token",
        "adbc.quack.ssl_fingerprint": "2B:31:F0:…:EA",  # the colon form openssl prints works too
    },
)
```

With a certificate from a CA (`quack_serve(…, ssl_cert_file => …, ssl_key_file => …)`), no
pin is needed: the driver verifies the chain and host name against the system roots.

The driver also keeps the connection's **heartbeat lease** alive (DuckDB 2.0 drops a client
that has been silent for the lease, 60 s by default), and **cancels** the statement on the
server when a call is cancelled or times out, rather than just walking away from it.

## Types

| DuckDB | Arrow |
|---|---|
| BOOLEAN, TINYINT … BIGINT, UTINYINT … UBIGINT, FLOAT, DOUBLE | the matching Arrow type |
| DECIMAL(p, s), HUGEINT, UHUGEINT | `decimal128` |
| VARCHAR, ENUM | `string` |
| BLOB, BIT | `binary` |
| DATE | `date32` |
| TIME, TIME_NS, TIMETZ | `time64[us]`, `time64[ns]`, `time64[us]` (local time) |
| TIMESTAMP_S / _MS / TIMESTAMP / _NS | `timestamp[s/ms/us/ns]` (no time zone) |
| TIMESTAMPTZ, TIMESTAMPTZ_NS | `timestamp[us/ns, tz=UTC]` |
| INTERVAL | `month_day_nano_interval` |
| UUID | `arrow.uuid` (canonical extension) |
| **VARIANT** | **`arrow.parquet.variant`** (canonical extension: Parquet Variant `metadata` + `value`) |
| **GEOMETRY** | **`geoarrow.wkb`** (WKB, with the CRS in the extension metadata) |
| LIST, ARRAY, STRUCT, MAP | `list`, `fixed_size_list`, `struct`, `map` |

VARIANT and GEOMETRY come back exactly as DuckDB's own Arrow export, and so
[Hardcharger](https://gizmodata.com/hardcharger), returns them. The same Arrow types go the
other way in an ingest: an `arrow.parquet.variant` column lands as VARIANT, `geoarrow.wkb` as
GEOMETRY, `arrow.uuid` as UUID. A VARIANT member a Parquet Variant cannot hold (an INTERVAL,
an integer over 38 digits) is an error naming the type, never a wrong value.

## Share data with Hardcharger

[**Hardcharger™**](https://gizmodata.com/hardcharger) is GizmoData's DuckDB 2.0 server for
production. Moving data between a Quack server and Hardcharger takes no files and no
intermediate copies, in either direction.

### Quack → Hardcharger, ADBC to ADBC

A Quack result streams straight into Hardcharger's bulk ingest, batch by batch, without
ever being materialized on the client, VARIANT and GEOMETRY columns included:

```sh
pip install adbc-driver-quack adbc-driver-hardcharger
```

```python
import adbc_driver_quack.dbapi as quack
from adbc_driver_hardcharger import dbapi as hardcharger

with quack.connect(uri="quack://quack.example.com:9494",
                   db_kwargs={"adbc.quack.token": quack_token}) as src, \
     hardcharger.connect(uri="hardcharger://hardcharger.example.com",
                         username="app", password=password) as dst:
    with src.cursor() as s, dst.cursor() as d:
        s.execute("SELECT * FROM orders")
        rows = d.adbc_ingest(table_name="orders", data=s.fetch_record_batch(), mode="replace")
        dst.commit()
        print(f"Loaded {rows:,} rows")
```

`python/tests/test_quack_to_hardcharger.py` runs exactly this, 250,000 rows with a VARIANT
and a GEOMETRY column, against a live Hardcharger in CI. To try it without a server of your
own, `pip install "hardcharger[adbc]"` runs a local Hardcharger from Python:

```python
import hardcharger

with hardcharger.Server() as server, server.connect() as dst:
    ...  # dst is a Hardcharger connection, as above
```

### Hardcharger (or DuckDB) ↔ Quack, live, with `adbc_scanner`

The c-shared driver plugs into Query.Farm's
[`adbc_scanner`](https://query.farm/products/extensions/adbc_scanner/) extension, so
Hardcharger and DuckDB can query a Quack server *live* and write to it, with projection and
filter pushdown:

```sql
SET allow_extension_repositories = 'allowed';
CREATE EXTENSION REPOSITORY IF NOT EXISTS query_farm WITH PREFIX 'https://duckdb-extensions.query.farm';
INSTALL adbc_scanner FROM query_farm;
LOAD adbc_scanner FROM query_farm;

CREATE SECRET quack_secret (
    TYPE adbc,
    SCOPE 'quack://quack.example.com:9494',
    driver 'quack',                     -- by name after `python -m adbc_driver_quack install-manifest`,
                                        -- or a path: '/path/to/libadbc_driver_quack.so'
    uri 'quack://quack.example.com:9494',
    extra_options MAP {'adbc.quack.token': '********'}
);
ATTACH 'quack://quack.example.com:9494' AS q (TYPE adbc);

-- pull, with filter pushdown
SELECT * FROM q.main.orders WHERE order_date >= DATE '2026-01-01';
SELECT * FROM adbc_scan('q', 'SELECT customer, sum(amount) FROM orders GROUP BY ALL');

-- push: CTAS through the attached catalog, or adbc_insert
USE q.main;
CREATE TABLE orders_copy AS SELECT * FROM memory.main.local_orders;
USE memory;
SELECT * FROM adbc_insert('q', 'orders_copy2', (SELECT * FROM local_orders), mode := 'create');
```

In Hardcharger the server loads the driver, so `driver` names a library (or an installed
manifest) on the server's machine. `python/tests/test_duckdb_extensions.py` runs every
statement above inside a Hardcharger server and in an embedded DuckDB.

## Why Hardcharger and Chargewire for production

Quack is DuckDB's own remote protocol, and a great fit for DuckDB-to-DuckDB work, sharing a
database with a teammate, and development. For a database many people and services depend
on, [Hardcharger](https://gizmodata.com/hardcharger) speaks
[**Chargewire**](https://github.com/gizmodata/chargewire), an open HTTP protocol built for
it:

| | Quack (DuckDB 2.0) | Hardcharger + Chargewire |
|---|---|---|
| **Access control** | a token checked by an authentication function (one shared token by default), plus an optional SQL authorization callback | users and roles with full **RBAC**, checked inside DuckDB against each statement's own plan (a write hidden in a CTE is still a write), **row policies**, **column masking** and **tenants** |
| **Sign-in** | the token | passwords, scoped tokens, **SSO (OIDC / JWKS)**, **mTLS** client certificates |
| **Wire format** | DuckDB's internal vector serialization, tied to DuckDB versions (1.5 → 2.0 changed it) | **Apache Arrow IPC**, an open standard any Arrow library reads, optionally LZ4/zstd compressed |
| **Large results** | batched fetch | streamed, **resumable** results, described as partitions for scale-out |
| **Bulk ingest** | client-built DataChunks | Arrow streamed straight into a table, over several connections, committed as one |
| **Audit and operations** | request log | **tamper-evident audit log**, instrumentation (DuckDB or PostgreSQL), Prometheus `/metrics`, query profiles, `KILL SESSION` |
| **Clients** | DuckDB, this driver, quack-jdbc | ADBC (Python, Go, C), JDBC, ODBC, JavaScript, MCP, a built-in web UI, desktop and iOS apps |

Hardcharger is free, with every feature in every build: see
[gizmodata.com/hardcharger](https://gizmodata.com/hardcharger) and the
[documentation](https://docs.hardcharger.gizmodata.com). The two work together: keep Quack
where it fits, and move data to Hardcharger with the [examples above](#share-data-with-hardcharger).

## Connection options

```
quack://host[:port]      also accepted: quack:host[:port] (DuckDB's spelling)
```

| Option | Default | Notes |
|---|---|---|
| `uri` | required | `quack://host:port`; the default port is 9494. |
| `adbc.quack.token` | (none) | The token `quack_serve` was started with. |
| `adbc.quack.token_env` | (none) | Environment variable to read the token from. Option only, refused on the URL. |
| `adbc.quack.token_file` | (none) | File to read the token from. Option only, refused on the URL. |
| `adbc.quack.tls` | auto | `true`/`false`. Unset: HTTP for localhost / 127.0.0.1 / ::1, HTTPS elsewhere. |
| `adbc.quack.ssl_fingerprint` | (none) | SHA-256 fingerprint of the one server certificate to trust (self-signed `quack_serve`). Implies TLS. |
| `adbc.quack.heartbeat_timeout_seconds` | `60` | Connection lease to request; the driver renews it while idle. |
| `adbc.quack.client_id` | (none) | Client identifier shown (hashed) in the server's `quack_active_connections()`. |
| `adbc.quack.rpc.timeout_seconds.connect` | `10` | HTTP connect timeout: seconds, or a Go duration like `1.5s`. |
| `adbc.quack.rpc.timeout_seconds.request` | `60` | Per-request timeout: seconds, or a Go duration like `90s`. |
| `adbc.quack.http.header.<Name>` | (none) | Extra HTTP header on every request (proxy / load-balancer auth). Repeatable; empty clears. Option only. |

An explicit `adbc.quack.token` wins over `token_env`, which wins over `token_file`. The
indirections and extra headers are accepted only as ADBC options, never as URL query
parameters: a pasted URL must not be able to read a local secret, or add headers, and send
them to the host it names. An unknown option is an error, so a misspelled one never goes
unnoticed.

The driver supports ADBC 1.1's `GetOption` / `SetOption`, the current catalog and schema
(`adbc.connection.catalog`, `adbc.connection.db_schema`), `GetInfo`, `GetObjects` (with
primary, unique and foreign keys), `GetTableSchema`, `GetTableTypes`, `ExecuteSchema`,
parameter binding (rendered as typed literals, since Quack has no wire-level parameters) and
cancellation. Server errors carry DuckDB's exception type as an ADBC status and SQLSTATE
(a missing table is `NOT_FOUND` / `42S02`, a syntax error `INVALID_ARGUMENT` / `42601`).

## Connection profiles & driver manifests

[ADBC connection profiles](https://arrow.apache.org/adbc/current/format/connection_profiles.html)
keep a connection's driver and options in a TOML file. They resolve the driver by name,
through a [driver manifest](https://arrow.apache.org/adbc/current/format/driver_manifests.html).
Install ours once per environment:

```console
$ python -m adbc_driver_quack install-manifest
Wrote ADBC driver manifest: .../etc/adbc/drivers/quack.toml
```

Then the driver manager finds the driver by URI scheme, with no import of
`adbc_driver_quack`:

```python
from adbc_driver_manager import dbapi

conn = dbapi.connect(uri="quack://localhost:9494", db_kwargs={"adbc.quack.token": "my-secret-token"})
```

and a profile (`~/.config/adbc/profiles/quack_prod.toml` on Linux,
`~/Library/Application Support/ADBC/Profiles/` on macOS, or any directory in
`ADBC_PROFILE_PATH`) bundles the whole connection:

```toml
profile_version = 1
driver = "quack"

[Options]
uri = "quack://prod.example.com:9494"
"adbc.quack.ssl_fingerprint" = "2B:31:F0:…:EA"
"adbc.quack.token" = "{{ env_var(QUACK_TOKEN) }}"
```

```python
conn = dbapi.connect(profile="quack_prod")
```

## Conformance

Every CI run, and so every release, passes:

- **[apache/arrow-adbc](https://github.com/apache/arrow-adbc)'s Go validation suite**
  (`driver/quack/validation_test.go`): all 44 tests and subtests pass. One subtest is turned off because it
  expects `INTERNAL` where the ADBC specification asks for `ALREADY_EXISTS`; the driver
  returns `ALREADY_EXISTS`, and its own test covers those cases.
- **The [adbc-drivers.org conformance suite](https://github.com/adbc-drivers/validation)**
  (`validation/`): 333 passed. Not tested: `GetStatistics` and the optional XDBC column
  metadata, which the driver does not offer, `GetParameterSchema` (Quack has no server-side
  parameters), and negative decimal scales (DuckDB has none). DuckDB-specific expectations
  (TIME is microsecond precision, `BLOB` takes no length, TIMESTAMP_NS reserves its INT64
  extremes) live in `validation/queries/`, each with the reason.

Run them yourself:

```bash
python scripts/quack_test_server.py --port 9494 --validation &
ADBC_QUACK_TEST_URI=quack://127.0.0.1:9494 go test ./driver/quack -run TestValidation
cd validation && ADBC_QUACK_VALIDATION_URI=quack://127.0.0.1:9494 \
    ADBC_QUACK_VALIDATION_TOKEN=adbc-driver-quack-it-token pytest
```

## ADBC or JDBC?

| You're using | Reach for |
|---|---|
| A JVM tool (DBeaver, IntelliJ, Spark, plain `java.sql`) | [`quack-jdbc`](https://github.com/gizmodata/quack-jdbc) (check its README for the DuckDB versions it supports) |
| Python (`pip install`), Go, Rust, R, anything on the ADBC C ABI, DuckDB's `adbc_scanner` | this driver |
| Zero-copy Arrow end to end | this driver |

## Repo layout

```
adbc-driver-quack/
├── internal/
│   ├── codec/       — DuckDB BinarySerializer reader/writer
│   ├── quacktype/   — DuckDB logical / physical types and their codec
│   ├── message/     — Quack protocol 3 messages, DataChunk and vector codecs
│   └── transport/   — quack:// URIs, HTTP(S) transport, TLS pinning
├── driver/quack/    — the ADBC driver: sessions, Arrow conversion, ingest, metadata
├── pkg/quack/       — cgo c-shared wrapper (libadbc_driver_quack.{so,dylib,dll})
├── python/          — the Python package (adbc_driver_quack) and its tests
├── validation/      — the adbc-drivers.org conformance suite setup
└── scripts/         — test server and conformance-override generator
```

## Credits

- Wire format: [DuckDB's Quack protocol](https://duckdb.org/docs/current/quack/overview)
  ([duckdb/duckdb-quack](https://github.com/duckdb/duckdb-quack), MIT)
- Codec design: ported from [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc)
  (MIT), itself clean-room ported from
  [`tobilg/quack-protocol`](https://github.com/tobilg/quack-protocol) (MIT)
- ADBC framework and the c-shared wrapper template: [Apache Arrow ADBC](https://github.com/apache/arrow-adbc) (Apache-2.0)
- Conformance suite: [adbc-drivers/validation](https://github.com/adbc-drivers/validation) (Apache-2.0), a test dependency

See [`NOTICE`](https://github.com/gizmodata/adbc-driver-quack/blob/main/NOTICE) for full attribution.

## Trademarks

DuckDB is a trademark of the DuckDB Foundation. **adbc-driver-quack is not affiliated with,
endorsed by, or sponsored by the DuckDB Foundation or DuckDB Labs.** References to DuckDB
and Quack identify the software this driver interoperates with.

## License

[MIT](https://github.com/gizmodata/adbc-driver-quack/blob/main/LICENSE): Copyright (c) 2026
GizmoData LLC. See [`NOTICE`](https://github.com/gizmodata/adbc-driver-quack/blob/main/NOTICE)
for third-party attributions.

---

<p align="center">
  <a href="https://gizmodata.com"><img src="https://raw.githubusercontent.com/gizmodata/adbc-driver-quack/main/docs/assets/gizmodata-logo.png" alt="GizmoData™" width="56"></a><br>
  <sub>Built by <a href="https://gizmodata.com">GizmoData™</a>, makers of <a href="https://gizmodata.com/hardcharger">Hardcharger™</a>. Free and open source.</sub>
</p>

<p align="center"><sub>Hardcharger™ and GizmoData™ are trademarks of GizmoData LLC.</sub></p>
