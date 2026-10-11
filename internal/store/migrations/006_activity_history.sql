CREATE TABLE IF NOT EXISTS activity_events (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    actor_category TEXT NOT NULL,
    action TEXT NOT NULL,
    category TEXT NOT NULL,
    entity_type TEXT,
    entity_id TEXT,
    summary TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL,
    source_audit_event_id TEXT REFERENCES audit_events(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_activity_source_portfolio
    ON activity_events(source_audit_event_id, portfolio_id);
CREATE INDEX IF NOT EXISTS idx_activity_portfolio_order
    ON activity_events(portfolio_id, occurred_at DESC, id DESC);