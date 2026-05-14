"""
pytest fixtures for the adbc-driver-quack integration tests.

Spins up an in-process DuckDB via the ``duckdb`` Python package,
installs the ``quack`` extension from ``core_nightly``, and calls
``quack_serve`` on a randomly-chosen free port. The Quack listener
runs in a background thread inside DuckDB; the test client connects
to ``quack://127.0.0.1:<port>`` exactly as it would against a
real deployed server.

Tests are auto-skipped when the ``duckdb`` Python package is not
installed (so ``pytest -m 'not integration'`` still runs cleanly).
"""

from __future__ import annotations

import os
import socket
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


@pytest.fixture(scope="session")
def quack_server() -> QuackServer:
    try:
        import duckdb
    except ImportError:
        pytest.skip(reason="duckdb python package not installed")

    port = _pick_free_port()
    token = "adbc-driver-quack-it-token"

    # In-process DuckDB with unsigned-extensions enabled so we can load
    # the quack extension from core_nightly. The connection stays alive
    # for the whole pytest session — closing it stops the Quack listener.
    con = duckdb.connect(
        database=":memory:",
        config={"allow_unsigned_extensions": "true"},
    )
    try:
        con.execute(query="INSTALL quack FROM core_nightly")
        con.execute(query="LOAD quack")
        con.execute(
            query=f"CALL quack_serve('quack:127.0.0.1:{port}', token=>'{token}')"
        )
    except Exception as exc:
        con.close()
        pytest.skip(reason=f"failed to start in-process Quack server: {exc}")

    server = QuackServer(host="127.0.0.1", port=port, token=token)
    yield server

    # Tear down: stop the listener, then drop the connection.
    try:
        con.execute(query=f"CALL quack_stop('quack:127.0.0.1:{port}')")
    except Exception:
        pass
    con.close()


@pytest.fixture(scope="session")
def driver_path() -> str:
    """Resolve the bundled Quack ADBC driver library path."""
    candidate = os.environ.get("ADBC_QUACK_LIBRARY")
    if candidate and Path(candidate).is_file():
        return candidate
    from adbc_driver_quack import _driver_path

    return _driver_path()
