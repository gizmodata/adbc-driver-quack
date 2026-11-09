"""pytest wiring for the conformance suite.

Run against a DuckDB 2.0 Quack server set up for validation:

    python scripts/quack_test_server.py --port 9494 --validation &
    ADBC_QUACK_VALIDATION_URI=quack://127.0.0.1:9494 \
    ADBC_QUACK_VALIDATION_TOKEN=adbc-driver-quack-it-token \
        pytest validation

The driver library is ADBC_QUACK_LIBRARY, or the one in the installed
adbc_driver_quack package. A missing server is a failure, not a skip.
"""

from __future__ import annotations

import os
from pathlib import Path

import adbc_drivers_validation.model
import adbc_drivers_validation.tests.conftest
import pytest
from adbc_drivers_validation.tests.conftest import (  # noqa: F401
    conn,
    conn_factory,
    db_kwargs,
    manual_test,
    noci,
    pytest_collection_modifyitems,
)

from .quack import TOKEN_ENV, URI_ENV, get_quirks


def pytest_addoption(parser):
    adbc_drivers_validation.tests.conftest.pytest_addoption(parser)
    parser.addoption("--vendor-version", action="store", default="2.0")


def pytest_sessionstart(session):
    missing = [name for name in (URI_ENV, TOKEN_ENV) if not os.environ.get(name)]
    if missing:
        pytest.exit(f"set {', '.join(missing)}: the conformance suite needs a live Quack server", returncode=2)


@pytest.fixture(scope="session")
def driver(request, pytestconfig) -> adbc_drivers_validation.model.DriverQuirks:
    assert request.param.startswith("quack")
    return get_quirks(pytestconfig.getoption("vendor_version"))


@pytest.fixture(scope="session")
def driver_path(driver: adbc_drivers_validation.model.DriverQuirks) -> str:
    candidate = os.environ.get("ADBC_QUACK_LIBRARY")
    if candidate and Path(candidate).is_file():
        return candidate
    from adbc_driver_quack import _driver_path

    return _driver_path()
