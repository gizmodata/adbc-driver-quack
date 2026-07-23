"""Apache Arrow ADBC driver for DuckDB's Quack remote protocol."""

from __future__ import annotations

import enum
import functools
import typing

import adbc_driver_manager

from ._version import __version__  # noqa: F401

__all__ = [
    "HTTP_HEADER_OPTION_PREFIX",
    "ConnectionOptions",
    "DatabaseOptions",
    "StatementOptions",
    "connect",
    "install_manifest",
]

#: Prefix for extra-HTTP-header database options. Append the header
#: name: ``{"adbc.quack.http.header.X-Proxy-Auth": "secret"}`` sends
#: ``X-Proxy-Auth: secret`` with every request (proxy/LB auth). An
#: empty value clears the header. Accepted only as ADBC options, never
#: as URL query parameters.
HTTP_HEADER_OPTION_PREFIX = "adbc.quack.http.header."


class DatabaseOptions(enum.Enum):
    """Database options specific to the Quack driver."""

    #: The authentication token to send during the CONNECTION_REQUEST handshake.
    TOKEN = "adbc.quack.token"

    #: Name of an environment variable to read the token from.
    #: Accepted only as an option, never as a URL query parameter.
    TOKEN_ENV = "adbc.quack.token_env"

    #: Path to a local file to read the token from.
    #: Accepted only as an option, never as a URL query parameter.
    TOKEN_FILE = "adbc.quack.token_file"

    #: Set to "true" to use HTTPS for the underlying transport.
    TLS = "adbc.quack.tls"

    #: HTTP connect timeout: seconds, or a Go duration like "1.5s" (default 10).
    CONNECT_TIMEOUT = "adbc.quack.rpc.timeout_seconds.connect"

    #: Per-request HTTP timeout: seconds, or a Go duration like "90s" (default 60).
    REQUEST_TIMEOUT = "adbc.quack.rpc.timeout_seconds.request"


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

    import os
    import sys

    if sys.platform == "darwin":
        candidates = ("libadbc_driver_quack.dylib",)
    elif sys.platform.startswith("win"):
        candidates = ("libadbc_driver_quack.dll", "adbc_driver_quack.dll")
    else:
        candidates = ("libadbc_driver_quack.so",)

    # resources.as_file() does NOT verify that the file exists on disk
    # (it only handles extraction from zip-backed resources), so we have
    # to check explicitly. Otherwise a stale stringified path is fed to
    # ADBC's driver manager, which on Windows misparses the drive-letter
    # colon as a `name:entrypoint` separator and reports the cryptic
    # "Could not load `D`" error.
    pkg = resources.files("adbc_driver_quack")
    tried: list[str] = []
    for name in candidates:
        try:
            with resources.as_file(pkg / name) as path:
                resolved = str(path)
                if os.path.isfile(resolved):
                    return resolved
                tried.append(resolved)
        except (FileNotFoundError, ModuleNotFoundError):
            tried.append(name)

    raise FileNotFoundError(
        "Could not locate the bundled Quack ADBC driver shared library. "
        "Rebuild the wheel with ADBC_QUACK_LIBRARY pointing at "
        "libadbc_driver_quack.{so,dylib,dll}. Looked for: "
        + ", ".join(tried)
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
        # dict.update is a C-extension method whose kwargs become new
        # dict keys, not a merge target — so the merge arg stays positional.
        kwargs.update(db_kwargs)
    return adbc_driver_manager.AdbcDatabase(**kwargs)


def install_manifest(*args, **kwargs):
    """Write the quack.toml ADBC driver manifest; see :mod:`._manifest`.

    After installing the manifest, this driver can be resolved by name —
    via connection profiles (``driver = "quack"``) or directly by URI
    scheme: ``adbc_driver_manager.dbapi.connect(uri="quack://...")``.
    Also available as ``python -m adbc_driver_quack install-manifest``.
    """
    from ._manifest import install_manifest as _impl

    return _impl(*args, **kwargs)
