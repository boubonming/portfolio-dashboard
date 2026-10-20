-- CONTRACT ARTIFACT ONLY: do not place under internal/store/migrations.
-- Execute once, inside one transaction, after runtime migrations 001-008.
-- The future runtime migration must preserve the operation/trigger ordering in
-- docs/holding-mutation-contract.md and verify every object listed there.
-- NO IMPORT SURFACE INVARIANT: this artifact initializes only canonical
-- database state and constrains mutation-only holding provenance.
PRAGMA foreign_keys = ON;

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

CREATE TABLE portfolio_instruments (
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (portfolio_id, instrument_id)
);
INSERT INTO portfolio_instruments(portfolio_id, instrument_id, created_at)
SELECT DISTINCT portfolio_id, instrument_id, created_at FROM lots;
CREATE INDEX idx_portfolio_instruments_instrument
    ON portfolio_instruments(instrument_id, portfolio_id);

CREATE INDEX idx_lots_active_portfolio
    ON lots(portfolio_id, lifecycle, id);
CREATE INDEX idx_lots_active_instrument
    ON lots(portfolio_id, instrument_id, lifecycle);
CREATE INDEX idx_lots_removed_at
    ON lots(portfolio_id, removed_at, id);

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
    actor_id TEXT NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 128),
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
    PRIMARY KEY (actor_category, actor_id, portfolio_id, idempotency_key),
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

CREATE TRIGGER holding_lifecycle_guard_insert
BEFORE INSERT ON lots
BEGIN
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
      AND length(NEW.removed_by_id) NOT BETWEEN 1 AND 128;
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
    WHERE length(NEW.removed_by_id) NOT BETWEEN 1 AND 128;
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
      NEW.created_at IS NOT OLD.created_at
  ))
BEGIN
    SELECT RAISE(ABORT, 'holding fields are immutable');
END;

CREATE TRIGGER holding_audit_append_only_update
BEFORE UPDATE ON audit_events
WHEN OLD.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'holding audit events are append-only');
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
    );
END;

CREATE TRIGGER holding_mutation_activity_scope_guard
BEFORE INSERT ON activity_events
WHEN NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'holding Activity must reference its audit')
    WHERE NEW.source_audit_event_id IS NULL
       OR NOT EXISTS (
           SELECT 1 FROM audit_event_portfolios p
           WHERE p.audit_event_id = NEW.source_audit_event_id
             AND p.portfolio_id = NEW.portfolio_id
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
SELECT 'empty_default_initialization', CASE WHEN
    (SELECT name FROM portfolios WHERE id = 'portfolio-default') = 'Portfolio'
    AND (SELECT reporting_currency FROM portfolios WHERE id = 'portfolio-default') = 'MYR'
    AND (SELECT holdings_revision FROM portfolios WHERE id = 'portfolio-default') = 0
    AND NOT EXISTS (SELECT 1 FROM lots WHERE portfolio_id = 'portfolio-default')
    AND NOT EXISTS (SELECT 1 FROM portfolio_instruments WHERE portfolio_id = 'portfolio-default')
    AND NOT EXISTS (SELECT 1 FROM accounts WHERE portfolio_id = 'portfolio-default')
    AND NOT EXISTS (SELECT 1 FROM snapshots WHERE portfolio_id = 'portfolio-default')
    THEN 1 ELSE 0 END;
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'authenticated_empty_overview_preconditions', CASE WHEN
    EXISTS (SELECT 1 FROM browser_principals WHERE id = 'local-owner')
    AND EXISTS (SELECT 1 FROM portfolios WHERE id = 'portfolio-default')
    AND NOT EXISTS (SELECT 1 FROM lots WHERE portfolio_id = 'portfolio-default')
    THEN 1 ELSE 0 END;

SAVEPOINT first_api_holding_probe;
INSERT INTO instruments(id, symbol, description, currency, asset_type, exchange, created_at)
VALUES ('__holding_contract_probe_instrument', 'CONTRACT-PROBE', 'Contract probe', 'MYR', 'equity', 'TEST', '1970-01-01T00:00:00.000000000Z');
INSERT INTO portfolio_instruments(portfolio_id, instrument_id, created_at)
VALUES ('portfolio-default', '__holding_contract_probe_instrument', '1970-01-01T00:00:00.000000000Z');
INSERT INTO lots(id, portfolio_id, instrument_id, account_id, quantity, unit_cost, cost_basis, currency, acquisition_date, created_at)
VALUES ('__holding_contract_probe_lot', 'portfolio-default', '__holding_contract_probe_instrument', NULL, '1', '1', '1', 'MYR', NULL, '1970-01-01T00:00:00.000000000Z');
INSERT INTO _holding_contract_assertion(check_name, ok)
SELECT 'first_api_created_holding', CASE WHEN
    EXISTS (SELECT 1 FROM lots WHERE id = '__holding_contract_probe_lot' AND portfolio_id = 'portfolio-default' AND lifecycle = 'active')
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

-- The runtime operation must perform these statements in one transaction:
--
--   1. BEGIN IMMEDIATE; verify the authenticated principal and exact
--      portfolio revision witness (If-Match).
--   2. Check the scoped idempotency key/fingerprint. Replay the stored
--      response verbatim; a different fingerprint returns 409.
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
