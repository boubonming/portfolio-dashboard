CREATE TABLE market_refresh_idempotency (
    actor_category TEXT NOT NULL CHECK (actor_category IN ('browser_user', 'machine')),
    actor_id TEXT NOT NULL CHECK (
        length(actor_id) BETWEEN 1 AND 128
        AND actor_id NOT GLOB '*[^A-Za-z0-9._~-]*'
        AND substr(actor_id, 1, 1) GLOB '[A-Za-z0-9]'
    ),
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    operation TEXT NOT NULL CHECK (operation = 'refresh'),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
    request_fingerprint TEXT NOT NULL CHECK (length(request_fingerprint) = 64),
    state TEXT NOT NULL CHECK (state IN ('pending', 'committed')),
    phase TEXT NOT NULL CHECK (phase IN ('reserved', 'observed', 'snapshotted', 'failed', 'committed')),
    response_payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_payload) = 1 AND json_type(response_payload) = 'object'),
    refresh_payload TEXT CHECK (refresh_payload IS NULL OR (json_valid(refresh_payload) = 1 AND json_type(refresh_payload) = 'object')),
    snapshot_payload TEXT CHECK (snapshot_payload IS NULL OR (json_valid(snapshot_payload) = 1 AND json_type(snapshot_payload) = 'object')),
    error_message TEXT,
    refresh_id TEXT,
    snapshot_id TEXT,
    audit_event_id TEXT REFERENCES audit_events(id),
    activity_event_id TEXT REFERENCES activity_events(id),
    created_at TEXT NOT NULL,
    committed_at TEXT,
    PRIMARY KEY (actor_category, actor_id, portfolio_id, operation, idempotency_key)
);
CREATE INDEX idx_market_refresh_idempotency_portfolio_created
    ON market_refresh_idempotency(portfolio_id, created_at DESC);
CREATE INDEX idx_market_refresh_idempotency_receipt
    ON market_refresh_idempotency(refresh_id, snapshot_id);

CREATE TRIGGER market_refresh_idempotency_no_delete
BEFORE DELETE ON market_refresh_idempotency
BEGIN
    SELECT RAISE(ABORT, 'market refresh receipts are append-only');
END;

CREATE TRIGGER market_refresh_idempotency_committed_immutable
BEFORE UPDATE ON market_refresh_idempotency
WHEN OLD.state = 'committed' AND (
    NEW.actor_category <> OLD.actor_category OR NEW.actor_id <> OLD.actor_id OR
    NEW.portfolio_id <> OLD.portfolio_id OR NEW.operation <> OLD.operation OR
    NEW.idempotency_key <> OLD.idempotency_key OR NEW.request_fingerprint <> OLD.request_fingerprint OR
    NEW.state <> OLD.state OR NEW.phase <> OLD.phase OR NEW.response_payload <> OLD.response_payload OR
    coalesce(NEW.refresh_payload, '') <> coalesce(OLD.refresh_payload, '') OR
    coalesce(NEW.snapshot_payload, '') <> coalesce(OLD.snapshot_payload, '') OR
    coalesce(NEW.error_message, '') <> coalesce(OLD.error_message, '') OR
    coalesce(NEW.refresh_id, '') <> coalesce(OLD.refresh_id, '') OR
    coalesce(NEW.snapshot_id, '') <> coalesce(OLD.snapshot_id, '') OR
    coalesce(NEW.audit_event_id, '') <> coalesce(OLD.audit_event_id, '') OR
    coalesce(NEW.activity_event_id, '') <> coalesce(OLD.activity_event_id, '') OR
    NEW.created_at <> OLD.created_at OR coalesce(NEW.committed_at, '') <> coalesce(OLD.committed_at, '')
)
BEGIN
    SELECT RAISE(ABORT, 'committed market refresh receipt is immutable');
END;