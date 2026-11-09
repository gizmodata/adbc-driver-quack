"""Serve an in-memory DuckDB over Quack for the Go test suites.

    python scripts/quack_test_server.py --port 9494 --token adbc-driver-quack-it-token

Prints ``READY quack://127.0.0.1:<port>`` once the listener is up and serves
until it is killed (CI starts it in the background; stop it by its PID).
Needs the ``duckdb`` package, 2.0 or later.
"""

from __future__ import annotations

import argparse
import signal
import sys
import threading

import duckdb


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[1])
    parser.add_argument("--port", type=int, default=9494)
    parser.add_argument("--token", default="adbc-driver-quack-it-token")
    parser.add_argument(
        "--validation",
        action="store_true",
        help="also create the secondary schema and catalog the conformance suite (validation/) uses",
    )
    args = parser.parse_args()

    if int(duckdb.__version__.split(".")[0]) < 2:
        print(f"DuckDB {duckdb.__version__} is too old: Quack protocol 3 needs DuckDB 2.0", file=sys.stderr)
        return 1
    con = duckdb.connect(database=":memory:")
    con.execute(query="INSTALL quack")
    con.execute(query="LOAD quack")
    # Zone-less TIMESTAMPTZ literals read in the session time zone: pin it so
    # results don't depend on the machine running the tests.
    con.execute(query="SET GLOBAL TimeZone = 'UTC'")
    if args.validation:
        con.execute(query="CREATE SCHEMA IF NOT EXISTS quack_validation_secondary")
        con.execute(query="ATTACH ':memory:' AS quack_validation_catalog")
    con.execute(query=f"CALL quack_serve('quack:127.0.0.1:{args.port}', token => ?)", parameters=[args.token])
    print(f"READY quack://127.0.0.1:{args.port}", flush=True)

    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    stop.wait()
    con.execute(query=f"CALL quack_stop('quack:127.0.0.1:{args.port}')")
    con.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
