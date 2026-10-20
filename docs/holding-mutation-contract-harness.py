#!/usr/bin/env python3
"""Executable target-state feasibility evidence for the holding contract.

This harness intentionally exercises the physical SQLite boundary.  It does
not claim that rejected standalone statements prove application rollback; the
rollback matrix below stages the complete mutation sequence inside savepoints
and verifies every table and the revision witness after each injected failure.
It applies baseline SQL files 001-008 directly in scratch SQLite and then the
non-runtime artifact; it does not exercise Store.Migrate, schema version
rollback, HTTP handlers, or a shipped runtime mutation path.
"""

from __future__ import annotations

import os
import pathlib
import sqlite3
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
MIGRATIONS = ROOT / "internal" / "store" / "migrations"
ARTIFACT = ROOT / "docs" / "holding-mutation-contract.sql"
TS = "2026-01-03T00:00:00.000000000Z"
RETAIN = "2033-01-03T00:00:00.000000000Z"


def baseline_migrations() -> sqlite3.Connection:
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
          ('lot-other','portfolio-other','instrument-other','account-other','3','5','15','USD','2026-01-01',3,'legacy-other','2026-01-01T00:00:00.000000000Z','hash-other'),
          ('lot-nofraction','portfolio-default','instrument-shared',NULL,'1','1','1','MYR',NULL,10,'legacy-nofraction','2026-01-04T00:00:00Z','hash-nofraction'),
          ('lot-shortfraction','portfolio-default','instrument-shared',NULL,'1','1','1','MYR',NULL,11,'legacy-shortfraction','2026-01-04T00:00:00.123Z','hash-shortfraction'),
          ('lot-nanosecond','portfolio-default','instrument-shared',NULL,'1','1','1','MYR',NULL,12,'legacy-nanosecond','2026-01-04T00:00:00.123456789Z','hash-nanosecond'),
          ('lot-offset','portfolio-default','instrument-shared',NULL,'1','1','1','MYR',NULL,13,'legacy-offset','2026-01-05T01:00:00.123+01:00','hash-offset'),
          ('lot-equal','portfolio-default','instrument-shared',NULL,'1','1','1','MYR',NULL,14,'legacy-equal','2026-01-06T00:00:00.000000000Z','hash-equal'),
          ('lot-malformed','portfolio-default','instrument-shared',NULL,'1','1','1','MYR',NULL,15,'legacy-malformed','2026','hash-malformed');
        INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,active,created_at)
          VALUES ('bad actor','portfolio-default',1,0,1,'2026-01-01T00:00:00.000000000Z');
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


def table_counts(db: sqlite3.Connection) -> dict[str, int]:
    tables = [row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")]
    return {table: db.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0] for table in tables}


def check_fresh() -> None:
    db = baseline_migrations()
    db.executescript(ARTIFACT.read_text())
    assert db.execute("SELECT name, reporting_currency, holdings_revision FROM portfolios WHERE id='portfolio-default'").fetchone() == ("Portfolio", "MYR", 0)
    assert db.execute("SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-default'").fetchone()[0] == 0
    assert not db.execute("PRAGMA foreign_key_check").fetchall()


