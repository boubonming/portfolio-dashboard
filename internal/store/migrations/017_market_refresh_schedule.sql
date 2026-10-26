PRAGMA foreign_keys=ON;

CREATE TABLE market_refresh_schedules (
    portfolio_id TEXT PRIMARY KEY REFERENCES portfolios(id),
    enabled INTEGER NOT NULL CHECK(enabled IN (0, 1)),
    cron TEXT NOT NULL CHECK(length(cron) <= 256),
    timezone TEXT NOT NULL CHECK(length(timezone) BETWEEN 1 AND 64),
    reporting_currency TEXT NOT NULL DEFAULT '' CHECK(reporting_currency = '' OR (length(reporting_currency) = 3 AND reporting_currency NOT GLOB '*[^A-Z]*')),
    revision INTEGER NOT NULL CHECK(revision >= 1 AND revision <= 2147483647),
    updated_at TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'saved' CHECK(source = 'saved'),
    UNIQUE(portfolio_id)
);

CREATE INDEX idx_market_refresh_schedules_updated
    ON market_refresh_schedules(updated_at DESC, portfolio_id);

CREATE TRIGGER market_refresh_schedules_revision_guard
BEFORE UPDATE ON market_refresh_schedules
WHEN NEW.portfolio_id <> OLD.portfolio_id OR NEW.revision <= OLD.revision
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule revision must increase');
END;

CREATE TRIGGER market_refresh_schedules_no_replacement_insert
BEFORE INSERT ON market_refresh_schedules
WHEN EXISTS (SELECT 1 FROM market_refresh_schedules WHERE portfolio_id = NEW.portfolio_id)
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedules cannot be replaced');
END;

CREATE TRIGGER market_refresh_schedules_no_delete
BEFORE DELETE ON market_refresh_schedules
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedules are not deletable');
END;

CREATE TRIGGER market_refresh_schedule_audit_no_update
BEFORE UPDATE ON audit_events
WHEN OLD.action = 'market.refresh.schedule.updated'
  OR NEW.action = 'market.refresh.schedule.updated'
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule audits are append-only');
END;

CREATE TRIGGER market_refresh_schedule_audit_no_replacement_insert
BEFORE INSERT ON audit_events
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = NEW.id)
 AND (NEW.action = 'market.refresh.schedule.updated'
      OR EXISTS (SELECT 1 FROM audit_events WHERE id = NEW.id AND action = 'market.refresh.schedule.updated'))
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule audits are append-only');
END;

CREATE TRIGGER market_refresh_schedule_audit_no_delete
BEFORE DELETE ON audit_events
WHEN OLD.action = 'market.refresh.schedule.updated'
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule audits are append-only');
END;

CREATE TRIGGER market_refresh_schedule_audit_owner_no_update
BEFORE UPDATE ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = OLD.audit_event_id AND action = 'market.refresh.schedule.updated')
   OR EXISTS (SELECT 1 FROM audit_events WHERE id = NEW.audit_event_id AND action = 'market.refresh.schedule.updated')
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule audit ownership is append-only');
END;

CREATE TRIGGER market_refresh_schedule_audit_owner_no_delete
BEFORE DELETE ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = OLD.audit_event_id AND action = 'market.refresh.schedule.updated')
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule audit ownership is append-only');
END;

CREATE TRIGGER market_refresh_schedule_audit_owner_insert_guard
BEFORE INSERT ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id = NEW.audit_event_id AND action = 'market.refresh.schedule.updated')
BEGIN
    SELECT RAISE(ABORT, 'market refresh schedule audit ownership is append-only')
    WHERE EXISTS (SELECT 1 FROM audit_event_portfolios WHERE audit_event_id = NEW.audit_event_id)
       OR NEW.portfolio_id IS NOT json_extract((SELECT payload FROM audit_events WHERE id = NEW.audit_event_id), '$.portfolio_id')
       OR NEW.retain_until IS NOT (SELECT retain_until FROM audit_events WHERE id = NEW.audit_event_id);
END;
