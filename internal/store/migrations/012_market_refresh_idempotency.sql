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
    response_payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_payload) = 1 AND json_type(response_payload) = 'object'),
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