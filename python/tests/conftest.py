"""
pytest fixtures for the adbc-driver-quack integration tests.

Spins up an in-process DuckDB 2.0 via the ``duckdb`` Python package,
loads the core ``quack`` extension, and calls ``quack_serve`` on a
randomly chosen free port. The Quack listener runs in a background thread
inside DuckDB; the test client connects to ``quack://127.0.0.1:<port>``
exactly as it would to a deployed server. ``quack_tls_server`` serves
HTTPS with a self-signed certificate, for the certificate-pinning tests.

These are gating tests: when the server cannot start, they FAIL, never
skip. Deselect them explicitly (``-m "not integration"``) to run only the
unit tests.

The Hardcharger tests (``-m hardcharger``) need a Hardcharger server
(https://gizmodata.com/hardcharger): the one named by
``ADBC_QUACK_TEST_HARDCHARGER_URI``, or else one started from the
``hardcharger`` PyPI package (``pip install -e ".[test]"`` installs it).
They fail when neither is available; deselect them with
``-m "not hardcharger"``.
"""

from __future__ import annotations

import os
import socket
import time
from dataclasses import dataclass
from pathlib import Path

import pytest


@dataclass
class QuackServer:
    """Connection details for an in-process Quack server."""

    host: str
    port: int
    token: str

    @property
    def uri(self) -> str:
        return f"quack://{self.host}:{self.port}"

    @property
    def db_kwargs(self) -> dict[str, str]:
        return {"adbc.quack.token": self.token}


def _pick_free_port() -> int:
    # socket.bind / getsockname are C-extension methods that only accept
    # positional arguments — kwargs aren't supported even in 3.12+.
    with socket.socket(family=socket.AF_INET, type=socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _start_quack(extra: str = ""):
    import duckdb  # a test dependency: missing it is an error, not a skip

    if int(duckdb.__version__.split(".")[0]) < 2:
        pytest.fail(reason=f"these tests need DuckDB 2.0 or later (Quack protocol 3); found {duckdb.__version__}")
    port = _pick_free_port()
    token = "adbc-driver-quack-it-token"
    # The connection stays alive for the whole pytest session: closing it
    # stops the Quack listener.
    con = duckdb.connect(database=":memory:")
    try:
        con.execute(query="INSTALL quack")
        con.execute(query="LOAD quack")
        con.execute(query=f"CALL quack_serve('quack:127.0.0.1:{port}', token => '{token}'{extra})")
    except Exception as exc:
        con.close()
        pytest.fail(reason=f"failed to start the in-process Quack server: {exc}")
    return con, port, token


def _stop_quack(con, port: int) -> None:
    try:
        con.execute(query=f"CALL quack_stop('quack:127.0.0.1:{port}')")
    except Exception:
        pass
    con.close()


@pytest.fixture(scope="session")
def quack_server() -> QuackServer:
    con, port, token = _start_quack()
    yield QuackServer(host="127.0.0.1", port=port, token=token)
    _stop_quack(con=con, port=port)


@dataclass
class QuackTLSServer(QuackServer):
    """A Quack server on HTTPS with a self-signed certificate."""

    fingerprint: str = ""


@pytest.fixture(scope="session")
def quack_tls_server(tmp_path_factory) -> QuackTLSServer:
    import duckdb

    keys = tmp_path_factory.mktemp("quack-keys")
    with duckdb.connect(database=":memory:") as gen:
        gen.execute(query="INSTALL quack")
        gen.execute(query="LOAD quack")
        cert, key, fingerprint = gen.execute(
            query="SELECT cert_file, key_file, fingerprint FROM quack_generate_keys(directory := ?)",
            parameters=[str(keys)],
        ).fetchone()
    con, port, token = _start_quack(
        extra=f", disable_ssl => false, ssl_cert_file => '{cert}', ssl_key_file => '{key}'"
    )
    yield QuackTLSServer(host="127.0.0.1", port=port, token=token, fingerprint=fingerprint)
    _stop_quack(con=con, port=port)


HARDCHARGER_MIN_VERSION = (2, 1)


@dataclass
class HardchargerServer:
    """Connection details for a Hardcharger server (test-only dependency)."""

    uri: str
    username: str
    password: str
    driver_path: str | None = None  # where the server sees this driver, if not ours

    def connect(self, autocommit: bool = True):
        from adbc_driver_hardcharger import dbapi as hardcharger

        return hardcharger.connect(
            uri=self.uri, username=self.username, password=self.password, autocommit=autocommit
        )


def _wait_for_hardcharger(server: HardchargerServer, timeout: float = 120.0) -> None:
    deadline = time.monotonic() + timeout
    last: Exception | None = None
    while time.monotonic() < deadline:
        try:
            with server.connect() as conn, conn.cursor() as cur:
                cur.execute(operation="SELECT HARDCHARGER_VERSION()")
                (version,) = cur.fetchone()
        except Exception as exc:  # not up yet
            last = exc
            time.sleep(1)
            continue
        major_minor = tuple(int(p) for p in version.lstrip("v").split(".")[:2])
        if major_minor < HARDCHARGER_MIN_VERSION:
            pytest.fail(reason=f"Hardcharger at {server.uri} is {version}; these tests need 2.1 or later")
        return
    pytest.fail(reason=f"no Hardcharger answering at {server.uri} after {timeout:.0f}s: {last}")


@pytest.fixture(scope="session")
def hardcharger_server():
    uri = os.environ.get("ADBC_QUACK_TEST_HARDCHARGER_URI")
    if uri:
        server = HardchargerServer(
            uri=uri,
            username=os.environ.get("ADBC_QUACK_TEST_HARDCHARGER_USERNAME", "hardcharger"),
            password=os.environ.get("ADBC_QUACK_TEST_HARDCHARGER_PASSWORD", "hardcharger"),
            driver_path=os.environ.get("ADBC_QUACK_TEST_HARDCHARGER_DRIVER_PATH"),
        )
        _wait_for_hardcharger(server=server)
        yield server
        return
    try:
        import adbc_driver_hardcharger  # noqa: F401
        import hardcharger
    except ImportError as exc:
        pytest.fail(
            reason=f"no ADBC_QUACK_TEST_HARDCHARGER_URI and the hardcharger packages aren't installed ({exc}); "
            'pip install -e ".[test]" or deselect with -m "not hardcharger"'
        )
    with hardcharger.Server() as local:
        server = HardchargerServer(uri=local.uri, username=local.username, password=local.password)
        _wait_for_hardcharger(server=server)
        yield server


@pytest.fixture(scope="session")
def driver_path() -> str:
    """Resolve the bundled Quack ADBC driver library path."""
    candidate = os.environ.get("ADBC_QUACK_LIBRARY")
    if candidate and Path(candidate).is_file():
        return candidate
    from adbc_driver_quack import _driver_path

    return _driver_path()
