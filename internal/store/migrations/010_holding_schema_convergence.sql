-- Forward-only convergence for databases that already recorded 001-009.
-- 001 and 002 remain immutable historical migrations; importer tables and
-- provenance indexes are retired here, never by rewriting applied history.
PRAGMA foreign_keys = ON;
PRAGMA defer_foreign_keys = ON;

DROP INDEX IF EXISTS idx_lots_source_provenance;
DROP INDEX IF EXISTS idx_import_items_source_row;
DROP INDEX IF EXISTS idx_import_runs_source_hash;
DROP INDEX IF EXISTS idx_audit_import_action;
DROP TABLE IF EXISTS import_items;
DROP TABLE IF EXISTS import_runs;

-- SQLite cannot remove columns in place. Rebuild the small set of tables that
-- reference lots while foreign keys remain enabled, preserving every
-- canonical field and historical row but not importer provenance columns.
DROP TRIGGER IF EXISTS holding_instrument_scope_guard_insert;
DROP TRIGGER IF EXISTS holding_instrument_scope_guard_update;
DROP TRIGGER IF EXISTS holding_account_scope_guard_insert;
DROP TRIGGER IF EXISTS holding_account_scope_guard_update;
DROP TRIGGER IF EXISTS holding_lifecycle_guard_insert;
DROP TRIGGER IF EXISTS holding_lifecycle_guard_update;
DROP TRIGGER IF EXISTS holding_archival_immutable_guard;
DROP TRIGGER IF EXISTS holding_created_at_immutable_guard;
DROP TRIGGER IF EXISTS portfolio_theses_scope_insert;
DROP TRIGGER IF EXISTS portfolio_theses_scope_update;
DROP TRIGGER IF EXISTS thesis_revisions_scope_insert;
DROP INDEX IF EXISTS idx_lots_portfolio;
DROP INDEX IF EXISTS idx_lots_portfolio_id_id;
DROP INDEX IF EXISTS idx_lots_active_portfolio;
DROP INDEX IF EXISTS idx_lots_active_instrument;
DROP INDEX IF EXISTS idx_lots_removed_at;

ALTER TABLE lots RENAME TO lots_legacy;
ALTER TABLE portfolio_theses RENAME TO portfolio_theses_legacy;
ALTER TABLE thesis_revisions RENAME TO thesis_revisions_legacy;
ALTER TABLE holding_mutation_idempotency RENAME TO holding_mutation_idempotency_legacy;

CREATE TABLE lots (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    account_id TEXT REFERENCES accounts(id),
    quantity TEXT NOT NULL,
    unit_cost TEXT,
    cost_basis TEXT,
    currency TEXT NOT NULL,
    acquisition_date TEXT,
    created_at TEXT NOT NULL,
    lifecycle TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'removed')),
    removed_at TEXT NULL,
    removed_by_category TEXT NULL,
    removed_by_id TEXT NULL,
    created_at_order_key TEXT NULL
);
INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,acquisition_date,created_at,lifecycle,removed_at,removed_by_category,removed_by_id,created_at_order_key)
SELECT id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,acquisition_date,created_at,lifecycle,removed_at,removed_by_category,removed_by_id,created_at_order_key
FROM lots_legacy;
CREATE UNIQUE INDEX idx_lots_portfolio_id_id ON lots(portfolio_id, id);

CREATE TABLE portfolio_theses (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    holding_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    summary TEXT NOT NULL,
    intended_holding_period TEXT NOT NULL,
    allocation_limit TEXT,
    reasons_to_hold TEXT NOT NULL,
    risks TEXT NOT NULL,
    reasons_to_sell TEXT NOT NULL,
    conditions_to_change TEXT NOT NULL,
    sources TEXT NOT NULL,
    current_revision INTEGER NOT NULL CHECK (current_revision >= 1),
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(portfolio_id, holding_id),
    UNIQUE(id, portfolio_id, holding_id, instrument_id),
    FOREIGN KEY (portfolio_id, holding_id) REFERENCES lots(portfolio_id, id)
);
INSERT INTO portfolio_theses SELECT * FROM portfolio_theses_legacy;

