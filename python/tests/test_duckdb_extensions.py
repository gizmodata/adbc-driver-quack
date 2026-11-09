"""DuckDB extensions driving this driver.

* Query.Farm's ``adbc_scanner``: Hardcharger (https://gizmodata.com/hardcharger)
  and an embedded DuckDB read from and write to a Quack server *live*,
  loading this repo's c-shared driver library::

      Hardcharger / duckdb --adbc_scanner--> libadbc_driver_quack --Quack--> DuckDB

  Every scanner test runs twice: inside a Hardcharger server (the SQL goes
  over Chargewire and the server loads the driver) and in an in-process
  DuckDB. Pushed-down filters use bound parameters, which the driver
  renders client-side (Quack has no wire-level parameters).
* Columnar's ``adbc``: refuses the Quack driver by name; the test is an
  xfail that turns green if that changes.

On DuckDB 2.0 and later the scanner comes from the Query.Farm repository.
"""

from __future__ import annotations

import textwrap
from collections.abc import Callable
from dataclasses import dataclass

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.duckdb_ext]


def lit(value: str) -> str:
    """A SQL string literal (the SQL travels as text, so no bind parameters)."""
    return "'" + value.replace("'", "''") + "'"


@dataclass
class Engine:
    """Somewhere to run DuckDB SQL: an in-process DuckDB or a Hardcharger session."""

    name: str
    run: Callable[[str], list[tuple]]
    catalog: str

    def one(self, sql: str) -> tuple:
        (row,) = self.run(sql)
        return row


def install_adbc_scanner(run: Callable[[str], list[tuple]]) -> None:
    run("SET allow_extension_repositories = 'allowed'")
    run("CREATE EXTENSION REPOSITORY IF NOT EXISTS query_farm WITH PREFIX 'https://duckdb-extensions.query.farm'")
    run("INSTALL adbc_scanner FROM query_farm")
    run("LOAD adbc_scanner FROM query_farm")


@pytest.fixture()
def sample_table(quack_server):
    from adbc_driver_quack import dbapi as quack

    with quack.connect(quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=True) as c, c.cursor() as cur:
        cur.execute(
            "CREATE OR REPLACE TABLE ext_t AS "
            "SELECT i AS id, 'n' || i AS name, (i * 0.5)::DECIMAL(10,2) AS amt FROM range(10) t(i)"
        )
    yield "ext_t"
    with quack.connect(quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=True) as c, c.cursor() as cur:
        for t in ("ext_t", "ext_ctas", "ext_ins"):
            cur.execute(f"DROP TABLE IF EXISTS {t}")


@pytest.fixture(
    params=[
        pytest.param("hardcharger", marks=pytest.mark.hardcharger),
        pytest.param("duckdb"),
    ]
)
def engine(request, quack_server, driver_path, sample_table):
    if request.param == "hardcharger":
        server = request.getfixturevalue("hardcharger_server")
        conn = server.connect(autocommit=True)
        cur = conn.cursor()

        def run(sql: str) -> list[tuple]:
            cur.execute(operation=sql)
            return cur.fetchall() if cur.description else []

        close = conn.close
        library = server.driver_path or driver_path
    else:
        import duckdb

        con = duckdb.connect(database=":memory:")

        def run(sql: str) -> list[tuple]:
            return con.execute(query=sql).fetchall()

        close = con.close
        library = driver_path

    try:
        install_adbc_scanner(run=run)
        ((catalog,),) = run("SELECT current_database()")
        # README "Query Quack from Hardcharger or DuckDB": the token lives in
        # a secret; ATTACH exposes the Quack server as a catalog.
        run(
            f"""CREATE OR REPLACE SECRET quack_secret (
                TYPE adbc,
                SCOPE {lit(quack_server.uri)},
                driver {lit(library)},
                entrypoint 'QuackDriverInit',
                uri {lit(quack_server.uri)},
                extra_options MAP {{'adbc.quack.token': {lit(quack_server.token)}}}
            )"""
        )
        run("DETACH DATABASE IF EXISTS q")
        run(f"ATTACH {lit(quack_server.uri)} AS q (TYPE adbc)")
    except Exception:
        close()
        raise
    yield Engine(name=request.param, run=run, catalog=catalog)
    try:
        run(f'USE "{catalog}"')
        run("DETACH q")
        run("DROP SECRET quack_secret")
    finally:
        close()


def test_attach_with_filter_pushdown(engine):
    # Pushed-down filters bind string, integer and decimal parameters.
    assert engine.run("SELECT id FROM q.main.ext_t WHERE name = 'n3'") == [(3,)]
    assert engine.one("SELECT COUNT(*) FROM q.main.ext_t WHERE id > 6 AND amt >= 3.5") == (3,)


def test_adbc_scan_runs_quack_sql(engine):
    assert engine.run("SELECT * FROM adbc_scan('q', 'SELECT 42 AS x')") == [(42,)]
    assert engine.one("SELECT SUM(id) FROM adbc_scan('q', 'SELECT * FROM ext_t')") == (45,)


def test_writes_into_quack(engine, quack_server):
    from adbc_driver_quack import dbapi as quack

    engine.run("CREATE OR REPLACE TEMP TABLE local_rows AS SELECT i AS id FROM range(7) t(i)")
    # CTAS through the attached catalog...
    engine.run("USE q.main")
    engine.run("CREATE TABLE ext_ctas AS SELECT * FROM temp.main.local_rows WHERE id < 5")
    engine.run(f'USE "{engine.catalog}"')
    # ...and the adbc_insert function.
    engine.run("SELECT * FROM adbc_insert('q', 'ext_ins', (SELECT * FROM local_rows), mode := 'create')")

    with quack.connect(quack_server.uri, db_kwargs=quack_server.db_kwargs) as c, c.cursor() as cur:
        cur.execute("SELECT (SELECT COUNT(*) FROM ext_ctas), (SELECT COUNT(*) FROM ext_ins)")
        assert cur.fetchone() == (5, 7)


def test_columnar_adbc_extension(quack_server, sample_table, tmp_path, monkeypatch):
    import duckdb

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
    try:
        con.execute(query="INSTALL adbc FROM community")
        con.execute(query="LOAD adbc")
    except Exception as exc:
        pytest.xfail(f"Columnar's adbc extension is not available for this DuckDB: {exc}")
    try:
        (n,) = con.execute(query="SELECT COUNT(*) FROM read_adbc('profile://quacktest', 'SELECT * FROM ext_t')").fetchone()
    except Exception as exc:
        if "not supported" in str(exc):
            pytest.xfail(f"Columnar's adbc extension refuses the Quack driver: {exc}")
        raise
    assert n == 10
