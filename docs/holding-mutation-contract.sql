-- CONTRACT ARTIFACT ONLY: do not place under internal/store/migrations.
-- This is executable target-state feasibility evidence for the unmerged
-- contract branch, not Store.Migrate or shipped runtime migration evidence.
-- Execute once, inside one scratch-database transaction, after baseline SQL
-- migrations 001-008; no schema_migrations version is changed here.
-- The future runtime migration must preserve the operation/trigger ordering in
-- docs/holding-mutation-contract.md and verify every object listed there.
-- NO IMPORT SURFACE INVARIANT: this artifact initializes only canonical
-- database state and constrains authenticated application mutations. Direct
-- SQLite file access is trusted/outside HTTP authorization; the application
-- store operation remains the sole supported revision-changing path.
PRAGMA foreign_keys = ON;

CREATE TEMP TABLE _holding_contract_default_state (was_absent INTEGER NOT NULL);
INSERT INTO _holding_contract_default_state
SELECT CASE WHEN EXISTS (SELECT 1 FROM portfolios WHERE id = 'portfolio-default') THEN 0 ELSE 1 END;
INSERT INTO portfolios(id, name, reporting_currency, created_at, updated_at)
SELECT 'portfolio-default', 'Portfolio', 'MYR',
       '1970-01-01T00:00:00.000000000Z',
       '1970-01-01T00:00:00.000000000Z'
WHERE NOT EXISTS (SELECT 1 FROM portfolios WHERE id = 'portfolio-default');

ALTER TABLE portfolios ADD COLUMN holdings_revision INTEGER NOT NULL DEFAULT 0 CHECK (holdings_revision >= 0);
ALTER TABLE machine_principals ADD COLUMN permission_holdings_write INTEGER NOT NULL DEFAULT 0 CHECK (permission_holdings_write IN (0, 1));
ALTER TABLE lots ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'removed'));
ALTER TABLE lots ADD COLUMN removed_at TEXT NULL;
ALTER TABLE lots ADD COLUMN removed_by_category TEXT NULL;
ALTER TABLE lots ADD COLUMN removed_by_id TEXT NULL;
-- Preserve historical created_at bytes and compare through a separately
-- persisted canonical UTC key. Parseable legacy rows are backfilled below;
-- malformed rows remain NULL and cannot be archived until remediated.
ALTER TABLE lots ADD COLUMN created_at_order_key TEXT NULL;

-- Normalize RFC3339 offsets with SQLite date functions, but copy the original
-- fraction independently so nanosecond ordering is not reduced to millis.
UPDATE lots
SET created_at_order_key =
    strftime('%Y-%m-%dT%H:%M:%S', created_at) || '.' ||
    substr(
        (CASE WHEN substr(created_at, 20, 1) = '.' THEN
            substr(created_at, 21,
                length(created_at) - 20 - CASE WHEN substr(created_at, -1) = 'Z' THEN 1 ELSE 6 END)
          ELSE '' END) || '000000000', 1, 9
    ) || 'Z'
WHERE length(created_at) BETWEEN 20 AND 35
  AND substr(created_at, 5, 1) = '-'
  AND substr(created_at, 8, 1) = '-'
  AND substr(created_at, 11, 1) = 'T'
  AND substr(created_at, 14, 1) = ':'
  AND substr(created_at, 17, 1) = ':'
  AND substr(created_at, 12, 2) BETWEEN '00' AND '23'
  AND substr(created_at, 15, 2) BETWEEN '00' AND '59'
  AND substr(created_at, 18, 2) BETWEEN '00' AND '59'
  AND strftime('%Y-%m-%d', substr(created_at, 1, 10)) = substr(created_at, 1, 10)
  AND (substr(created_at, -1) = 'Z' OR substr(created_at, -6, 1) IN ('+', '-'))
  AND (substr(created_at, -1) = 'Z' OR
       (substr(created_at, -5, 2) BETWEEN '00' AND '23' AND
        substr(created_at, -2, 2) BETWEEN '00' AND '59'))
  AND (substr(created_at, 20, 1) <> '.' OR
       (length(created_at) - 20 - CASE WHEN substr(created_at, -1) = 'Z' THEN 1 ELSE 6 END) BETWEEN 0 AND 9)
  AND strftime('%Y-%m-%dT%H:%M:%S', created_at) IS NOT NULL
  AND (substr(created_at, 20, 1) <> '.' OR
       substr(created_at, 21,
           length(created_at) - 20 - CASE WHEN substr(created_at, -1) = 'Z' THEN 1 ELSE 6 END)
           NOT GLOB '*[^0-9]*');

