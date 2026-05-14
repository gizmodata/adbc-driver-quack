"""
pytest fixtures for the adbc-driver-quack integration tests.

Spawns ``duckdb -unsigned`` as a subprocess, installs the Quack extension
from ``core_nightly``, calls ``quack_serve`` on a randomly-chosen free
port, and exposes the URL + token via the ``quack_server`` fixture.

Tests are auto-skipped when the ``duckdb`` binary is not on PATH (so
``pytest -m 'not integration'`` still runs cleanly on CI legs that
don't install DuckDB).
"""

from __future__ import annotations

import os
import shutil
import socket
import subprocess
import threading
import time
from dataclasses import dataclass
from pathlib import Path

import pytest


@dataclass
class QuackServer:
    """Connection details for a spawned Quack server."""

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
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_for_port(host: str, port: int, timeout: float = 60.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.settimeout(0.5)
            try:
                s.connect((host, port))
                return True
            except OSError:
                time.sleep(0.25)
    return False


@pytest.fixture(scope="session")
def quack_server() -> QuackServer:
    duckdb = os.environ.get("QUACK_IT_DUCKDB", shutil.which("duckdb"))
    if duckdb is None:
        pytest.skip("duckdb binary not on PATH; set QUACK_IT_DUCKDB to override")

    port = _pick_free_port()
    token = "adbc-driver-quack-it-token"

    process = subprocess.Popen(
        [duckdb, "-unsigned"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        bufsize=1,
    )
    assert process.stdin is not None

    commands = [
        ".mode csv",
        ".headers on",
        "INSTALL quack FROM core_nightly;",
        "LOAD quack;",
        f"CALL quack_serve('quack:127.0.0.1:{port}', token=>'{token}');",
    ]
    try:
        for line in commands:
            process.stdin.write(line + "\n")
        process.stdin.flush()
    except OSError as exc:
        process.kill()
        pytest.skip(f"duckdb subprocess exited early: {exc}")

    if not _wait_for_port("127.0.0.1", port, timeout=60.0):
        process.kill()
        pytest.skip(f"Quack server did not come up within 60s on :{port}")

    server = QuackServer(host="127.0.0.1", port=port, token=token)

    def _drain_stdout() -> None:
        assert process.stdout is not None
        try:
            for _ in process.stdout:
                pass
        except Exception:
            pass

    threading.Thread(target=_drain_stdout, daemon=True).start()

    yield server

    try:
        process.stdin.write(f"CALL quack_stop('quack:127.0.0.1:{port}');\n")
        process.stdin.write(".quit\n")
        process.stdin.flush()
    except OSError:
        pass
    try:
        process.wait(timeout=5.0)
    except subprocess.TimeoutExpired:
        process.kill()


@pytest.fixture(scope="session")
def driver_path() -> str:
    """Resolve the bundled Quack ADBC driver library path."""
    candidate = os.environ.get("ADBC_QUACK_LIBRARY")
    if candidate and Path(candidate).is_file():
        return candidate
    # Fall back to the package's bundled lib (after install or
    # after running `make -C ../pkg/quack`).
    import adbc_driver_quack  # noqa: F401

    from adbc_driver_quack import _driver_path

    return _driver_path()
