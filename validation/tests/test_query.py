import adbc_drivers_validation.tests.query as query_tests

from .quack import get_quirks


def pytest_generate_tests(metafunc) -> None:
    quirks = [get_quirks(metafunc.config.getoption("vendor_version"))]
    return query_tests.generate_tests(quirks, metafunc)


class TestQuery(query_tests.TestQuery):
    pass
