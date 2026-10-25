PRAGMA foreign_keys=ON;
PRAGMA defer_foreign_keys=ON;

-- Market refresh is an independent, fail-closed capability. Existing
-- principals and receipts intentionally receive the disabled value.
ALTER TABLE machine_principals ADD COLUMN permission_market_data_refresh INTEGER NOT NULL DEFAULT 0 CHECK(permission_market_data_refresh IN (0, 1));
ALTER TABLE market_refresh_idempotency ADD COLUMN permission_market_data_refresh INTEGER NOT NULL DEFAULT 0 CHECK(permission_market_data_refresh IN (0, 1));

CREATE TRIGGER machine_refresh_operator_audit_append_only_update
BEFORE UPDATE ON audit_events
WHEN OLD.action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke')
  OR NEW.action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke')
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh operator audits are append-only');
END;
CREATE TRIGGER machine_refresh_operator_audit_append_only_delete
BEFORE DELETE ON audit_events
WHEN OLD.action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke')
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh operator audits are append-only');
END;
CREATE TRIGGER machine_refresh_operator_audit_replacement_guard
BEFORE INSERT ON audit_events
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id=NEW.id)
 AND (NEW.action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke') OR EXISTS (SELECT 1 FROM audit_events WHERE id=NEW.id AND action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke')))
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh operator audits are append-only');
END;
CREATE TRIGGER machine_refresh_operator_owner_append_only_update
BEFORE UPDATE ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id=OLD.audit_event_id AND action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke'))
 OR EXISTS (SELECT 1 FROM audit_events WHERE id=NEW.audit_event_id AND action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke'))
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh audit ownership is append-only');
END;
CREATE TRIGGER machine_refresh_operator_owner_append_only_delete
BEFORE DELETE ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events WHERE id=OLD.audit_event_id AND action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke'))
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh audit ownership is append-only');
END;
CREATE TRIGGER machine_refresh_operator_owner_insert_guard
BEFORE INSERT ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events e WHERE e.id=NEW.audit_event_id AND e.action IN ('machine_market_refresh_grant', 'machine_market_refresh_revoke'))
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh audit ownership is append-only')
    WHERE EXISTS (SELECT 1 FROM audit_event_portfolios WHERE audit_event_id=NEW.audit_event_id)
       OR NEW.portfolio_id IS NOT json_extract((SELECT payload FROM audit_events WHERE id=NEW.audit_event_id), '$.portfolio_id')
       OR NEW.retain_until IS NOT (SELECT retain_until FROM audit_events WHERE id=NEW.audit_event_id);
END;

-- Receipt and grant fields are independent and fail closed at the database
-- boundary as well as in the runtime authorization path.
CREATE TRIGGER market_refresh_idempotency_machine_permission_guard
BEFORE INSERT ON market_refresh_idempotency
WHEN NEW.actor_category = 'machine' AND NEW.permission_market_data_refresh <> 1
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh receipt requires market_data:refresh');
END;
CREATE TRIGGER market_refresh_idempotency_machine_permission_update_guard
BEFORE UPDATE OF actor_category,permission_market_data_refresh ON market_refresh_idempotency
WHEN NEW.actor_category = 'machine' AND NEW.permission_market_data_refresh <> 1
BEGIN
    SELECT RAISE(ABORT, 'machine market refresh receipt requires market_data:refresh');
END;

DROP TRIGGER market_refresh_idempotency_committed_immutable;
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
    NEW.permission_market_data_refresh <> OLD.permission_market_data_refresh OR
    NEW.created_at <> OLD.created_at OR coalesce(NEW.committed_at, '') <> coalesce(OLD.committed_at, '') OR
    coalesce(NEW.owner_token, '') <> coalesce(OLD.owner_token, '') OR coalesce(NEW.lease_until, '') <> coalesce(OLD.lease_until, '')
)
BEGIN
    SELECT RAISE(ABORT, 'committed market refresh receipt is immutable');
END;
