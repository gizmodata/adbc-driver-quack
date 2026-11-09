"""Quack -> Hardcharger (https://gizmodata.com/hardcharger), ADBC to ADBC:
the README's "Share data with Hardcharger" example, run for real.

A Quack result streams straight into Hardcharger's bulk ingest without
being materialised on the client, VARIANT and GEOMETRY columns included.
"""

from __future__ import annotations

import pytest

from adbc_driver_quack import dbapi as quack

pytestmark = [pytest.mark.integration, pytest.mark.hardcharger]


def test_stream_a_quack_table_into_hardcharger(quack_server, hardcharger_server):
    with quack.connect(uri=quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=True) as src:
        with src.cursor() as s:
            s.execute(
                "CREATE OR REPLACE TABLE orders AS SELECT i AS id, 'customer ' || (i % 97) AS customer, "
                "(i * 1.25)::DECIMAL(12, 2) AS amount, TIMESTAMP '2026-01-01' + to_seconds(i) AS placed_at, "
                "{'channel': CASE WHEN i % 2 = 0 THEN 'web' ELSE 'store' END, 'items': i % 5}::VARIANT AS attrs, "
                "('POINT (' || (i % 180) || ' ' || (i % 90) || ')')::GEOMETRY AS location FROM range(250000) t(i)"
            )
    with quack.connect(uri=quack_server.uri, db_kwargs=quack_server.db_kwargs) as src, hardcharger_server.connect(
        autocommit=False
    ) as dst:
        with src.cursor() as s, dst.cursor() as d:
            s.execute("SELECT * FROM orders")
            rows = d.adbc_ingest(table_name="orders_from_quack", data=s.fetch_record_batch(), mode="replace")
            dst.commit()
        assert rows == 250000
        with dst.cursor() as d:
            d.execute(
                "SELECT count(*), sum(amount), count(DISTINCT customer), typeof(any_value(attrs)), "
                "typeof(any_value(location)), sum(attrs.items::INTEGER) FROM orders_from_quack"
            )
            n, total, customers, attrs_type, location_type, items = d.fetchone()
            d.execute("DROP TABLE orders_from_quack")
            dst.commit()
    with quack.connect(uri=quack_server.uri, db_kwargs=quack_server.db_kwargs, autocommit=True) as src:
        with src.cursor() as s:
            s.execute(
                "SELECT count(*), sum(amount), count(DISTINCT customer), sum(attrs.items::INTEGER) FROM orders"
            )
            want = s.fetchone()
            s.execute("DROP TABLE orders")
    assert (n, total, customers, items) == want
    assert attrs_type == "VARIANT"
    assert location_type.startswith("GEOMETRY")