CREATE TABLE portfolio_instruments (
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (portfolio_id, instrument_id)
);
INSERT INTO portfolio_instruments(portfolio_id, instrument_id, created_at)
SELECT portfolio_id, instrument_id, MIN(created_at)
FROM lots
GROUP BY portfolio_id, instrument_id;
CREATE INDEX idx_portfolio_instruments_instrument
    ON portfolio_instruments(instrument_id, portfolio_id);

CREATE INDEX idx_lots_active_portfolio
    ON lots(portfolio_id, lifecycle, id);
CREATE INDEX idx_lots_active_instrument
    ON lots(portfolio_id, instrument_id, lifecycle);
CREATE INDEX idx_lots_removed_at
    ON lots(portfolio_id, removed_at, id);

CREATE TRIGGER holding_instrument_scope_guard_insert
BEFORE INSERT ON lots
BEGIN
    SELECT RAISE(ABORT, 'holding instrument is outside portfolio')
    WHERE NOT EXISTS (
        SELECT 1 FROM portfolio_instruments
        WHERE portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    );
END;
CREATE TRIGGER holding_instrument_scope_guard_update
BEFORE UPDATE OF portfolio_id, instrument_id ON lots
BEGIN
    SELECT RAISE(ABORT, 'holding instrument is outside portfolio')
    WHERE NOT EXISTS (
        SELECT 1 FROM portfolio_instruments
        WHERE portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    );
END;

CREATE TRIGGER holding_revision_guard
BEFORE UPDATE OF holdings_revision ON portfolios
BEGIN
    SELECT RAISE(ABORT, 'holding revision must increase by one')
    WHERE NEW.holdings_revision <> OLD.holdings_revision + 1;
END;

CREATE TRIGGER holding_account_scope_guard_insert
BEFORE INSERT ON lots
WHEN NEW.account_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'holding account is outside portfolio')
    WHERE NOT EXISTS (
        SELECT 1 FROM accounts
        WHERE id = NEW.account_id AND portfolio_id = NEW.portfolio_id
    );
END;
CREATE TRIGGER holding_account_scope_guard_update
BEFORE UPDATE OF portfolio_id, account_id ON lots
WHEN NEW.account_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'holding account is outside portfolio')
    WHERE NOT EXISTS (
        SELECT 1 FROM accounts
        WHERE id = NEW.account_id AND portfolio_id = NEW.portfolio_id
    );
END;

CREATE TABLE holding_mutation_idempotency (
    actor_category TEXT NOT NULL CHECK (actor_category IN ('browser_user', 'machine')),
    actor_id TEXT NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 128
        AND actor_id NOT GLOB '*[^A-Za-z0-9._~-]*'
        AND substr(actor_id, 1, 1) GLOB '[A-Za-z0-9]'),
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    operation TEXT NOT NULL CHECK (operation IN ('create', 'remove')),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    request_fingerprint TEXT NOT NULL CHECK (length(request_fingerprint) = 64),
    holding_id TEXT NOT NULL,
    audit_event_id TEXT NOT NULL REFERENCES audit_events(id),
    activity_event_id TEXT NOT NULL REFERENCES activity_events(id),
    permission_portfolio_read INTEGER NOT NULL CHECK (permission_portfolio_read = 1),
    permission_holdings_write INTEGER NOT NULL CHECK (permission_holdings_write IN (0, 1)),
    response_payload TEXT NOT NULL CHECK (json_valid(response_payload) = 1 AND json_type(response_payload) = 'object'),
    created_at TEXT NOT NULL,
    retain_until TEXT NOT NULL,
    PRIMARY KEY (actor_category, actor_id, portfolio_id, operation, idempotency_key),
    UNIQUE (portfolio_id, holding_id, operation),
    FOREIGN KEY (portfolio_id, holding_id) REFERENCES lots(portfolio_id, id),
    CHECK (actor_category = 'machine' OR permission_holdings_write = 0),
    CHECK (actor_category = 'browser_user' OR permission_holdings_write = 1)
);
CREATE INDEX idx_holding_mutation_portfolio_created
    ON holding_mutation_idempotency(portfolio_id, created_at DESC, idempotency_key);