CREATE TABLE thesis_revisions (
    id TEXT PRIMARY KEY,
    thesis_id TEXT NOT NULL,
    portfolio_id TEXT NOT NULL,
    holding_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    summary TEXT NOT NULL,
    intended_holding_period TEXT NOT NULL,
    allocation_limit TEXT,
    reasons_to_hold TEXT NOT NULL,
    risks TEXT NOT NULL,
    reasons_to_sell TEXT NOT NULL,
    conditions_to_change TEXT NOT NULL,
    sources TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(thesis_id, revision),
    UNIQUE(thesis_id, request_hash),
    FOREIGN KEY (thesis_id, portfolio_id, holding_id, instrument_id)
        REFERENCES portfolio_theses(id, portfolio_id, holding_id, instrument_id)
);
INSERT INTO thesis_revisions SELECT * FROM thesis_revisions_legacy;

CREATE TABLE holding_mutation_idempotency (
    actor_category TEXT NOT NULL CHECK (actor_category IN ('browser_user', 'machine')),
    actor_id TEXT NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 128 AND actor_id NOT GLOB '*[^A-Za-z0-9._~-]*' AND substr(actor_id, 1, 1) GLOB '[A-Za-z0-9]'),
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
INSERT INTO holding_mutation_idempotency SELECT * FROM holding_mutation_idempotency_legacy;

DROP TABLE thesis_revisions_legacy;
DROP TABLE portfolio_theses_legacy;
DROP TABLE holding_mutation_idempotency_legacy;
DROP TABLE lots_legacy;

CREATE INDEX idx_lots_portfolio ON lots(portfolio_id);
CREATE INDEX idx_lots_active_portfolio ON lots(portfolio_id, lifecycle, id);
CREATE INDEX idx_lots_active_instrument ON lots(portfolio_id, instrument_id, lifecycle);
CREATE INDEX idx_lots_removed_at ON lots(portfolio_id, removed_at, id);
CREATE INDEX idx_portfolio_theses_portfolio ON portfolio_theses(portfolio_id);
CREATE INDEX idx_portfolio_theses_instrument ON portfolio_theses(portfolio_id, instrument_id);
CREATE INDEX idx_thesis_revisions_history ON thesis_revisions(thesis_id, revision DESC);
CREATE INDEX idx_thesis_revisions_portfolio ON thesis_revisions(portfolio_id, holding_id, revision DESC);
CREATE INDEX idx_holding_mutation_portfolio_created ON holding_mutation_idempotency(portfolio_id, created_at DESC, idempotency_key);
CREATE INDEX idx_holding_mutation_holding ON holding_mutation_idempotency(portfolio_id, holding_id, operation);

CREATE TRIGGER portfolio_theses_scope_insert
BEFORE INSERT ON portfolio_theses BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis holding scope mismatch') END;
END;
CREATE TRIGGER portfolio_theses_scope_update
BEFORE UPDATE OF portfolio_id, holding_id, instrument_id ON portfolio_theses BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis holding scope mismatch') END;
END;
CREATE TRIGGER thesis_revisions_scope_insert
BEFORE INSERT ON thesis_revisions BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis revision holding scope mismatch') END;
END;
CREATE TRIGGER portfolio_theses_append_only_delete
BEFORE DELETE ON portfolio_theses BEGIN SELECT RAISE(ABORT, 'theses are append-only'); END;
CREATE TRIGGER thesis_revisions_append_only_update
BEFORE UPDATE ON thesis_revisions BEGIN SELECT RAISE(ABORT, 'thesis revisions are append-only'); END;
CREATE TRIGGER thesis_revisions_append_only_delete
BEFORE DELETE ON thesis_revisions BEGIN SELECT RAISE(ABORT, 'thesis revisions are append-only'); END;

DROP TRIGGER IF EXISTS holding_instrument_scope_guard_insert;
CREATE TRIGGER holding_instrument_scope_guard_insert
BEFORE INSERT ON lots
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
    WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.account_id AND portfolio_id = NEW.portfolio_id);
END;
CREATE TRIGGER holding_account_scope_guard_update
BEFORE UPDATE OF portfolio_id, account_id ON lots
WHEN NEW.account_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'holding account is outside portfolio')
    WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.account_id AND portfolio_id = NEW.portfolio_id);
END;

