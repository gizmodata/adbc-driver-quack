# adbc-driver-quack

**Python package for [adbc-driver-quack](https://github.com/gizmodata/adbc-driver-quack), an
[Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for
[DuckDB's Quack remote protocol](https://duckdb.org/docs/current/quack/overview), by
[GizmoData™](https://gizmodata.com): Arrow straight from a remote DuckDB 2.0.**

A pure-Go client for Quack, the remote protocol DuckDB 2.0 serves with `quack_serve`. It returns
Apache Arrow record batches from the server's results, streams large results in batches and
bulk-loads Arrow data into DuckDB tables. No DuckDB library on the client: the driver is a shared
library inside the wheel.

> An independent, open-source project. Not affiliated with, endorsed by, or sponsored by the
> DuckDB Foundation or DuckDB Labs. "DuckDB" is a trademark of the DuckDB Foundation, used here
> only to describe compatibility.

This page covers the Python package. For other languages (C, C++, Go, Rust, R, DuckDB's
`adbc_scanner`), the one-line installer of the shared library
(`curl https://install.adbc.gizmodata.com/quack | sh`), TLS, every connection option, type mapping
and conformance, see the [project README on GitHub](https://github.com/gizmodata/adbc-driver-quack#readme).

## Install

```bash
pip install adbc-driver-quack
```

- Python 3.11 or later.
- Wheels for Linux x86_64 and aarch64 (glibc 2.17 or later), macOS arm64 (13 or later) and
  Windows x64.
- Depends on `adbc-driver-manager` and `pyarrow`.
- A DuckDB 2.0 server (Quack protocol version 3). DuckDB 1.5's preview Quack is not compatible.

Start a server in any DuckDB 2.0 session (`quack` is a core extension):

```sql
INSTALL quack;
LOAD quack;
CALL quack_serve('quack:localhost:9494', token => 'my-secret-token');
```

## Connect and query

```python
from adbc_driver_quack import dbapi as quack

with quack.connect(
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer, 'hello duckdb' AS greeting")
    table = cur.fetch_arrow_table()  # a pyarrow.Table
    print(table)
```

The result is a `pyarrow.Table`: pass it to Polars, pandas, DuckDB or anything else that reads
Arrow.

## Stream large results

`fetch_record_batch()` returns a `pyarrow.RecordBatchReader` that pulls one server batch at a
time, so memory stays bounded by the batch size, not the result size:

```python
from adbc_driver_quack import dbapi as quack

with quack.connect(
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT range AS n FROM range(1000000)")
    rows = 0
    for batch in cur.fetch_record_batch():
        rows += batch.num_rows
    print(rows)
```

## Bulk ingest (Arrow to DuckDB)

```python
import pyarrow as pa
from adbc_driver_quack import dbapi as quack

data = pa.table({"id": [1, 2, 3], "name": ["a", "b", "c"]})

with quack.connect(
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
    autocommit=True,
) as conn, conn.cursor() as cur:
    cur.adbc_ingest(table_name="pypi_example_people", data=data, mode="replace")
    cur.execute("SELECT count(*) AS n FROM pypi_example_people")
    print(cur.fetchone())
```

## Load the driver by name (driver manifest)

ADBC [driver manifests](https://arrow.apache.org/adbc/current/format/driver_manifests.html) let
the ADBC driver manager, and [connection profiles](https://arrow.apache.org/adbc/current/format/connection_profiles.html),
find a driver by name. The package writes one for its own library:

```console
$ python -m adbc_driver_quack install-manifest
Wrote ADBC driver manifest: .../etc/adbc/drivers/quack.toml
```

Then the driver manager resolves `quack://` URLs with no import of `adbc_driver_quack`:

```python
from adbc_driver_manager import dbapi

with dbapi.connect(
    uri="quack://localhost:9494",
    db_kwargs={"adbc.quack.token": "my-secret-token"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 1 AS one")
    print(cur.fetch_arrow_table())
```

## More

- [Project README](https://github.com/gizmodata/adbc-driver-quack#readme): every option and
  feature, other languages, and the one-line installer for the shared library
- [Changelog](https://github.com/gizmodata/adbc-driver-quack/blob/main/CHANGELOG.md)
- [Issues](https://github.com/gizmodata/adbc-driver-quack/issues)

Licensed under the [MIT License](https://github.com/gizmodata/adbc-driver-quack/blob/main/LICENSE).