CREATE INDEX idx_holding_mutation_holding
    ON holding_mutation_idempotency(portfolio_id, holding_id, operation);

CREATE TRIGGER holding_mutation_idempotency_principal_guard
BEFORE INSERT ON holding_mutation_idempotency
BEGIN
    SELECT RAISE(ABORT, 'invalid browser mutation principal')
    WHERE NEW.actor_category = 'browser_user'
      AND (NEW.actor_id <> 'local-owner'
           OR NOT EXISTS (SELECT 1 FROM browser_principals WHERE id = NEW.actor_id));
    SELECT RAISE(ABORT, 'invalid machine mutation principal')
    WHERE NEW.actor_category = 'machine'
      AND NOT EXISTS (
          SELECT 1 FROM machine_principals
          WHERE id = NEW.actor_id
            AND portfolio_id = NEW.portfolio_id
            AND active = 1
            AND permission_portfolio_read = 1
            AND permission_holdings_write = NEW.permission_holdings_write
            AND permission_holdings_write = 1
      );
END;

CREATE TRIGGER holding_machine_principal_actor_guard
BEFORE INSERT ON machine_principals
BEGIN
    SELECT RAISE(ABORT, 'invalid machine principal id')
    WHERE length(NEW.id) NOT BETWEEN 1 AND 128
       OR NEW.id GLOB '*[^A-Za-z0-9._~-]*'
       OR substr(NEW.id, 1, 1) NOT GLOB '[A-Za-z0-9]';
END;
CREATE TRIGGER holding_machine_principal_actor_update_guard
BEFORE UPDATE OF id ON machine_principals
BEGIN
    SELECT RAISE(ABORT, 'invalid machine principal id')
    WHERE length(NEW.id) NOT BETWEEN 1 AND 128
       OR NEW.id GLOB '*[^A-Za-z0-9._~-]*'
       OR substr(NEW.id, 1, 1) NOT GLOB '[A-Za-z0-9]';
END;

