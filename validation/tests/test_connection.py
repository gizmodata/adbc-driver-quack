import adbc_drivers_validation.tests.connection as connection_tests

from .quack import get_quirks


def pytest_generate_tests(metafunc) -> None:
    quirks = [get_quirks(metafunc.config.getoption("vendor_version"))]
    return connection_tests.generate_tests(quirks, metafunc)


class TestConnection(connection_tests.TestConnection):
    pass
