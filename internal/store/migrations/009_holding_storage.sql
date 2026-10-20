-- Canonical holding lifecycle foundation. This migration is intentionally
-- append-only: legacy provenance columns remain for historical evidence, while
-- current reads use lifecycle and canonical portfolio associations.
PRAGMA foreign_keys = ON;
PRAGMA defer_foreign_keys = ON;

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
ALTER TABLE lots ADD COLUMN created_at_order_key TEXT NULL;

-- The original created_at value is never rewritten. Parseable RFC3339 values
-- get an independent UTC ordering key; malformed historical values stay NULL.
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

CREATE TRIGGER holding_revision_guard
BEFORE UPDATE OF holdings_revision ON portfolios
BEGIN
    SELECT RAISE(ABORT, 'holding revision must increase by one')
    WHERE NEW.holdings_revision <> OLD.holdings_revision + 1;
END;

-- Legacy callers that insert a lot directly are upgraded safely by creating
-- the one portfolio/instrument association before scope validation. The
-- application mutation path remains responsible for the revision transaction.
CREATE TRIGGER holding_instrument_scope_guard_insert
BEFORE INSERT ON lots
BEGIN
    INSERT OR IGNORE INTO portfolio_instruments(portfolio_id, instrument_id, created_at)
    SELECT NEW.portfolio_id, NEW.instrument_id, NEW.created_at
    WHERE EXISTS (SELECT 1 FROM portfolios WHERE id=NEW.portfolio_id)
      AND EXISTS (SELECT 1 FROM instruments WHERE id=NEW.instrument_id);
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
CREATE TRIGGER holding_account_scope_guard_insert
BEFORE INSERT ON lots
WHEN NEW.account_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'holding account is outside portfolio')
    WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE id=NEW.account_id AND portfolio_id=NEW.portfolio_id);
END;
CREATE TRIGGER holding_account_scope_guard_update
BEFORE UPDATE OF portfolio_id, account_id ON lots
WHEN NEW.account_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'holding account is outside portfolio')
    WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE id=NEW.account_id AND portfolio_id=NEW.portfolio_id);
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
    WHERE strftime('%Y-%m-%dT%H:%M:%S', NEW.created_at) IS NULL
       OR NEW.created_at_order_key IS NULL
       OR NEW.created_at_order_key IS NOT NEW.created_at;
    SELECT RAISE(ABORT, 'invalid active holding removal fields')
    WHERE NEW.lifecycle='active' AND (NEW.removed_at IS NOT NULL OR NEW.removed_by_category IS NOT NULL OR NEW.removed_by_id IS NOT NULL);
    SELECT RAISE(ABORT, 'invalid removed holding removal fields')
    WHERE NEW.lifecycle='removed' AND (NEW.removed_at IS NULL OR NEW.removed_by_category IS NULL OR NEW.removed_by_id IS NULL);
END;
CREATE TRIGGER holding_lifecycle_guard_update
BEFORE UPDATE OF lifecycle, removed_at, removed_by_category, removed_by_id ON lots
BEGIN
    SELECT RAISE(ABORT, 'holding lifecycle can only move active to removed')
    WHERE OLD.lifecycle <> 'active' OR NEW.lifecycle <> 'removed';
    SELECT RAISE(ABORT, 'removed holding requires actor')
    WHERE NEW.removed_at IS NULL OR NEW.removed_by_category IS NULL OR NEW.removed_by_id IS NULL
       OR NEW.created_at_order_key IS NULL OR NEW.removed_at < NEW.created_at_order_key;
END;
CREATE TRIGGER holding_archival_immutable_guard
BEFORE UPDATE ON lots
WHEN OLD.lifecycle='removed' OR (OLD.lifecycle='active' AND NEW.lifecycle='removed' AND (
    NEW.portfolio_id IS NOT OLD.portfolio_id OR NEW.instrument_id IS NOT OLD.instrument_id OR
    NEW.account_id IS NOT OLD.account_id OR NEW.quantity IS NOT OLD.quantity OR
    NEW.unit_cost IS NOT OLD.unit_cost OR NEW.cost_basis IS NOT OLD.cost_basis OR
    NEW.currency IS NOT OLD.currency OR NEW.acquisition_date IS NOT OLD.acquisition_date OR
    NEW.created_at IS NOT OLD.created_at OR NEW.created_at_order_key IS NOT OLD.created_at_order_key))
BEGIN SELECT RAISE(ABORT, 'holding fields are immutable'); END;
CREATE TRIGGER holding_created_at_immutable_guard
BEFORE UPDATE OF created_at, created_at_order_key ON lots
WHEN NEW.created_at IS NOT OLD.created_at OR NEW.created_at_order_key IS NOT OLD.created_at_order_key
BEGIN SELECT RAISE(ABORT, 'holding creation timestamp is immutable'); END;