CREATE TRIGGER holding_lifecycle_guard_insert
BEFORE INSERT ON lots
BEGIN
    SELECT RAISE(ABORT, 'invalid holding created_at')
    WHERE length(NEW.created_at) <> 30
       OR NEW.created_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
       OR substr(NEW.created_at, 20, 1) <> '.'
       OR strftime('%Y-%m-%d', julianday(substr(NEW.created_at, 1, 10))) <> substr(NEW.created_at, 1, 10)
       OR substr(NEW.created_at, 12, 2) NOT BETWEEN '00' AND '23'
       OR substr(NEW.created_at, 15, 2) NOT BETWEEN '00' AND '59'
       OR substr(NEW.created_at, 18, 2) NOT BETWEEN '00' AND '59'
       OR NEW.created_at < '1970-01-01T00:00:00.000000000Z';
    SELECT RAISE(ABORT, 'new holding requires matching created_at_order_key')
    WHERE NEW.created_at_order_key IS NULL
       OR NEW.created_at_order_key IS NOT NEW.created_at;
    SELECT RAISE(ABORT, 'invalid active holding removal fields')
    WHERE NEW.lifecycle = 'active'
      AND (NEW.removed_at IS NOT NULL OR NEW.removed_by_category IS NOT NULL OR NEW.removed_by_id IS NOT NULL);
    SELECT RAISE(ABORT, 'invalid removed holding removal fields')
    WHERE NEW.lifecycle = 'removed'
      AND (NEW.removed_at IS NULL OR NEW.removed_by_category IS NULL OR NEW.removed_by_id IS NULL);
    SELECT RAISE(ABORT, 'invalid holding removal actor')
    WHERE NEW.removed_by_category IS NOT NULL
      AND NEW.removed_by_category NOT IN ('browser_user', 'machine');
    SELECT RAISE(ABORT, 'invalid holding removal actor id')
    WHERE NEW.removed_by_id IS NOT NULL
      AND (length(NEW.removed_by_id) NOT BETWEEN 1 AND 128
           OR NEW.removed_by_id GLOB '*[^A-Za-z0-9._~-]*'
           OR substr(NEW.removed_by_id, 1, 1) NOT GLOB '[A-Za-z0-9]');
    SELECT RAISE(ABORT, 'invalid holding removal timestamp')
    WHERE NEW.removed_at IS NOT NULL
      AND (length(NEW.removed_at) <> 30
           OR NEW.removed_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
           OR substr(NEW.removed_at, 20, 1) <> '.'
           OR strftime('%Y-%m-%d', julianday(substr(NEW.removed_at, 1, 10))) <> substr(NEW.removed_at, 1, 10)
           OR substr(NEW.removed_at, 12, 2) NOT BETWEEN '00' AND '23'
           OR substr(NEW.removed_at, 15, 2) NOT BETWEEN '00' AND '59'
           OR substr(NEW.removed_at, 18, 2) NOT BETWEEN '00' AND '59'
           OR (NEW.created_at_order_key IS NOT NULL AND
               (length(NEW.created_at_order_key) <> 30 OR
                NEW.created_at_order_key NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'))
           OR NEW.created_at_order_key IS NULL
           OR NEW.removed_at < NEW.created_at_order_key);
END;
CREATE TRIGGER holding_lifecycle_guard_update
BEFORE UPDATE OF lifecycle, removed_at, removed_by_category, removed_by_id ON lots
BEGIN
    SELECT RAISE(ABORT, 'holding lifecycle can only move active to removed')
    WHERE OLD.lifecycle <> 'active' OR NEW.lifecycle <> 'removed';
    SELECT RAISE(ABORT, 'removed holding requires actor')
    WHERE NEW.removed_at IS NULL OR NEW.removed_by_category IS NULL OR NEW.removed_by_id IS NULL;
    SELECT RAISE(ABORT, 'invalid holding removal actor')
    WHERE NEW.removed_by_category NOT IN ('browser_user', 'machine');
    SELECT RAISE(ABORT, 'invalid holding removal actor id')
    WHERE length(NEW.removed_by_id) NOT BETWEEN 1 AND 128
       OR NEW.removed_by_id GLOB '*[^A-Za-z0-9._~-]*'
       OR substr(NEW.removed_by_id, 1, 1) NOT GLOB '[A-Za-z0-9]';
    SELECT RAISE(ABORT, 'invalid_holding_created_at')
    WHERE NEW.created_at_order_key IS NULL;
    SELECT RAISE(ABORT, 'invalid holding removal timestamp')
    WHERE length(NEW.removed_at) <> 30
       OR NEW.removed_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
       OR substr(NEW.removed_at, 20, 1) <> '.'
       OR strftime('%Y-%m-%d', julianday(substr(NEW.removed_at, 1, 10))) <> substr(NEW.removed_at, 1, 10)
       OR substr(NEW.removed_at, 12, 2) NOT BETWEEN '00' AND '23'
       OR substr(NEW.removed_at, 15, 2) NOT BETWEEN '00' AND '59'
       OR substr(NEW.removed_at, 18, 2) NOT BETWEEN '00' AND '59'
       OR (NEW.created_at_order_key IS NOT NULL AND
           (length(NEW.created_at_order_key) <> 30 OR
            NEW.created_at_order_key NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'))
       OR NEW.removed_at < NEW.created_at_order_key;
END;

CREATE TRIGGER holding_archival_immutable_guard
BEFORE UPDATE ON lots
WHEN OLD.lifecycle = 'removed'
  OR (OLD.lifecycle = 'active' AND NEW.lifecycle = 'removed' AND (
      NEW.id IS NOT OLD.id OR
      NEW.portfolio_id IS NOT OLD.portfolio_id OR
      NEW.instrument_id IS NOT OLD.instrument_id OR
      NEW.account_id IS NOT OLD.account_id OR
      NEW.quantity IS NOT OLD.quantity OR
      NEW.unit_cost IS NOT OLD.unit_cost OR
      NEW.cost_basis IS NOT OLD.cost_basis OR
      NEW.currency IS NOT OLD.currency OR
      NEW.acquisition_date IS NOT OLD.acquisition_date OR
      NEW.source_row IS NOT OLD.source_row OR
      NEW.source_ref IS NOT OLD.source_ref OR
      NEW.source_sha256 IS NOT OLD.source_sha256 OR
      NEW.created_at IS NOT OLD.created_at OR
      NEW.created_at_order_key IS NOT OLD.created_at_order_key
  ))
BEGIN
    SELECT RAISE(ABORT, 'holding fields are immutable');
END;
CREATE TRIGGER holding_created_at_immutable_guard
BEFORE UPDATE OF created_at, created_at_order_key ON lots
WHEN NEW.created_at IS NOT OLD.created_at
  OR NEW.created_at_order_key IS NOT OLD.created_at_order_key
BEGIN
    SELECT RAISE(ABORT, 'holding creation timestamp is immutable');
END;

CREATE TRIGGER holding_audit_append_only_update
BEFORE UPDATE ON audit_events
WHEN OLD.action IN ('holding_created', 'holding_removed')
  OR NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'holding audit events are append-only');
END;

CREATE TRIGGER holding_audit_actor_guard
BEFORE INSERT ON audit_events
WHEN NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'invalid holding audit actor')
    WHERE length(NEW.actor) NOT BETWEEN 1 AND 128
       OR NEW.actor GLOB '*[^A-Za-z0-9._~-]*'
       OR substr(NEW.actor, 1, 1) NOT GLOB '[A-Za-z0-9]';
END;
CREATE TRIGGER holding_audit_actor_update_guard
BEFORE UPDATE OF actor, action ON audit_events
WHEN NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'invalid holding audit actor')
    WHERE length(NEW.actor) NOT BETWEEN 1 AND 128
       OR NEW.actor GLOB '*[^A-Za-z0-9._~-]*'
       OR substr(NEW.actor, 1, 1) NOT GLOB '[A-Za-z0-9]';
END;

CREATE TRIGGER holding_audit_append_only_delete
BEFORE DELETE ON audit_events
WHEN OLD.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'holding audit events are append-only');
END;

