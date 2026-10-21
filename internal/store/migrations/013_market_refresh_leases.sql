ALTER TABLE market_refresh_idempotency ADD COLUMN owner_token TEXT;
ALTER TABLE market_refresh_idempotency ADD COLUMN lease_until TEXT;

CREATE INDEX idx_market_refresh_idempotency_lease
    ON market_refresh_idempotency(state, lease_until);

CREATE TRIGGER market_refresh_idempotency_pending_owner_shape
BEFORE INSERT ON market_refresh_idempotency
WHEN NEW.state = 'pending' AND NEW.phase <> 'failed' AND (
    NEW.owner_token IS NULL OR length(NEW.owner_token) <> 48 OR NEW.owner_token GLOB '*[^0-9a-f]*' OR
    NEW.lease_until IS NULL OR length(NEW.lease_until) <> 30 OR
    NEW.lease_until NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9][.][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
)
BEGIN
    SELECT RAISE(ABORT, 'pending market refresh requires bounded owner lease');
END;

CREATE TRIGGER market_refresh_idempotency_pending_owner_update_shape
BEFORE UPDATE ON market_refresh_idempotency
WHEN NEW.state = 'pending' AND NEW.phase <> 'failed' AND (
    NEW.owner_token IS NULL OR length(NEW.owner_token) <> 48 OR NEW.owner_token GLOB '*[^0-9a-f]*' OR
    NEW.lease_until IS NULL OR length(NEW.lease_until) <> 30 OR
    NEW.lease_until NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9][.][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
)
BEGIN
    SELECT RAISE(ABORT, 'pending market refresh requires bounded owner lease');
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
    NEW.created_at <> OLD.created_at OR coalesce(NEW.committed_at, '') <> coalesce(OLD.committed_at, '') OR
    coalesce(NEW.owner_token, '') <> coalesce(OLD.owner_token, '') OR coalesce(NEW.lease_until, '') <> coalesce(OLD.lease_until, '')
)
BEGIN
    SELECT RAISE(ABORT, 'committed market refresh receipt is immutable');
END;