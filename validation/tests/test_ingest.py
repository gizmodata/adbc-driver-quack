import adbc_drivers_validation.tests.ingest as ingest_tests

from .quack import get_quirks


def pytest_generate_tests(metafunc) -> None:
    quirks = [get_quirks(metafunc.config.getoption("vendor_version"))]
    return ingest_tests.generate_tests(quirks, metafunc)


class TestIngest(ingest_tests.TestIngest):
    pass