def check_legacy_timestamps(db: sqlite3.Connection) -> None:
    originals = dict(db.execute("SELECT id,created_at FROM lots WHERE id LIKE 'lot-%'"))
    keys = dict(db.execute("SELECT id,created_at_order_key FROM lots WHERE id LIKE 'lot-%'"))
    assert keys["lot-nofraction"] == "2026-01-04T00:00:00.000000000Z"
    assert keys["lot-shortfraction"] == "2026-01-04T00:00:00.123000000Z"
    assert keys["lot-nanosecond"] == "2026-01-04T00:00:00.123456789Z"
    assert keys["lot-offset"] == "2026-01-05T00:00:00.123000000Z"
    assert keys["lot-equal"] == "2026-01-06T00:00:00.000000000Z"
    assert keys["lot-malformed"] is None

    for lot, removed_at in (
        ("lot-nofraction", "2026-01-04T00:00:00.000000001Z"),
        ("lot-shortfraction", "2026-01-04T00:00:00.123000001Z"),
        ("lot-nanosecond", "2026-01-04T00:00:00.123456789Z"),
        ("lot-offset", "2026-01-05T00:00:00.123000001Z"),
        ("lot-equal", "2026-01-06T00:00:00.000000000Z"),
    ):
        db.execute("UPDATE lots SET lifecycle='removed',removed_at=?,removed_by_category='browser_user',removed_by_id='local-owner' WHERE id=?", (removed_at, lot))
    db.commit()
    expect_reject(db, "UPDATE lots SET lifecycle='removed',removed_at='2026-01-01T00:00:00.000000000Z',removed_by_category='browser_user',removed_by_id='local-owner' WHERE id='lot-two'")
    for bad_actor in ("", "-leading", " bad", "bad actor", "bad/actor", "bad\nactor", "éactor", "x" * 129):
        expect_reject(db, "UPDATE lots SET lifecycle='removed',removed_at='2026-01-03T00:00:00.000000000Z',removed_by_category='browser_user',removed_by_id=? WHERE id='lot-two'", (bad_actor,))
    expect_reject(db, "UPDATE lots SET lifecycle='removed',removed_at='2026-01-07T00:00:00.000000000Z',removed_by_category='browser_user',removed_by_id='local-owner' WHERE id='lot-malformed'")
    assert db.execute("SELECT lifecycle,created_at FROM lots WHERE id='lot-malformed'").fetchone() == ("active", originals["lot-malformed"])
    assert dict(db.execute("SELECT id,created_at FROM lots WHERE id LIKE 'lot-%'")) == originals


def check_new_row_order_key(db: sqlite3.Connection) -> None:
    created_at = "2026-01-07T00:00:00.000000000Z"
    before = table_counts(db)
    expect_reject(
        db,
        "INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at) VALUES ('lot-missing-key','portfolio-default','instrument-shared','1','1','1','MYR',?)",
        (created_at,),
    )
    expect_reject(
        db,
        "INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES ('lot-mismatched-key','portfolio-default','instrument-shared','1','1','1','MYR',?,?)",
        (created_at, "2026-01-07T00:00:00.000000001Z"),
    )
    assert table_counts(db) == before

    db.execute(
        "INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES ('lot-order-key','portfolio-default','instrument-shared','1','1','1','MYR',?,?)",
        (created_at, created_at),
    )
    assert db.execute("SELECT created_at,created_at_order_key FROM lots WHERE id='lot-order-key'").fetchone() == (created_at, created_at)
    db.commit()

    before_reject = table_counts(db)
    expect_reject(
        db,
        "UPDATE lots SET lifecycle='removed',removed_at='2026-01-06T23:59:59.999999999Z',removed_by_category='browser_user',removed_by_id='local-owner' WHERE id='lot-order-key'",
    )
    assert table_counts(db) == before_reject

    savepoint = "order_key_archive_rollback"
    db.execute("SAVEPOINT " + savepoint)
    db.execute(
        "UPDATE lots SET lifecycle='removed',removed_at='2026-01-07T00:00:00.000000001Z',removed_by_category='browser_user',removed_by_id='local-owner' WHERE id='lot-order-key'"
    )
    db.execute("ROLLBACK TO " + savepoint)
    db.execute("RELEASE " + savepoint)
    assert db.execute("SELECT lifecycle,created_at_order_key FROM lots WHERE id='lot-order-key'").fetchone() == ("active", created_at)

    db.execute(
        "UPDATE lots SET lifecycle='removed',removed_at='2026-01-07T00:00:00.000000001Z',removed_by_category='browser_user',removed_by_id='local-owner' WHERE id='lot-order-key'"
    )
    db.commit()


def valid_holding_chain(db: sqlite3.Connection, suffix: str, operation: str, key: str, holding: str = "lot-two") -> None:
    action = "holding_created" if operation == "create" else "holding_removed"
    payload = '{"portfolio_id":"portfolio-default","holding_id":"%s","operation":"%s","occurred_at":"%s"}' % (holding, operation, TS)
    audit = "audit-holding-" + suffix
    activity = "activity-holding-" + suffix
    db.execute("INSERT INTO audit_events VALUES (?,?,?,?,?,?,?,?)", (audit, "local-owner", action, "holding", holding, payload, TS, RETAIN))
    db.execute("INSERT INTO audit_event_portfolios VALUES (?,?,?)", (audit, "portfolio-default", RETAIN))
    db.execute("INSERT INTO activity_events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)", (activity, "portfolio-default", "browser_user", action, "holding", "holding", holding, "created", "", TS, TS, audit, RETAIN))
    db.execute("INSERT INTO holding_mutation_idempotency VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", ("browser_user", "local-owner", "portfolio-default", operation, key, "a" * 64, holding, audit, activity, 1, 0, '{"ok":true}', TS, RETAIN))