CREATE TRIGGER holding_mutation_idempotency_append_only_update
BEFORE UPDATE ON holding_mutation_idempotency
BEGIN
    SELECT RAISE(ABORT, 'holding idempotency records are append-only');
END;
CREATE TRIGGER holding_mutation_idempotency_append_only_delete
BEFORE DELETE ON holding_mutation_idempotency
BEGIN
    SELECT RAISE(ABORT, 'holding idempotency records are append-only');
END;

CREATE TRIGGER holding_mutation_audit_scope_guard
BEFORE INSERT ON audit_event_portfolios
WHEN EXISTS (
    SELECT 1 FROM audit_events WHERE id = NEW.audit_event_id AND action IN ('holding_created', 'holding_removed')
)
BEGIN
    SELECT RAISE(ABORT, 'holding audit must identify its portfolio')
    WHERE NOT EXISTS (
        SELECT 1 FROM audit_events e
        WHERE e.id = NEW.audit_event_id
          AND json_valid(e.payload) = 1
          AND json_extract(e.payload, '$.portfolio_id') = NEW.portfolio_id
          AND e.entity_type = 'holding'
          AND json_extract(e.payload, '$.holding_id') = e.entity_id
          AND json_extract(e.payload, '$.operation') = CASE e.action WHEN 'holding_created' THEN 'create' WHEN 'holding_removed' THEN 'remove' END
          AND json_extract(e.payload, '$.occurred_at') = e.created_at
          AND NEW.retain_until = e.retain_until
    );
END;

CREATE TRIGGER holding_mutation_audit_owner_update_guard
BEFORE UPDATE ON audit_event_portfolios
WHEN EXISTS (
    SELECT 1 FROM audit_events
    WHERE id = OLD.audit_event_id AND action IN ('holding_created', 'holding_removed')
)
BEGIN
    SELECT RAISE(ABORT, 'holding audit ownership is append-only');
END;
CREATE TRIGGER holding_mutation_audit_owner_delete_guard
BEFORE DELETE ON audit_event_portfolios
WHEN EXISTS (
    SELECT 1 FROM audit_events
    WHERE id = OLD.audit_event_id AND action IN ('holding_created', 'holding_removed')
)
BEGIN
    SELECT RAISE(ABORT, 'holding audit ownership is append-only');
END;

