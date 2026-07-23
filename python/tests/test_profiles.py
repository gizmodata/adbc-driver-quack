"""End-to-end tests for driver-manifest resolution, connection
profiles, and extra HTTP headers against a live Quack server.

These prove the full chain CI-side: install-manifest output → driver
manager name/scheme/profile resolution → our driver → real server.
"""

from __future__ import annotations

import pytest

pytestmark = pytest.mark.integration


@pytest.fixture()
def manifest_env(tmp_path, monkeypatch):
    """Install the quack.toml manifest and point the driver manager at it."""
    import adbc_driver_quack

    drivers = tmp_path / "drivers"
    adbc_driver_quack.install_manifest(directory=drivers)
    monkeypatch.setenv("ADBC_DRIVER_PATH", str(drivers))
    return tmp_path


def test_connect_by_driver_name(quack_server, manifest_env):
    """Manifest resolution: driver="quack", no import-derived path."""
    from adbc_driver_manager import dbapi

    with dbapi.connect(
        driver="quack",
        db_kwargs={"uri": quack_server.uri, **quack_server.db_kwargs},
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 42 AS answer")
        assert cur.fetchone() == (42,)


def test_connect_by_uri_scheme(quack_server, manifest_env, monkeypatch):
    """Scheme resolution: the quack:// prefix alone selects the driver."""
    from adbc_driver_manager import dbapi

    # Token has to ride along as an option; the URI carries no secret.
    with dbapi.connect(
        uri=quack_server.uri, db_kwargs=quack_server.db_kwargs
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 'scheme' AS via")
        assert cur.fetchone() == ("scheme",)


def test_connect_via_profile(quack_server, manifest_env, monkeypatch):
    """Profile resolution incl. {{ env_var(...) }} token substitution."""
    from adbc_driver_manager import dbapi

    profiles = manifest_env / "profiles"
    profiles.mkdir()
    (profiles / "quack_it.toml").write_text(
        f'''\
profile_version = 1
driver = "quack"

[Options]
uri = "{quack_server.uri}"
"adbc.quack.token" = "{{{{ env_var(QUACK_IT_TOKEN) }}}}"
''',
        encoding="utf-8",
    )
    monkeypatch.setenv("ADBC_PROFILE_PATH", str(profiles))
    monkeypatch.setenv("QUACK_IT_TOKEN", quack_server.token)

    with dbapi.connect(profile="quack_it") as conn, conn.cursor() as cur:
        cur.execute("SELECT 'profile' AS via")
        assert cur.fetchone() == ("profile",)


def test_extra_http_headers_full_stack(quack_server):
    """Header options flow through the whole stack without breaking
    the protocol (the server ignores unknown headers)."""
    import adbc_driver_quack.dbapi as quack

    with quack.connect(
        uri=quack_server.uri,
        db_kwargs={
            **quack_server.db_kwargs,
            "adbc.quack.http.header.X-Proxy-Auth": "it-header-value",
        },
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 7 AS lucky")
        assert cur.fetchone() == (7,)


def test_extra_http_headers_rejected_on_url(quack_server):
    """A pasted URL cannot inject headers — parse-time rejection."""
    import adbc_driver_quack.dbapi as quack

    with pytest.raises(Exception, match="not a URL query parameter"):
        quack.connect(
            uri=quack_server.uri + "?adbc.quack.http.header.X-Evil=1",
            db_kwargs=quack_server.db_kwargs,
        )