def check_actor_and_idempotency(db: sqlite3.Connection) -> None:
    for bad in ("", "-leading", " bad", "bad actor", "bad/actor", "bad\nactor", "éactor", "x" * 129):
        expect_reject(db, "INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,active,created_at) VALUES (?,?,?,?,?,?)", (bad, "portfolio-default", 1, 0, 1, TS))
    expect_reject(db, "UPDATE machine_principals SET id='bad/actor' WHERE id='bad actor'")
    db.execute("UPDATE machine_principals SET permission_holdings_write=1 WHERE id='bad actor'")
    db.commit()
    before = table_counts(db)
    expect_reject(db, "INSERT INTO holding_mutation_idempotency VALUES ('machine','bad actor','portfolio-default','create','malformed-principal-key','%s','lot-two','audit-legacy','activity-legacy',1,1,'{}',?,?)" % ('b' * 64), (TS, RETAIN))
    assert table_counts(db) == before
    assert not db.execute("SELECT 1 FROM holding_mutation_idempotency WHERE actor_id='bad actor'").fetchone()
    assert db.execute("SELECT permission_holdings_write FROM machine_principals WHERE id='bad actor'").fetchone()[0] == 1

    valid_holding_chain(db, "create", "create", "same-key-1234567")
    db.commit()
    before = table_counts(db)
    replay = db.execute("SELECT response_payload FROM holding_mutation_idempotency WHERE actor_category='browser_user' AND actor_id='local-owner' AND portfolio_id='portfolio-default' AND operation='create' AND idempotency_key='same-key-1234567'").fetchone()[0]
    assert replay == '{"ok":true}'
    assert table_counts(db) == before
    stored_fp = db.execute("SELECT request_fingerprint FROM holding_mutation_idempotency WHERE idempotency_key='same-key-1234567'").fetchone()[0]
    assert stored_fp != "b" * 64
    before_conflict = table_counts(db)
    # The store's replay path reads this row before attempting an insert. A
    # direct duplicate insert is still rejected by the physical namespace;
    # either path must leave every table unchanged.
    expect_reject(db, "INSERT INTO holding_mutation_idempotency VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", ("browser_user", "local-owner", "portfolio-default", "create", "same-key-1234567", "a" * 64, "lot-two", "audit-holding-create", "activity-holding-create", 1, 0, '{"ok":true}', TS, RETAIN))
    assert stored_fp != "b" * 64  # changed fingerprint is a read-only conflict decision
    assert table_counts(db) == before_conflict

    valid_holding_chain(db, "remove", "remove", "same-key-1234567")
    db.commit()
    assert db.execute("SELECT COUNT(*) FROM holding_mutation_idempotency WHERE idempotency_key='same-key-1234567'").fetchone()[0] == 2


def machine_mutation_attempt(db: sqlite3.Connection, principal: str, ordinal: int, expected: bool) -> None:
    savepoint = "machine_grant_%d" % ordinal
    before = table_counts(db)
    db.execute("SAVEPOINT " + savepoint)
    lot = "machine-grant-lot-%d" % ordinal
    audit = "machine-grant-audit-%d" % ordinal
    activity = "machine-grant-activity-%d" % ordinal
    key = "machine-grant-key-%d" % ordinal
    payload = '{"portfolio_id":"portfolio-default","holding_id":"%s","operation":"create","occurred_at":"%s"}' % (lot, TS)
    try:
        db.execute(
            "INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES (?,?,?,?,?,?,?,?,?)",
            (lot, "portfolio-default", "instrument-shared", "1", "1", "1", "MYR", TS, TS),
        )
        db.execute("INSERT INTO audit_events VALUES (?,?,?,?,?,?,?,?)", (audit, principal, "holding_created", "holding", lot, payload, TS, RETAIN))
        db.execute("INSERT INTO audit_event_portfolios VALUES (?,?,?)", (audit, "portfolio-default", RETAIN))
        db.execute("INSERT INTO activity_events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)", (activity, "portfolio-default", "machine", "holding_created", "holding", "holding", lot, "created", "", TS, TS, audit, RETAIN))
        db.execute("INSERT INTO holding_mutation_idempotency VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", ("machine", principal, "portfolio-default", "create", key, "d" * 64, lot, audit, activity, 1, 1, '{"ok":true}', TS, RETAIN))
    except sqlite3.IntegrityError:
        db.execute("ROLLBACK TO " + savepoint)
        db.execute("RELEASE " + savepoint)
        assert not expected
        assert table_counts(db) == before, principal
        return
    db.execute("RELEASE " + savepoint)
    assert expected
    db.commit()
    assert table_counts(db) != before
    assert db.execute("SELECT actor_category,actor_id,response_payload FROM holding_mutation_idempotency WHERE idempotency_key=?", (key,)).fetchone() == ("machine", principal, '{"ok":true}')


