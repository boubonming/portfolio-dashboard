#!/usr/bin/env python3
"""Migrator-faithful contract checks for the non-runtime holding artifact."""

from __future__ import annotations

import pathlib
import sqlite3

ROOT = pathlib.Path(__file__).resolve().parents[1]
MIGRATIONS = ROOT / "internal" / "store" / "migrations"
ARTIFACT = ROOT / "docs" / "holding-mutation-contract.sql"
TS = "2026-01-03T00:00:00.000000000Z"
RETAIN = "2033-01-03T00:00:00.000000000Z"


def migrated() -> sqlite3.Connection:
    db = sqlite3.connect(":memory:")
    db.execute("PRAGMA foreign_keys=ON")
    for path in sorted(MIGRATIONS.glob("*.sql")):
        db.executescript(path.read_text())
    return db


def expect_reject(db: sqlite3.Connection, sql: str, params: tuple = ()) -> None:
    try:
        db.execute(sql, params)
    except sqlite3.IntegrityError:
        db.rollback()
        return
    raise AssertionError(f"expected rejection: {sql}")


def seed_populated(db: sqlite3.Connection) -> None:
    db.executescript(
        """
        INSERT INTO portfolios VALUES
          ('portfolio-default','Personal Portfolio','MYR','2026-01-01T00:00:00.000000000Z','2026-01-02T00:00:00.000000000Z'),
          ('portfolio-other','Other','USD','2026-01-01T00:00:00.000000000Z','2026-01-02T00:00:00.000000000Z');
        INSERT INTO instruments VALUES
          ('instrument-shared','SHARED','Shared','MYR','equity','TEST','2026-01-01T00:00:00.000000000Z'),
          ('instrument-other','OTHER','Other','USD','equity','TEST','2026-01-01T00:00:00.000000000Z');
        INSERT INTO accounts VALUES
          ('account-default','portfolio-default','broker','Default','2026-01-01T00:00:00.000000000Z'),
          ('account-other','portfolio-other','broker','Other','2026-01-01T00:00:00.000000000Z');
        INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,acquisition_date,source_row,source_ref,created_at,source_sha256) VALUES
          ('lot-one','portfolio-default','instrument-shared','account-default','1','10','10','MYR','2026-01-01',1,'legacy-one','2026-01-01T00:00:00.000000000Z','hash-one'),
          ('lot-two','portfolio-default','instrument-shared','account-default','2','10','20','MYR','2026-01-02',2,'legacy-two','2026-01-02T00:00:00.000000000Z','hash-two'),
          ('lot-other','portfolio-other','instrument-other','account-other','3','5','15','USD','2026-01-01',3,'legacy-other','2026-01-01T00:00:00.000000000Z','hash-other');
        INSERT INTO snapshots VALUES ('snapshot-default','portfolio-default','v1','{"historical":true}','2026-01-03T00:00:00.000000000Z','legacy-input');
        INSERT INTO reviews VALUES ('review-default','snapshot-default','instrument-shared','complete','legacy rationale','{"historical":true}','2026-01-03T00:00:00.000000000Z');
        INSERT INTO portfolio_theses VALUES ('thesis-default','portfolio-default','lot-one','instrument-shared','legacy','long','10','["hold"]','["risk"]','["sell"]','["change"]','[]',1,'thesis-hash','2026-01-03T00:00:00.000000000Z','2026-01-03T00:00:00.000000000Z');
        INSERT INTO thesis_revisions VALUES ('thesis-revision-default','thesis-default','portfolio-default','lot-one','instrument-shared',1,'legacy','long','10','["hold"]','["risk"]','["sell"]','["change"]','[]','thesis-hash','2026-01-03T00:00:00.000000000Z');
        INSERT INTO audit_events VALUES ('audit-legacy','legacy','note','portfolio','portfolio-default','{"legacy":true}','2026-01-03T00:00:00.000000000Z',NULL);
        INSERT INTO audit_event_portfolios VALUES ('audit-legacy','portfolio-default','2033-01-03T00:00:00.000000000Z');
        INSERT INTO activity_events VALUES ('activity-legacy','portfolio-default','browser_user','note','system','portfolio','portfolio-default','legacy','','2026-01-03T00:00:00.000000000Z','2026-01-03T00:00:00.000000000Z','audit-legacy','2033-01-03T00:00:00.000000000Z');
        """
    )


def historical_rows(db: sqlite3.Connection) -> dict[str, tuple[list[str], list[tuple]]]:
    tables = ("portfolios", "instruments", "accounts", "lots", "snapshots", "reviews", "portfolio_theses", "thesis_revisions", "audit_events", "audit_event_portfolios", "activity_events")
    result = {}
    for table in tables:
        columns = [row[1] for row in db.execute(f"PRAGMA table_info({table})")]
        names = ",".join(columns)
        result[table] = (columns, db.execute(f"SELECT {names} FROM {table} ORDER BY rowid").fetchall())
    return result


