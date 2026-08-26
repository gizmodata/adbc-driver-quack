"""DuckDB community extensions driving this driver.

* Query.Farm's ``adbc_scanner``: ``CREATE SECRET`` + ``ATTACH`` (with
  projection and filter pushdown — the pushed-down filters use bound
  parameters, which the driver renders client-side since Quack has no
  wire-level parameters).
* Columnar's ``adbc``: currently refuses the Quack driver by name; the
  test is an xfail that turns green if that changes.
"""

from __future__ import annotations

import textwrap

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.duckdb_ext]


def _load(con, ext):
    try:
        con.execute(query=f"INSTALL {ext} FROM community")
        con.execute(query=f"LOAD {ext}")
    except Exception as exc:  # pragma: no cover - environment dependent
        pytest.skip(reason=f"{ext} extension unavailable: {exc}")


@pytest.fixture()
def sample_table(quack_server):
    import adbc_driver_quack.dbapi as quack

    with quack.connect(quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=True) as c, c.cursor() as cur:
        cur.execute("CREATE OR REPLACE TABLE ext_t AS SELECT i AS id, 'n' || i AS name, (i * 0.5)::DECIMAL(10,2) AS amt FROM range(10) t(i)")
    yield "ext_t"
    with quack.connect(quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=True) as c, c.cursor() as cur:
        cur.execute("DROP TABLE IF EXISTS ext_t")


def test_adbc_scanner_secret_attach_pushdown(quack_server, sample_table):
    """README "Query Quack from DuckDB or GizmoSQL (adbc_scanner)"."""
    duckdb = pytest.importorskip("duckdb")
    import adbc_driver_quack

    con = duckdb.connect(database=":memory:")
    _load(con, "adbc_scanner")
    con.execute(
        query="""CREATE SECRET quack_secret (
            TYPE adbc,
            SCOPE $uri,
            driver $driver,
            entrypoint 'QuackDriverInit',
            uri $uri,
            extra_options MAP {'adbc.quack.token': $token}
        )""",
        parameters={"driver": adbc_driver_quack._driver_path(), "uri": quack_server.uri, "token": quack_server.token},
    )
    con.execute(query=f"ATTACH '{quack_server.uri}' AS q (TYPE adbc)")
    # Filter pushdown with string, integer and decimal parameters.
    assert con.execute(query="SELECT id FROM q.main.ext_t WHERE name = 'n3'").fetchall() == [(3,)]
    assert con.execute(query="SELECT COUNT(*) FROM q.main.ext_t WHERE id > 6 AND amt >= 3.5").fetchone() == (3,)
    # Function API through the same secret.
    con.execute(query="SET VARIABLE q = adbc_connect({'secret': 'quack_secret'})")
    assert con.execute(query="SELECT * FROM adbc_scan(getvariable('q')::BIGINT, 'SELECT 42 AS x')").fetchall() == [(42,)]
    con.execute(query="SELECT adbc_disconnect(getvariable('q')::BIGINT)")
    con.execute(query="DETACH q")
    con.close()


def test_columnar_adbc_extension(quack_server, sample_table, tmp_path, monkeypatch):
    duckdb = pytest.importorskip("duckdb")
    import adbc_driver_quack

    drivers = tmp_path / "drivers"
    profiles = tmp_path / "profiles"
    profiles.mkdir()
    adbc_driver_quack.install_manifest(directory=drivers)
    (profiles / "quacktest.toml").write_text(
        textwrap.dedent(
            f"""\
            profile_version = 1
            driver = "quack"

            [Options]
            uri = "{quack_server.uri}"
            "adbc.quack.token" = "{quack_server.token}"
            """
        ),
        encoding="utf-8",
    )
    monkeypatch.setenv("ADBC_DRIVER_PATH", str(drivers))
    monkeypatch.setenv("ADBC_PROFILE_PATH", str(profiles))

    con = duckdb.connect(database=":memory:")
    _load(con, "adbc")
    try:
        (n,) = con.execute(query="SELECT COUNT(*) FROM read_adbc('profile://quacktest', 'SELECT * FROM ext_t')").fetchone()
    except Exception as exc:
        if "not supported" in str(exc):
            pytest.xfail(f"Columnar's adbc extension refuses the Quack driver: {exc}")
        raise
    assert n == 10