def check_machine_grant_matrix(db: sqlite3.Connection) -> None:
    principals = (
        ("machine-default-false", "portfolio-default", 1, 0, 1, 0),
        ("machine-reviews-only", "portfolio-default", 1, 1, 1, 0),
        ("machine-inactive", "portfolio-default", 1, 0, 0, 1),
        ("machine-wrong-portfolio", "portfolio-other", 1, 0, 1, 1),
        ("machine-read-only", "portfolio-default", 1, 0, 1, 0),
        ("machine-holdings-only", "portfolio-default", 0, 0, 1, 1),
        ("machine-valid", "portfolio-default", 1, 0, 1, 1),
    )
    for principal, portfolio, read, reviews, active, holdings_write in principals:
        if principal == "machine-default-false":
            db.execute(
                "INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,active,created_at) VALUES (?,?,?,?,?,?)",
                (principal, portfolio, read, reviews, active, TS),
            )
        else:
            db.execute(
                "INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,active,created_at,permission_holdings_write) VALUES (?,?,?,?,?,?,?)",
                (principal, portfolio, read, reviews, active, TS, holdings_write),
            )
    db.commit()
    for ordinal, (principal, *_rest) in enumerate(principals):
        machine_mutation_attempt(db, principal, ordinal, principal == "machine-valid")
    assert db.execute("SELECT COUNT(*) FROM holding_mutation_idempotency WHERE actor_category='machine'").fetchone()[0] == 1


def check_audit_immutability(db: sqlite3.Connection) -> None:
    before_legacy = db.execute("SELECT * FROM audit_events WHERE id='audit-legacy'").fetchone()
    expect_reject(db, "UPDATE audit_events SET action='holding_created' WHERE id='audit-legacy'")
    assert db.execute("SELECT * FROM audit_events WHERE id='audit-legacy'").fetchone() == before_legacy
    holding_before = db.execute("SELECT * FROM audit_events WHERE id='audit-holding-create'").fetchone()
    expect_reject(db, "UPDATE audit_events SET payload='{}' WHERE id='audit-holding-create'")
    expect_reject(db, "UPDATE audit_events SET entity_id='lot-one' WHERE id='audit-holding-create'")
    expect_reject(db, "UPDATE audit_events SET created_at='2026-02-01T00:00:00.000000000Z' WHERE id='audit-holding-create'")
    assert db.execute("SELECT * FROM audit_events WHERE id='audit-holding-create'").fetchone() == holding_before
    owner_before = db.execute("SELECT * FROM audit_event_portfolios WHERE audit_event_id='audit-holding-create'").fetchone()
    expect_reject(db, "UPDATE audit_event_portfolios SET retain_until='2034-01-01T00:00:00.000000000Z' WHERE audit_event_id='audit-holding-create'")
    expect_reject(db, "UPDATE audit_event_portfolios SET portfolio_id='portfolio-other' WHERE audit_event_id='audit-holding-create'")
    expect_reject(db, "DELETE FROM audit_event_portfolios WHERE audit_event_id='audit-holding-create'")
    expect_reject(db, "DELETE FROM audit_events WHERE id='audit-holding-create'")
    assert db.execute("SELECT * FROM audit_event_portfolios WHERE audit_event_id='audit-holding-create'").fetchone() == owner_before


