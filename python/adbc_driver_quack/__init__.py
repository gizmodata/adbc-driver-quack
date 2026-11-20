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
#: name: ``{"quack.http.header.X-Proxy-Auth": "secret"}`` sends
#: ``X-Proxy-Auth: secret`` with every request (proxy/LB auth). An
#: empty value clears the header. Accepted only as ADBC options, never
#: as URL query parameters.
HTTP_HEADER_OPTION_PREFIX = "quack.http.header."


class DatabaseOptions(enum.Enum):
    """Database options specific to the Quack driver.

    The keys follow the ADBC driver style guide (``quack.<group>.<option>``).
    The pre-1.1 ``adbc.quack.*`` keys still work, and print a deprecation
    warning to stderr the first time each one is used in a process.
    """

    #: The authentication token to send during the CONNECTION_REQUEST handshake.
    TOKEN = "quack.auth.token"

    #: Name of an environment variable to read the token from.
    #: Accepted only as an option, never as a URL query parameter.
    TOKEN_ENV = "quack.auth.token_env"

    #: Path to a local file to read the token from.
    #: Accepted only as an option, never as a URL query parameter.
    TOKEN_FILE = "quack.auth.token_file"

    #: "true" for HTTPS, "false" for plain HTTP. Unset follows DuckDB 2.0:
    #: plain HTTP to localhost / 127.0.0.1 / ::1, HTTPS to any other host.
    TLS = "quack.tls.enabled"

    #: SHA-256 fingerprint of the one server certificate to trust instead of
    #: CA verification (how a client authenticates a quack_serve self-signed
    #: certificate). Implies TLS. Accepts the colon-separated form openssl prints.
    SSL_FINGERPRINT = "quack.tls.certificate_fingerprint"

    #: Connection lease requested from the server, in seconds (default 60).
    #: The driver renews it with a background heartbeat while idle.
    HEARTBEAT_TIMEOUT = "quack.session.heartbeat_timeout_seconds"

    #: Opaque client identifier shown (hashed) in the server's
    #: quack_active_connections(). Empty, or at least 4 characters.
    CLIENT_ID = "quack.session.client_id"

    #: HTTP connect timeout: seconds, or a Go duration like "1.5s" (default 10).
    CONNECT_TIMEOUT = "quack.http.connect_timeout_seconds"

    #: Per-request HTTP timeout: seconds, or a Go duration like "90s" (default 60).
    REQUEST_TIMEOUT = "quack.http.request_timeout_seconds"


class ConnectionOptions(enum.Enum):
    """Connection options specific to the Quack driver."""

    #: Reserved for future use.
    _RESERVED = "quack.connection.reserved"


class StatementOptions(enum.Enum):
    """Statement options specific to the Quack driver."""

    #: Reserved for future use.
    _RESERVED = "quack.query.reserved"


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
        ``quack.auth.token`` for the auth token,
        ``quack.tls.enabled`` to use HTTPS.
    """
    kwargs = {"driver": _driver_path(), "entrypoint": "AdbcDriverQuackInit", "uri": uri}
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