CREATE TRIGGER holding_mutation_activity_scope_guard
BEFORE INSERT ON activity_events
WHEN NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'holding Activity must reference its audit')
    WHERE NEW.actor_category NOT IN ('browser_user', 'machine')
       OR NEW.source_audit_event_id IS NULL
       OR NEW.category <> 'holding'
       OR NEW.entity_type <> 'holding'
       OR NEW.entity_id IS NULL
       OR NOT EXISTS (
           SELECT 1 FROM audit_event_portfolios p
           JOIN audit_events a ON a.id = p.audit_event_id
           WHERE p.audit_event_id = NEW.source_audit_event_id
             AND p.portfolio_id = NEW.portfolio_id
             AND p.retain_until = NEW.retain_until
             AND a.action = NEW.action
             AND a.entity_type = NEW.entity_type
             AND a.entity_id = NEW.entity_id
             AND a.created_at = NEW.occurred_at
             AND json_extract(a.payload, '$.portfolio_id') = NEW.portfolio_id
             AND json_extract(a.payload, '$.holding_id') = NEW.entity_id
             AND json_extract(a.payload, '$.occurred_at') = NEW.occurred_at
       );
END;

CREATE TRIGGER holding_mutation_idempotency_ownership_guard
BEFORE INSERT ON holding_mutation_idempotency
BEGIN
    SELECT RAISE(ABORT, 'holding idempotency audit ownership mismatch')
    WHERE NOT EXISTS (
        SELECT 1
        FROM audit_events a
        JOIN audit_event_portfolios p ON p.audit_event_id = a.id AND p.portfolio_id = NEW.portfolio_id
        WHERE a.id = NEW.audit_event_id
          AND a.action = CASE NEW.operation WHEN 'create' THEN 'holding_created' WHEN 'remove' THEN 'holding_removed' END
          AND a.entity_type = 'holding'
          AND a.entity_id = NEW.holding_id
          AND a.created_at = NEW.created_at
          AND p.retain_until = NEW.retain_until
          AND json_extract(a.payload, '$.portfolio_id') = NEW.portfolio_id
          AND json_extract(a.payload, '$.holding_id') = NEW.holding_id
          AND json_extract(a.payload, '$.operation') = NEW.operation
          AND json_extract(a.payload, '$.occurred_at') = NEW.created_at
    );
    SELECT RAISE(ABORT, 'holding idempotency Activity ownership mismatch')
    WHERE NOT EXISTS (
        SELECT 1 FROM activity_events e
        WHERE e.id = NEW.activity_event_id
          AND e.portfolio_id = NEW.portfolio_id
          AND e.action = CASE NEW.operation WHEN 'create' THEN 'holding_created' WHEN 'remove' THEN 'holding_removed' END
          AND e.category = 'holding'
          AND e.entity_type = 'holding'
          AND e.entity_id = NEW.holding_id
          AND e.occurred_at = NEW.created_at
          AND e.source_audit_event_id = NEW.audit_event_id
          AND e.retain_until = NEW.retain_until
    );
END;

-- Executable contract checks. A failed check aborts the artifact through the
-- CHECK(ok=1) constraint. The probe holding intentionally omits all historical
-- external-document fields: this is the first API-created holding shape.
CREATE TEMP TABLE _holding_contract_assertion (
    check_name TEXT PRIMARY KEY,
    ok INTEGER NOT NULL CHECK(ok = 1)
);
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'fresh_empty_default_initialization', CASE WHEN
    (SELECT was_absent FROM _holding_contract_default_state) = 0
    OR ((SELECT name FROM portfolios WHERE id = 'portfolio-default') = 'Portfolio'
        AND (SELECT reporting_currency FROM portfolios WHERE id = 'portfolio-default') = 'MYR'
        AND (SELECT holdings_revision FROM portfolios WHERE id = 'portfolio-default') = 0
        AND NOT EXISTS (SELECT 1 FROM lots WHERE portfolio_id = 'portfolio-default')
        AND NOT EXISTS (SELECT 1 FROM portfolio_instruments WHERE portfolio_id = 'portfolio-default')
        AND NOT EXISTS (SELECT 1 FROM accounts WHERE portfolio_id = 'portfolio-default')
        AND NOT EXISTS (SELECT 1 FROM snapshots WHERE portfolio_id = 'portfolio-default'))
    THEN 1 ELSE 0 END;
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'existing_default_preserved', CASE WHEN
    (SELECT was_absent FROM _holding_contract_default_state) = 1
    OR EXISTS (SELECT 1 FROM portfolios WHERE id = 'portfolio-default')
    THEN 1 ELSE 0 END;
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'authenticated_empty_overview_preconditions', CASE WHEN
    EXISTS (SELECT 1 FROM browser_principals WHERE id = 'local-owner')
    AND EXISTS (SELECT 1 FROM portfolios WHERE id = 'portfolio-default')
    AND ((SELECT was_absent FROM _holding_contract_default_state) = 0
         OR NOT EXISTS (SELECT 1 FROM lots WHERE portfolio_id = 'portfolio-default'))
    THEN 1 ELSE 0 END;