def check_fresh() -> None:
    db = migrated()
    db.executescript(ARTIFACT.read_text())
    assert db.execute("SELECT name, reporting_currency, holdings_revision FROM portfolios WHERE id='portfolio-default'").fetchone() == ("Portfolio", "MYR", 0)
    assert db.execute("SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-default'").fetchone()[0] == 0
    assert not db.execute("PRAGMA foreign_key_check").fetchall()


def check_populated() -> None:
    db = migrated()
    seed_populated(db)
    before = historical_rows(db)
    db.executescript(ARTIFACT.read_text())
    for table, (columns, rows) in before.items():
        names = ",".join(columns)
        assert db.execute(f"SELECT {names} FROM {table} ORDER BY rowid").fetchall() == rows, table
    assert db.execute("SELECT name FROM portfolios WHERE id='portfolio-default'").fetchone()[0] == "Personal Portfolio"
    assert db.execute("SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-default'").fetchone()[0] == 2
    assert db.execute("SELECT COUNT(*) FROM portfolio_instruments WHERE portfolio_id='portfolio-default' AND instrument_id='instrument-shared'").fetchone()[0] == 1

    # Same-portfolio association succeeds; an unassociated instrument and account do not.
    db.execute("INSERT INTO instruments VALUES ('instrument-new','NEW','New','MYR','equity','TEST',?)", (TS,))
    db.execute("INSERT INTO portfolio_instruments VALUES ('portfolio-default','instrument-new',?)", (TS,))
    db.execute("INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at) VALUES ('lot-new','portfolio-default','instrument-new',NULL,'1','1','1','MYR',?)", (TS,))
    db.commit()
    lot_count = db.execute("SELECT COUNT(*) FROM lots").fetchone()[0]
    expect_reject(db, "INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at) VALUES ('bad-instrument','portfolio-default','instrument-other',NULL,'1','1','1','MYR',?)", (TS,))
    expect_reject(db, "INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at) VALUES ('bad-account','portfolio-default','instrument-shared','account-other','1','1','1','MYR',?)", (TS,))
    assert db.execute("SELECT COUNT(*) FROM lots").fetchone()[0] == lot_count

    # Archive metadata is canonical, ordered, bounded, and one-way.
    db.execute("UPDATE lots SET lifecycle='removed', removed_at=?, removed_by_category='browser_user', removed_by_id='local-owner' WHERE id='lot-one'", (TS,))
    db.commit()
    expect_reject(db, "UPDATE lots SET quantity='9' WHERE id='lot-one'")
    expect_reject(db, "UPDATE lots SET lifecycle='active' WHERE id='lot-one'")
    expect_reject(db, "UPDATE lots SET lifecycle='removed', removed_at='not-a-timestamp', removed_by_category='browser_user', removed_by_id='local-owner' WHERE id='lot-two'")
    expect_reject(db, "UPDATE lots SET lifecycle='removed', removed_at='2025-01-01T00:00:00.000000000Z', removed_by_category='browser_user', removed_by_id='local-owner' WHERE id='lot-two'")
    expect_reject(db, "UPDATE lots SET lifecycle='removed', removed_at=?, removed_by_category='browser_user', removed_by_id='bad actor' WHERE id='lot-two'", (TS,))

    # Direct revision changes are trusted physical access; only single-step increments pass.
    db.execute("UPDATE portfolios SET holdings_revision=holdings_revision+1 WHERE id='portfolio-default'")
    expect_reject(db, "UPDATE portfolios SET holdings_revision=holdings_revision+2 WHERE id='portfolio-default'")

    # A correctly owned audit/Activity/idempotency chain succeeds.
    payload = '{"portfolio_id":"portfolio-default","holding_id":"lot-two","operation":"create","occurred_at":"%s"}' % TS
    db.execute("INSERT INTO audit_events VALUES ('audit-holding','local-owner','holding_created','holding','lot-two',?, ?, ?)", (payload, TS, RETAIN))
    db.execute("INSERT INTO audit_event_portfolios VALUES ('audit-holding','portfolio-default',?)", (RETAIN,))
    db.execute("INSERT INTO activity_events VALUES ('activity-holding','portfolio-default','browser_user','holding_created','holding','holding','lot-two','created','',?,?,?,?)", (TS, TS, 'audit-holding', RETAIN))
    db.execute("INSERT INTO holding_mutation_idempotency VALUES ('browser_user','local-owner','portfolio-default','create','idempotency-key-001','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','lot-two','audit-holding','activity-holding',1,0,'{}',?,?)", (TS, RETAIN))
    db.commit()
    before_bad = db.execute("SELECT COUNT(*) FROM holding_mutation_idempotency").fetchone()[0]
    expect_reject(db, "INSERT INTO holding_mutation_idempotency VALUES ('browser_user','local-owner','portfolio-default','remove','idempotency-key-002','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','lot-two','audit-holding','activity-holding',1,0,'{}',?,?)", (TS, RETAIN))
    assert db.execute("SELECT COUNT(*) FROM holding_mutation_idempotency").fetchone()[0] == before_bad
    assert not db.execute("PRAGMA foreign_key_check").fetchall()


if __name__ == "__main__":
    check_fresh()
    check_populated()
    print("holding mutation contract harness: PASS (fresh, populated, scope, archive, revision, ownership, rollback, foreign keys)")
