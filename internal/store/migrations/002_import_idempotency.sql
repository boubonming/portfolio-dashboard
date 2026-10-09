ALTER TABLE lots ADD COLUMN source_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE import_items ADD COLUMN source_sha256 TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_import_runs_source_hash ON import_runs(source_sha256);
CREATE UNIQUE INDEX IF NOT EXISTS idx_portfolios_name ON portfolios(name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instruments_symbol_currency ON instruments(symbol, currency);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lots_source_provenance ON lots(source_sha256, source_row);
CREATE UNIQUE INDEX IF NOT EXISTS idx_import_items_source_row ON import_items(import_run_id, source_row);
CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_import_action ON audit_events(action, entity_type, entity_id);