SAVEPOINT first_api_holding_probe;
INSERT INTO instruments(id, symbol, description, currency, asset_type, exchange, created_at)
VALUES ('__holding_contract_probe_instrument', 'CONTRACT-PROBE', 'Contract probe', 'MYR', 'equity', 'TEST', '1970-01-01T00:00:00.000000000Z');
INSERT INTO portfolio_instruments(portfolio_id, instrument_id, created_at)
VALUES ('portfolio-default', '__holding_contract_probe_instrument', '1970-01-01T00:00:00.000000000Z');
INSERT INTO lots(id, portfolio_id, instrument_id, account_id, quantity, unit_cost, cost_basis, currency, acquisition_date, created_at, created_at_order_key)
VALUES ('__holding_contract_probe_lot', 'portfolio-default', '__holding_contract_probe_instrument', NULL, '1', '1', '1', 'MYR', NULL, '1970-01-01T00:00:00.000000000Z', '1970-01-01T00:00:00.000000000Z');
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'first_api_created_holding', CASE WHEN
    EXISTS (SELECT 1 FROM lots WHERE id = '__holding_contract_probe_lot' AND portfolio_id = 'portfolio-default' AND lifecycle = 'active')
    AND (SELECT created_at_order_key FROM lots WHERE id = '__holding_contract_probe_lot') = (SELECT created_at FROM lots WHERE id = '__holding_contract_probe_lot')
    AND NOT EXISTS (SELECT 1 FROM holding_mutation_idempotency WHERE holding_id = '__holding_contract_probe_lot')
    THEN 1 ELSE 0 END;
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'no_external_provenance_required', CASE WHEN
    EXISTS (SELECT 1 FROM lots WHERE id = '__holding_contract_probe_lot' AND portfolio_id = 'portfolio-default')
    THEN 1 ELSE 0 END;
RELEASE SAVEPOINT first_api_holding_probe;
DELETE FROM lots WHERE id = '__holding_contract_probe_lot';
DELETE FROM portfolio_instruments WHERE portfolio_id = 'portfolio-default' AND instrument_id = '__holding_contract_probe_instrument';
DELETE FROM instruments WHERE id = '__holding_contract_probe_instrument';
DROP TABLE _holding_contract_assertion;
DROP TABLE _holding_contract_default_state;

-- The runtime operation must perform these statements in one transaction:
--
--   1. BEGIN IMMEDIATE; verify the authenticated principal and exact
--      portfolio revision witness (If-Match).
--   2. Check the (actor_category, actor_id, portfolio_id, operation,
--      idempotency_key) namespace and canonical fingerprint. Replay the
--      stored response verbatim; a same-operation different fingerprint
--      returns 409. Create and remove namespaces are independent; route is
--      fingerprinted but is not a physical namespace column.
--   3. Resolve an existing portfolio_instruments row, or insert the bounded
--      inline instrument and its portfolio association, but never both inputs.
--   4. Insert the active lot, or UPDATE one active lot to lifecycle='removed'
--      with its actor witness. A zero-row removal is 404/409 according to the
--      request state; it never creates a second audit or Activity event.
--   5. UPDATE portfolios SET holdings_revision = holdings_revision + 1 and
--      capture the new value.
--   6. Insert one redacted audit_events row, one audit_event_portfolios owner,
--      one Activity row, and this idempotency row. No secret or raw request is
--      put in any payload.
--   7. COMMIT. Any error rolls back all seven steps, including the revision.
--
-- This comment is executable-design guidance, not a runtime implementation.