DROP TRIGGER IF EXISTS holding_lifecycle_guard_insert;
CREATE TRIGGER holding_lifecycle_guard_insert
BEFORE INSERT ON lots
BEGIN
    SELECT RAISE(ABORT, 'invalid holding created_at')
    WHERE length(NEW.created_at) <> 30
       OR NEW.created_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
       OR strftime('%Y-%m-%d', julianday(substr(NEW.created_at, 1, 10))) <> substr(NEW.created_at, 1, 10)
       OR substr(NEW.created_at, 12, 2) NOT BETWEEN '00' AND '23'
       OR substr(NEW.created_at, 15, 2) NOT BETWEEN '00' AND '59'
       OR substr(NEW.created_at, 18, 2) NOT BETWEEN '00' AND '59'
       OR NEW.created_at < '1970-01-01T00:00:00.000000000Z';
    SELECT RAISE(ABORT, 'new holding requires matching created_at_order_key')
    WHERE NEW.created_at_order_key IS NULL OR NEW.created_at_order_key IS NOT NEW.created_at;
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
           OR strftime('%Y-%m-%d', julianday(substr(NEW.removed_at, 1, 10))) <> substr(NEW.removed_at, 1, 10)
           OR substr(NEW.removed_at, 12, 2) NOT BETWEEN '00' AND '23'
           OR substr(NEW.removed_at, 15, 2) NOT BETWEEN '00' AND '59'
           OR substr(NEW.removed_at, 18, 2) NOT BETWEEN '00' AND '59'
           OR NEW.created_at_order_key IS NULL
           OR NEW.removed_at < NEW.created_at_order_key);
END;

DROP TRIGGER IF EXISTS holding_lifecycle_guard_update;
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
    SELECT RAISE(ABORT, 'invalid holding removal timestamp')
    WHERE length(NEW.removed_at) <> 30
       OR NEW.removed_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
       OR strftime('%Y-%m-%d', julianday(substr(NEW.removed_at, 1, 10))) <> substr(NEW.removed_at, 1, 10)
       OR substr(NEW.removed_at, 12, 2) NOT BETWEEN '00' AND '23'
       OR substr(NEW.removed_at, 15, 2) NOT BETWEEN '00' AND '59'
       OR substr(NEW.removed_at, 18, 2) NOT BETWEEN '00' AND '59'
       OR NEW.created_at_order_key IS NULL
       OR NEW.removed_at < NEW.created_at_order_key;
END;

CREATE TRIGGER holding_mutation_idempotency_principal_guard
BEFORE INSERT ON holding_mutation_idempotency
BEGIN
    SELECT RAISE(ABORT, 'invalid browser mutation principal')
    WHERE NEW.actor_category = 'browser_user'
      AND (NEW.actor_id <> 'local-owner' OR NOT EXISTS (SELECT 1 FROM browser_principals WHERE id = NEW.actor_id));
    SELECT RAISE(ABORT, 'invalid machine mutation principal')
    WHERE NEW.actor_category = 'machine'
      AND NOT EXISTS (
          SELECT 1 FROM machine_principals
          WHERE id = NEW.actor_id AND portfolio_id = NEW.portfolio_id AND active = 1
            AND permission_portfolio_read = 1 AND permission_holdings_write = 1
            AND NEW.permission_holdings_write = 1
      );
END;

CREATE TRIGGER holding_mutation_idempotency_append_only_update
BEFORE UPDATE ON holding_mutation_idempotency
BEGIN SELECT RAISE(ABORT, 'holding idempotency records are append-only'); END;
CREATE TRIGGER holding_mutation_idempotency_append_only_delete
BEFORE DELETE ON holding_mutation_idempotency
BEGIN SELECT RAISE(ABORT, 'holding idempotency records are append-only'); END;

CREATE TRIGGER holding_audit_append_only_update
BEFORE UPDATE ON audit_events
WHEN OLD.action IN ('holding_created', 'holding_removed') OR NEW.action IN ('holding_created', 'holding_removed')
BEGIN SELECT RAISE(ABORT, 'holding audit events are append-only'); END;
CREATE TRIGGER holding_audit_append_only_delete
BEFORE DELETE ON audit_events
WHEN OLD.action IN ('holding_created', 'holding_removed')
BEGIN SELECT RAISE(ABORT, 'holding audit events are append-only'); END;
CREATE TRIGGER holding_audit_actor_guard
BEFORE INSERT ON audit_events
WHEN NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'invalid holding audit actor')
    WHERE length(NEW.actor) NOT BETWEEN 1 AND 128
       OR NEW.actor GLOB '*[^A-Za-z0-9._~-]*'
       OR substr(NEW.actor, 1, 1) NOT GLOB '[A-Za-z0-9]';
