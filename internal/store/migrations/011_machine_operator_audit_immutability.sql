-- Operator grant/revoke audits are immutable attribution records. This
-- forward migration covers both the event and its portfolio owner without
-- changing the historical 001-010 migration bodies.
CREATE TRIGGER machine_operator_audit_append_only_update
BEFORE UPDATE ON audit_events
WHEN OLD.action IN ('machine_holdings_grant', 'machine_holdings_revoke')
  OR NEW.action IN ('machine_holdings_grant', 'machine_holdings_revoke')
BEGIN
    SELECT RAISE(ABORT, 'machine operator audit events are append-only');
END;

CREATE TRIGGER machine_operator_audit_append_only_delete
BEFORE DELETE ON audit_events
WHEN OLD.action IN ('machine_holdings_grant', 'machine_holdings_revoke')
BEGIN
    SELECT RAISE(ABORT, 'machine operator audit events are append-only');
END;

-- SQLite's INSERT OR REPLACE deletes a conflicting row without running its
-- DELETE trigger. Reject replacement attempts before the conflict can be
-- resolved, including attempts that rename an existing operator event away.
CREATE TRIGGER machine_operator_audit_replacement_guard
BEFORE INSERT ON audit_events
WHEN EXISTS (
    SELECT 1 FROM audit_events
    WHERE id = NEW.id
)
AND (
    NEW.action IN ('machine_holdings_grant', 'machine_holdings_revoke')
    OR EXISTS (
        SELECT 1 FROM audit_events
        WHERE id = NEW.id
          AND action IN ('machine_holdings_grant', 'machine_holdings_revoke')
    )
)
BEGIN
    SELECT RAISE(ABORT, 'machine operator audit events are append-only');
END;

CREATE TRIGGER machine_operator_audit_owner_append_only_update
BEFORE UPDATE ON audit_event_portfolios
WHEN EXISTS (
    SELECT 1 FROM audit_events
    WHERE id = OLD.audit_event_id
      AND action IN ('machine_holdings_grant', 'machine_holdings_revoke')
)
OR EXISTS (
    SELECT 1 FROM audit_events
    WHERE id = NEW.audit_event_id
      AND action IN ('machine_holdings_grant', 'machine_holdings_revoke')
)
BEGIN
    SELECT RAISE(ABORT, 'machine operator audit ownership is append-only');
END;

CREATE TRIGGER machine_operator_audit_owner_append_only_delete
BEFORE DELETE ON audit_event_portfolios
WHEN EXISTS (
    SELECT 1 FROM audit_events
    WHERE id = OLD.audit_event_id
      AND action IN ('machine_holdings_grant', 'machine_holdings_revoke')
)
BEGIN
    SELECT RAISE(ABORT, 'machine operator audit ownership is append-only');
END;

CREATE TRIGGER machine_operator_audit_owner_insert_guard
BEFORE INSERT ON audit_event_portfolios
WHEN EXISTS (
    SELECT 1 FROM audit_events e
    WHERE e.id = NEW.audit_event_id
      AND e.action IN ('machine_holdings_grant', 'machine_holdings_revoke')
)
BEGIN
    SELECT RAISE(ABORT, 'machine operator audit ownership is append-only')
    WHERE EXISTS (
        SELECT 1 FROM audit_event_portfolios
        WHERE audit_event_id = NEW.audit_event_id
    )
       OR NEW.portfolio_id IS NOT json_extract((SELECT payload FROM audit_events WHERE id = NEW.audit_event_id), '$.portfolio_id')
       OR NEW.retain_until IS NOT (SELECT retain_until FROM audit_events WHERE id = NEW.audit_event_id);
END;