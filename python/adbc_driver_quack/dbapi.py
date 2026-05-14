"""
DBAPI 2.0 wrapper for the Quack ADBC driver.

Thin re-export of ``adbc_driver_manager.dbapi`` with a ``connect`` shim
that knows how to load the Quack shared library.
"""

from __future__ import annotations

import typing

import adbc_driver_manager
import adbc_driver_manager.dbapi

import adbc_driver_quack


def connect(
    uri: str,
    *,
    db_kwargs: typing.Mapping[str, str] | None = None,
    conn_kwargs: typing.Mapping[str, str] | None = None,
    autocommit: bool = False,
) -> adbc_driver_manager.dbapi.Connection:
    """
    Open a DBAPI 2.0 :class:`Connection` against a Quack server.

    Parameters
    ----------
    uri:
        Quack connection URL, e.g. ``"quack://127.0.0.1:9494"``.
    db_kwargs:
        Optional mapping of ADBC database options. Common keys:
        ``adbc.quack.token`` for the auth token,
        ``adbc.quack.tls`` to use HTTPS.
    conn_kwargs:
        Optional mapping of ADBC connection options.
    autocommit:
        Forwarded to the underlying connection.
    """
    db = adbc_driver_quack.connect(uri, db_kwargs=db_kwargs)
    return adbc_driver_manager.dbapi.Connection(db, conn_kwargs=dict(conn_kwargs or {}), autocommit=autocommit)