END;

CREATE TRIGGER holding_mutation_audit_scope_guard
BEFORE INSERT ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = NEW.audit_event_id AND action IN ('holding_created', 'holding_removed'))
BEGIN
    SELECT RAISE(ABORT, 'holding audit must identify its portfolio')
    WHERE NOT EXISTS (
        SELECT 1 FROM audit_events e
        WHERE e.id = NEW.audit_event_id AND e.entity_type = 'holding'
          AND e.entity_id = json_extract(e.payload, '$.holding_id')
          AND json_extract(e.payload, '$.portfolio_id') = NEW.portfolio_id
          AND json_extract(e.payload, '$.operation') = CASE e.action WHEN 'holding_created' THEN 'create' WHEN 'holding_removed' THEN 'remove' END
          AND NEW.retain_until = e.retain_until
          AND json_extract(e.payload, '$.occurred_at') = e.created_at
    );
END;
CREATE TRIGGER holding_mutation_audit_owner_update_guard
BEFORE UPDATE ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = OLD.audit_event_id AND action IN ('holding_created', 'holding_removed'))
BEGIN SELECT RAISE(ABORT, 'holding audit ownership is append-only'); END;
CREATE TRIGGER holding_mutation_audit_owner_delete_guard
BEFORE DELETE ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = OLD.audit_event_id AND action IN ('holding_created', 'holding_removed'))
BEGIN SELECT RAISE(ABORT, 'holding audit ownership is append-only'); END;

CREATE TRIGGER holding_mutation_activity_scope_guard
BEFORE INSERT ON activity_events
WHEN NEW.action IN ('holding_created', 'holding_removed')
BEGIN
    SELECT RAISE(ABORT, 'holding Activity must reference its audit')
    WHERE NEW.actor_category NOT IN ('browser_user', 'machine') OR NEW.source_audit_event_id IS NULL
       OR NEW.category <> 'holding' OR NEW.entity_type <> 'holding' OR NEW.entity_id IS NULL
       OR NOT EXISTS (
           SELECT 1 FROM audit_event_portfolios p JOIN audit_events a ON a.id = p.audit_event_id
           WHERE p.audit_event_id = NEW.source_audit_event_id AND p.portfolio_id = NEW.portfolio_id
             AND p.retain_until = NEW.retain_until AND a.action = NEW.action
             AND a.entity_type = NEW.entity_type AND a.entity_id = NEW.entity_id
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
        SELECT 1 FROM audit_events a JOIN audit_event_portfolios p
          ON p.audit_event_id = a.id AND p.portfolio_id = NEW.portfolio_id
        WHERE a.id = NEW.audit_event_id
          AND a.action = CASE NEW.operation WHEN 'create' THEN 'holding_created' WHEN 'remove' THEN 'holding_removed' END
          AND a.entity_type = 'holding' AND a.entity_id = NEW.holding_id
          AND a.created_at = NEW.created_at AND p.retain_until = NEW.retain_until
          AND json_extract(a.payload, '$.portfolio_id') = NEW.portfolio_id
          AND json_extract(a.payload, '$.holding_id') = NEW.holding_id
          AND json_extract(a.payload, '$.operation') = NEW.operation
          AND json_extract(a.payload, '$.occurred_at') = NEW.created_at
    );
    SELECT RAISE(ABORT, 'holding idempotency Activity ownership mismatch')
    WHERE NOT EXISTS (
        SELECT 1 FROM activity_events e
        WHERE e.id = NEW.activity_event_id AND e.portfolio_id = NEW.portfolio_id
          AND e.action = CASE NEW.operation WHEN 'create' THEN 'holding_created' WHEN 'remove' THEN 'holding_removed' END
          AND e.category = 'holding' AND e.entity_type = 'holding' AND e.entity_id = NEW.holding_id
          AND e.occurred_at = NEW.created_at AND e.source_audit_event_id = NEW.audit_event_id
          AND e.retain_until = NEW.retain_until
    );
END;
