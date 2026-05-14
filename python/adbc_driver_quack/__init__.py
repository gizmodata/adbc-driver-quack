"""Apache Arrow ADBC driver for DuckDB's Quack remote protocol."""

from __future__ import annotations

import enum
import functools
import typing

import adbc_driver_manager

from ._version import __version__  # noqa: F401

__all__ = [
    "ConnectionOptions",
    "DatabaseOptions",
    "StatementOptions",
    "connect",
]


class DatabaseOptions(enum.Enum):
    """Database options specific to the Quack driver."""

    #: The authentication token to send during the CONNECTION_REQUEST handshake.
    TOKEN = "adbc.quack.token"

    #: Set to "true" to use HTTPS for the underlying transport.
    TLS = "adbc.quack.tls"


class ConnectionOptions(enum.Enum):
    """Connection options specific to the Quack driver."""

    #: Reserved for future use.
    _RESERVED = "adbc.quack.connection.reserved"


class StatementOptions(enum.Enum):
    """Statement options specific to the Quack driver."""

    #: Reserved for future use.
    _RESERVED = "adbc.quack.statement.reserved"


@functools.lru_cache(maxsize=1)
def _driver_path() -> str:
    """Resolve the path to the bundled c-shared driver library."""
    try:
        from importlib import resources
    except ImportError:  # pragma: no cover
        import importlib_resources as resources  # type: ignore[import-not-found]

    import sys

    if sys.platform == "darwin":
        candidates = ("libadbc_driver_quack.dylib",)
    elif sys.platform.startswith("win"):
        candidates = ("adbc_driver_quack.dll", "libadbc_driver_quack.dll")
    else:
        candidates = ("libadbc_driver_quack.so",)

    pkg = resources.files("adbc_driver_quack")
    last_error: Exception | None = None
    for name in candidates:
        try:
            with resources.as_file(pkg / name) as path:
                return str(path)
        except (FileNotFoundError, ModuleNotFoundError) as exc:
            last_error = exc

    raise FileNotFoundError(
        "Could not locate the bundled Quack ADBC driver shared library. "
        "Rebuild the wheel with ADBC_QUACK_LIBRARY pointing at "
        "libadbc_driver_quack.{so,dylib,dll}. Last error: "
        f"{last_error!r}"
    )


def connect(
    uri: str,
    db_kwargs: typing.Mapping[str, str] | None = None,
) -> adbc_driver_manager.AdbcDatabase:
    """
    Open a low-level ADBC database against a Quack server.

    Most users will want :func:`adbc_driver_quack.dbapi.connect` instead,
    which returns a DBAPI 2.0 :class:`Connection`.

    Parameters
    ----------
    uri:
        Quack connection URL, e.g. ``"quack://127.0.0.1:9494"``.
    db_kwargs:
        Optional mapping of ADBC database options. Common keys:
        ``adbc.quack.token`` for the auth token,
        ``adbc.quack.tls`` to use HTTPS.
    """
    kwargs = {"driver": _driver_path(), "entrypoint": "QuackDriverInit", "uri": uri}
    if db_kwargs:
        kwargs.update(db_kwargs)
    return adbc_driver_manager.AdbcDatabase(**kwargs)
