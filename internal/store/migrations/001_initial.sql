CREATE TABLE IF NOT EXISTS portfolios (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    reporting_currency TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instruments (
    id TEXT PRIMARY KEY,
    symbol TEXT NOT NULL,
    description TEXT,
    currency TEXT NOT NULL,
    asset_type TEXT,
    exchange TEXT,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    broker TEXT,
    name TEXT,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS lots (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    account_id TEXT REFERENCES accounts(id),
    quantity TEXT NOT NULL,
    unit_cost TEXT,
    cost_basis TEXT,
    currency TEXT NOT NULL,
    acquisition_date TEXT,
    source_row INTEGER,
    source_ref TEXT,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS import_runs (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT REFERENCES portfolios(id),
    source_path TEXT NOT NULL,
    source_sha256 TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS import_items (
    id TEXT PRIMARY KEY,
    import_run_id TEXT NOT NULL REFERENCES import_runs(id),
    source_row INTEGER NOT NULL,
    symbol TEXT,
    classification TEXT NOT NULL,
    payload TEXT NOT NULL,
    issue TEXT
);
CREATE TABLE IF NOT EXISTS quotes (
    id TEXT PRIMARY KEY,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    price TEXT NOT NULL,
    currency TEXT NOT NULL,
    source TEXT NOT NULL,
    market_at TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    basis TEXT NOT NULL,
    provenance TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS fx_rates (
    id TEXT PRIMARY KEY,
    base_currency TEXT NOT NULL,
    quote_currency TEXT NOT NULL,
    rate TEXT NOT NULL,
    source TEXT NOT NULL,
    market_at TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    provenance TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    calculation_version TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS reviews (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
    instrument_id TEXT REFERENCES instruments(id),
    status TEXT NOT NULL,
    rationale TEXT,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lots_portfolio ON lots(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_quotes_instrument ON quotes(instrument_id, market_at);
CREATE INDEX IF NOT EXISTS idx_reviews_snapshot ON reviews(snapshot_id);
