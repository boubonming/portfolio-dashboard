-- Machine market refresh is authorized by the existing exact-portfolio
-- portfolio:read authority. Keep the legacy columns and operator audit
-- triggers for upgrade compatibility, but stop enforcing the retired receipt
-- grant on new and updated workflow rows.
DROP TRIGGER IF EXISTS market_refresh_idempotency_machine_permission_guard;
DROP TRIGGER IF EXISTS market_refresh_idempotency_machine_permission_update_guard;