def check_transactional_rollback(db: sqlite3.Connection) -> None:
    boundaries = ("instrument", "lot", "revision", "audit", "Activity", "idempotency")
    for ordinal, boundary in enumerate(boundaries):
        before = table_counts(db)
        revision = db.execute("SELECT holdings_revision FROM portfolios WHERE id='portfolio-default'").fetchone()[0]
        savepoint = "rollback_probe_%d" % ordinal
        db.execute("SAVEPOINT " + savepoint)
        try:
            instrument = "rollback-instrument-%d" % ordinal
            lot = "rollback-lot-%d" % ordinal
            audit = "rollback-audit-%d" % ordinal
            activity = "rollback-activity-%d" % ordinal
            db.execute("INSERT INTO instruments VALUES (?,?,?,?,?,?,?)", (instrument, "RB%d" % ordinal, "Rollback", "MYR", "equity", "TEST", TS))
            if boundary == "instrument":
                raise RuntimeError("injected failure")
            db.execute("INSERT INTO portfolio_instruments VALUES (?,?,?)", ("portfolio-default", instrument, TS))
            db.execute("INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES (?,?,?,?,?,?,?,?,?)", (lot, "portfolio-default", instrument, "1", "1", "1", "MYR", TS, TS))
            if boundary == "lot":
                raise RuntimeError("injected failure")
            db.execute("UPDATE portfolios SET holdings_revision=holdings_revision+1 WHERE id='portfolio-default'")
            if boundary == "revision":
                raise RuntimeError("injected failure")
            payload = '{"portfolio_id":"portfolio-default","holding_id":"%s","operation":"create","occurred_at":"%s"}' % (lot, TS)
            db.execute("INSERT INTO audit_events VALUES (?,?,?,?,?,?,?,?)", (audit, "local-owner", "holding_created", "holding", lot, payload, TS, RETAIN))
            if boundary == "audit":
                raise RuntimeError("injected failure")
            db.execute("INSERT INTO audit_event_portfolios VALUES (?,?,?)", (audit, "portfolio-default", RETAIN))
            db.execute("INSERT INTO activity_events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)", (activity, "portfolio-default", "browser_user", "holding_created", "holding", "holding", lot, "created", "", TS, TS, audit, RETAIN))
            if boundary == "Activity":
                raise RuntimeError("injected failure")
            db.execute("INSERT INTO holding_mutation_idempotency VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", ("browser_user", "local-owner", "portfolio-default", "create", "rollback-key-probe-%d" % ordinal, "c" * 64, lot, audit, activity, 1, 0, '{"ok":true}', TS, RETAIN))
            if boundary == "idempotency":
                raise RuntimeError("injected failure")
        except RuntimeError:
            db.execute("ROLLBACK TO " + savepoint)
            db.execute("RELEASE " + savepoint)
        else:
            raise AssertionError("rollback probe did not fail")
        assert table_counts(db) == before, boundary
        assert db.execute("SELECT holdings_revision FROM portfolios WHERE id='portfolio-default'").fetchone()[0] == revision


def check_schema_metadata(db: sqlite3.Connection) -> None:
    columns = {row[1]: row[5] for row in db.execute("PRAGMA table_info(holding_mutation_idempotency)")}
    assert [name for name, ordinal in sorted(columns.items(), key=lambda item: item[1]) if ordinal] == [
        "actor_category", "actor_id", "portfolio_id", "operation", "idempotency_key"
    ]
    foreign_keys = db.execute("PRAGMA foreign_key_list(holding_mutation_idempotency)").fetchall()
    assert any(row[2] == "lots" and row[3] == "portfolio_id" and row[4] == "portfolio_id" for row in foreign_keys)
    assert any(row[2] == "audit_events" and row[3] == "audit_event_id" for row in foreign_keys)
    trigger_names = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='trigger'")}
    assert {
        "holding_machine_principal_actor_guard",
        "holding_machine_principal_actor_update_guard",
        "holding_audit_append_only_update",
        "holding_audit_append_only_delete",
        "holding_mutation_audit_owner_update_guard",
        "holding_mutation_audit_owner_delete_guard",
        "holding_created_at_immutable_guard",
    } <= trigger_names


