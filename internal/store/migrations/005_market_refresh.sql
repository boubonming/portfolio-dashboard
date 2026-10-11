CREATE TABLE IF NOT EXISTS instrument_provider_mappings (
    id TEXT PRIMARY KEY,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    provider TEXT NOT NULL CHECK(provider IN ('finnhub','alpha_vantage','coingecko','open_exchange_rates','manual')),
    provider_symbol TEXT NOT NULL,
    quote_currency TEXT NOT NULL,
    active_from TEXT NOT NULL,
    provenance TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provider_mappings_lookup
    ON instrument_provider_mappings(instrument_id, provider, active_from DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_mappings_version
    ON instrument_provider_mappings(instrument_id, provider, provider_symbol, quote_currency, active_from);
CREATE TRIGGER IF NOT EXISTS provider_mappings_append_only_update
BEFORE UPDATE ON instrument_provider_mappings BEGIN
    SELECT RAISE(ABORT, 'provider mappings are append-only');
END;
CREATE TRIGGER IF NOT EXISTS provider_mappings_append_only_delete
BEFORE DELETE ON instrument_provider_mappings BEGIN
    SELECT RAISE(ABORT, 'provider mappings are append-only');
END;
CREATE TABLE IF NOT EXISTS refresh_runs (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    provider TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0,
    succeeded_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    missing_mapping_count INTEGER NOT NULL DEFAULT 0,
    error_classification TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_refresh_runs_portfolio_started
    ON refresh_runs(portfolio_id, started_at DESC);
