"""Run every Python example on the PyPI page (python/README.md) against the live database.

Each ```python block runs as written in its own interpreter, with only the documented local
server (``quack://localhost:9494``, token ``my-secret-token``) swapped for the test server, and with
the driver manifest the page tells you to install written first (into a temporary directory named
by ``ADBC_DRIVER_PATH``), so the page cannot show an example that does not work.
"""

from __future__ import annotations

import os
import pathlib
import re
import subprocess
import sys

import pytest

pytestmark = pytest.mark.integration

README = pathlib.Path(__file__).resolve().parents[1] / "README.md"
BLOCKS = re.findall(
    r"```python\n(.*?)```", README.read_text(encoding="utf-8"), flags=re.DOTALL
)


def _localize(code: str, server) -> str:
    return code.replace("quack://localhost:9494", server.uri).replace(
        "my-secret-token", server.token
    )


@pytest.fixture(scope="module")
def manifest_dir(tmp_path_factory) -> pathlib.Path:
    """``python -m adbc_driver_quack install-manifest``, as the page shows, into a temp dir."""
    directory = tmp_path_factory.mktemp("adbc-drivers")
    out = subprocess.run(
        [
            sys.executable,
            "-m",
            "adbc_driver_quack",
            "install-manifest",
            "--dir",
            str(directory),
        ],
        capture_output=True,
        text=True,
        check=True,
    )
    assert out.stdout.startswith("Wrote ADBC driver manifest: "), out.stdout
    assert (directory / "quack.toml").is_file()
    return directory


def test_page_has_examples():
    assert len(BLOCKS) >= 4


@pytest.mark.parametrize("index", range(len(BLOCKS)))
def test_pypi_readme_example(index, quack_server, manifest_dir):
    code = _localize(code=BLOCKS[index], server=quack_server)
    env = dict(os.environ, ADBC_DRIVER_PATH=str(manifest_dir))
    out = subprocess.run(
        [sys.executable, "-c", code],
        check=False,
        capture_output=True,
        text=True,
        env=env,
        timeout=300,
    )
    assert out.returncode == 0, (
        f"example {index} failed:\n{code}\n{out.stdout}\n{out.stderr}"
    )
    assert out.stdout.strip(), f"example {index} printed nothing"