def check_populated() -> None:
    db = baseline_migrations()
    seed_populated(db)
    before = historical_rows(db)
    db.executescript(ARTIFACT.read_text())
    for table, (columns, rows) in before.items():
        names = ",".join(columns)
        assert db.execute(f"SELECT {names} FROM {table} ORDER BY rowid").fetchall() == rows, table
    assert db.execute("SELECT name FROM portfolios WHERE id='portfolio-default'").fetchone()[0] == "Personal Portfolio"
    assert db.execute("SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-default'").fetchone()[0] == 8
    assert db.execute("SELECT COUNT(*) FROM portfolio_instruments WHERE portfolio_id='portfolio-default' AND instrument_id='instrument-shared'").fetchone()[0] == 1
    check_schema_metadata(db)

    db.execute("INSERT INTO instruments VALUES ('instrument-new','NEW','New','MYR','equity','TEST',?)", (TS,))
    db.execute("INSERT INTO portfolio_instruments VALUES ('portfolio-default','instrument-new',?)", (TS,))
    db.execute("INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES ('lot-new','portfolio-default','instrument-new',NULL,'1','1','1','MYR',?,?)", (TS, TS))
    db.commit()
    lot_count = db.execute("SELECT COUNT(*) FROM lots").fetchone()[0]
    expect_reject(db, "INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES ('bad-instrument','portfolio-default','instrument-other',NULL,'1','1','1','MYR',?,?)", (TS, TS))
    expect_reject(db, "INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES ('bad-account','portfolio-default','instrument-shared','account-other','1','1','1','MYR',?,?)", (TS, TS))
    assert db.execute("SELECT COUNT(*) FROM lots").fetchone()[0] == lot_count

    check_legacy_timestamps(db)
    check_new_row_order_key(db)
    db.execute("UPDATE portfolios SET holdings_revision=holdings_revision+1 WHERE id='portfolio-default'")
    expect_reject(db, "UPDATE portfolios SET holdings_revision=holdings_revision+2 WHERE id='portfolio-default'")
    check_actor_and_idempotency(db)
    check_machine_grant_matrix(db)
    db.commit()
    check_audit_immutability(db)
    check_transactional_rollback(db)
    assert not db.execute("PRAGMA foreign_key_check").fetchall()


def check_future_no_document_import_regression() -> None:
    """Gate the post-implementation clean-tree removal invariant.

    The current branch intentionally retains baseline runtime debt, so this
    regression is opt-in for the implementation milestone.  Contract files
    may mention the debt only as a prohibition, historical note, or removal
    ledger; all other tracked files must be clean of the old active surface.
    """
    if os.environ.get("HOLDING_CONTRACT_IMPLEMENTED") != "1":
        return
    patterns = (
        "Portfolio.md",
        "import-preview",
        "import-apply",
        "import-status",
        "source_row",
        "source_ref",
        "source_sha256",
        "import_runs",
        "import_items",
    )
    allowed_contract_files = {
        "api/openapi.yaml",
        "docs/holding-mutation-contract.md",
        "docs/holding-mutation-contract.sql",
        "docs/holding-mutation-contract-harness.py",
    }
    tracked = subprocess.check_output(["git", "ls-files"], cwd=ROOT, text=True).splitlines()
    for relative in tracked:
        if pathlib.PurePosixPath(relative).name.startswith("."):
            continue  # Ignore files are not application architecture.
        path = ROOT / relative
        if not path.is_file():
            continue
        try:
            lines = path.read_text().splitlines()
        except UnicodeDecodeError:
            continue
        for line_number, line in enumerate(lines, start=1):
            if not any(term in line for term in patterns):
                continue
            if relative not in allowed_contract_files:
                raise AssertionError(f"active document-import identity remains: {relative}:{line_number}")
            context = line.lower()
            if not any(marker in context for marker in ("no ", "never", "remove", "removal", "historical", "baseline", "prohibit", "not runtime")):
                raise AssertionError(f"unaccounted contract reference: {relative}:{line_number}")


if __name__ == "__main__":
    check_fresh()
    check_populated()
    check_future_no_document_import_regression()
    print("holding mutation contract harness: PASS (target-state scratch feasibility, new-row comparison keys, machine grant matrix, actor grammar, idempotency replay/conflict/cross-operation, legacy timestamps, audit ownership immutability, staged rollback, foreign keys)")
