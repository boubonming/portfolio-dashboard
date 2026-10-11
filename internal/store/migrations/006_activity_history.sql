CREATE TABLE activity_events (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    actor_category TEXT NOT NULL,
    action TEXT NOT NULL,
    category TEXT NOT NULL,
    entity_type TEXT,
    entity_id TEXT,
    summary TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL CHECK (
        length(occurred_at) = 30 AND
        occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND
        substr(occurred_at, 20, 1) = '.' AND
        occurred_at >= '1970-01-01T00:00:00.000000000Z'
    ),
    sort_time TEXT NOT NULL CHECK (sort_time = occurred_at),
    source_audit_event_id TEXT REFERENCES audit_events(id)
);
CREATE UNIQUE INDEX idx_activity_source_portfolio
    ON activity_events(source_audit_event_id, portfolio_id);
CREATE INDEX idx_activity_portfolio_order
    ON activity_events(portfolio_id, sort_time DESC, id DESC);
CREATE TRIGGER activity_events_append_only_update
BEFORE UPDATE ON activity_events BEGIN
    SELECT RAISE(ABORT, 'activity events are append-only');
END;
CREATE TRIGGER activity_events_append_only_delete
BEFORE DELETE ON activity_events BEGIN
    SELECT RAISE(ABORT, 'activity events are append-only');
END;
