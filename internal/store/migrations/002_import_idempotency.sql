CREATE UNIQUE INDEX IF NOT EXISTS idx_portfolios_name ON portfolios(name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instruments_symbol_currency ON instruments(symbol, currency);
CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_action_entity ON audit_events(action, entity_type, entity_id);