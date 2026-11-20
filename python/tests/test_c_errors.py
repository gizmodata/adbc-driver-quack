"""Errors through the C library's plain ADBC 1.0 path (no error details).

A C caller that leaves ``vendor_code`` at 0 (``ADBC_ERROR_INIT``) gets the message copied
by the C wrapper's ``setErr``, which once used it as a printf format, so a ``%`` in a
message came out as ``%!o(MISSING)``. Calls the library's exported ``Adbc*`` functions
with ctypes; no server needed.
"""

from __future__ import annotations

import ctypes

import pytest


class AdbcError(ctypes.Structure):
    _fields_ = [
        ("message", ctypes.c_char_p),
        ("vendor_code", ctypes.c_int32),
        ("sqlstate", ctypes.c_char * 5),
        ("release", ctypes.c_void_p),
        ("private_data", ctypes.c_void_p),
        ("private_driver", ctypes.c_void_p),
    ]


class AdbcDatabase(ctypes.Structure):
    _fields_ = [("private_data", ctypes.c_void_p), ("private_driver", ctypes.c_void_p)]


ADBC_STATUS_NOT_IMPLEMENTED = 2


@pytest.fixture(scope="module")
def lib(driver_path):
    library = ctypes.CDLL(driver_path)
    for name in ("AdbcDatabaseNew", "AdbcDatabaseInit", "AdbcDatabaseRelease"):
        getattr(library, name).argtypes = [ctypes.POINTER(AdbcDatabase), ctypes.POINTER(AdbcError)]
        getattr(library, name).restype = ctypes.c_uint8
    library.AdbcDatabaseSetOption.argtypes = [
        ctypes.POINTER(AdbcDatabase),
        ctypes.c_char_p,
        ctypes.c_char_p,
        ctypes.POINTER(AdbcError),
    ]
    library.AdbcDatabaseSetOption.restype = ctypes.c_uint8
    return library


def _release(error: AdbcError) -> None:
    if error.release:
        ctypes.CFUNCTYPE(None, ctypes.POINTER(AdbcError))(error.release)(ctypes.byref(error))


@pytest.mark.parametrize("vendor_code", [0, -2147483648], ids=["adbc-1.0-error", "error-details"])
def test_message_with_percent_sign_survives(lib, vendor_code):
    db = AdbcDatabase()
    error = AdbcError(vendor_code=vendor_code)
    assert lib.AdbcDatabaseNew(ctypes.byref(db), ctypes.byref(error)) == 0
    try:
        assert lib.AdbcDatabaseSetOption(ctypes.byref(db), b"uri", b"quack://127.0.0.1:1", ctypes.byref(error)) == 0
        assert lib.AdbcDatabaseSetOption(ctypes.byref(db), b"quack.50%off", b"x", ctypes.byref(error)) == 0
        status = lib.AdbcDatabaseInit(ctypes.byref(db), ctypes.byref(error))
        assert status == ADBC_STATUS_NOT_IMPLEMENTED
        message = error.message.decode()
        assert message == '[quack] unsupported option "quack.50%off"', message
        _release(error)
    finally:
        lib.AdbcDatabaseRelease(ctypes.byref(db), ctypes.byref(AdbcError()))
