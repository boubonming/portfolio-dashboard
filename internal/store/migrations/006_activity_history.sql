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
    sort_time TEXT GENERATED ALWAYS AS (
        printf('%020d', CAST(strftime('%s', occurred_at) AS INTEGER)) ||
        substr(
            (CASE WHEN instr(occurred_at, '.') > 0 AND instr(occurred_at, 'Z') > instr(occurred_at, '.')
                THEN substr(occurred_at, instr(occurred_at, '.') + 1, instr(occurred_at, 'Z') - instr(occurred_at, '.') - 1)
                ELSE '' END) || '000000000', 1, 9
        )
    ) STORED,
    source_audit_event_id TEXT REFERENCES audit_events(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_activity_source_portfolio
    ON activity_events(source_audit_event_id, portfolio_id);
CREATE INDEX IF NOT EXISTS idx_activity_portfolio_order
    ON activity_events(portfolio_id, sort_time DESC, id DESC);
CREATE TRIGGER IF NOT EXISTS activity_events_append_only_update
BEFORE UPDATE ON activity_events BEGIN
    SELECT RAISE(ABORT, 'activity events are append-only');
END;
CREATE TRIGGER IF NOT EXISTS activity_events_append_only_delete
BEFORE DELETE ON activity_events BEGIN
    SELECT RAISE(ABORT, 'activity events are append-only');
END;