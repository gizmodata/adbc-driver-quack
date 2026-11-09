"""Driver quirks for running the adbc-drivers.org conformance suite
(https://github.com/adbc-drivers/validation) against adbc-driver-quack.

The suite is a test dependency only; this repository is not part of, and
is not maintained by, the ADBC Driver Foundry.
"""

from __future__ import annotations

import functools
from pathlib import Path

from adbc_drivers_validation import model, quirks

URI_ENV = "ADBC_QUACK_VALIDATION_URI"
TOKEN_ENV = "ADBC_QUACK_VALIDATION_TOKEN"


class QuackQuirks(model.DriverQuirks):
    name = "quack"
    driver = "adbc_driver_quack"
    driver_name = "ADBC Quack Driver - Go"
    vendor_name = "DuckDB (via Quack)"
    vendor_version = "v2.0.0"  # as DuckDB reports it (SELECT version())
    short_version = "2.0"
    features = model.DriverFeatures(
        connection_get_table_schema=True,
        connection_set_current_catalog=True,
        connection_set_current_schema=True,
        connection_transactions=True,
        current_catalog="memory",
        current_schema="main",
        get_objects=True,
        get_objects_constraints_foreign=True,
        get_objects_constraints_primary=True,
        get_objects_constraints_unique=True,
        get_objects_tabletypes_filter=True,
        statement_bind=True,
        statement_bulk_ingest=True,
        statement_bulk_ingest_catalog=True,
        statement_bulk_ingest_schema=True,
        statement_bulk_ingest_temporary=True,
        statement_execute_schema=True,
        statement_get_parameter_schema=False,
        statement_prepare=True,
        statement_rows_affected=True,
        statement_rows_affected_ddl=False,
        secondary_catalog="quack_validation_catalog",
        secondary_catalog_schema="main",
        secondary_schema="quack_validation_secondary",
        supported_xdbc_fields=[],
        quirk_foundry=False,
    )
    setup = model.DriverSetup(
        database={
            "uri": model.FromEnv(URI_ENV),
            "adbc.quack.token": model.FromEnv(TOKEN_ENV),
        },
        connection={},
        statement={},
    )

    @property
    def queries_paths(self) -> tuple[Path]:
        return (Path(__file__).parent.parent / "queries",)

    def is_table_not_found(self, table_name: str | None, error: Exception) -> bool:
        text = str(error).lower()
        return "does not exist" in text or "not found" in text

    def drop_table(
        self,
        *,
        table_name: str,
        schema_name: str | None = None,
        catalog_name: str | None = None,
        if_exists: bool = True,
        temporary: bool = False,
    ) -> str:
        if temporary:
            # DuckDB keeps temporary tables in the "temp" catalog.
            catalog_name, schema_name = "temp", "main"
        name = self.quote_identifier(*[part for part in (catalog_name, schema_name, table_name) if part])
        return f"DROP TABLE {'IF EXISTS ' if if_exists else ''}{name}"

    def qualify_temp_table(self, cursor, name: str) -> list[str]:
        return ["temp", "main", name]

    @property
    def sample_ddl_constraints(self) -> list[str]:
        # A leading "z" column keeps the constraint columns off ordinal
        # position 1, so GetObjects must map them by name.
        return [
            'CREATE TABLE "constraint_primary" ("z" INTEGER, "a" INTEGER, "b" VARCHAR, PRIMARY KEY ("a"))',
            'CREATE TABLE "constraint_primary_multi" ("z" INTEGER, "a" INTEGER, "b" VARCHAR, PRIMARY KEY ("b", "a"))',
            'CREATE TABLE "constraint_primary_multi2" ("z" INTEGER, "a" VARCHAR, "b" INTEGER, PRIMARY KEY ("a", "b"))',
            'CREATE TABLE "constraint_unique" ("z" INTEGER, "a" INTEGER, "b" INTEGER, "c" INTEGER, UNIQUE ("a"), UNIQUE ("c", "b"))',
            'CREATE TABLE "constraint_foreign" ("z" INTEGER, "a" INTEGER, "b" INTEGER, '
            'FOREIGN KEY ("b") REFERENCES "constraint_primary" ("a"))',
            'CREATE TABLE "constraint_foreign_multi" ("z" INTEGER, "a" INTEGER, "b" INTEGER, "c" VARCHAR, '
            'FOREIGN KEY ("c", "b") REFERENCES "constraint_primary_multi2" ("a", "b"))',
        ]

    def split_statement(self, statement: str) -> list[str]:
        return quirks.split_statement(statement, dialect="duckdb")


@functools.cache
def get_quirks(version: str) -> QuackQuirks:
    q = QuackQuirks()
    if version not in {q.short_version, q.vendor_version}:
        raise ValueError(f"Unsupported DuckDB version for Quack: {version}")
    return q
