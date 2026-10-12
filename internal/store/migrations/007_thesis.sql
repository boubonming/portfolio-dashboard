CREATE UNIQUE INDEX IF NOT EXISTS idx_lots_portfolio_id_id ON lots(portfolio_id, id);

CREATE TABLE portfolio_theses (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    holding_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    summary TEXT NOT NULL,
    intended_holding_period TEXT NOT NULL,
    allocation_limit TEXT,
    reasons_to_hold TEXT NOT NULL,
    risks TEXT NOT NULL,
    reasons_to_sell TEXT NOT NULL,
    conditions_to_change TEXT NOT NULL,
    sources TEXT NOT NULL,
    current_revision INTEGER NOT NULL CHECK (current_revision >= 1),
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(portfolio_id, holding_id),
    UNIQUE(portfolio_id, instrument_id),
    FOREIGN KEY (portfolio_id, holding_id) REFERENCES lots(portfolio_id, id)
);
CREATE INDEX idx_portfolio_theses_portfolio ON portfolio_theses(portfolio_id);
CREATE INDEX idx_portfolio_theses_instrument ON portfolio_theses(portfolio_id, instrument_id);

CREATE TABLE thesis_revisions (
    id TEXT PRIMARY KEY,
    thesis_id TEXT NOT NULL REFERENCES portfolio_theses(id),
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    holding_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    summary TEXT NOT NULL,
    intended_holding_period TEXT NOT NULL,
    allocation_limit TEXT,
    reasons_to_hold TEXT NOT NULL,
    risks TEXT NOT NULL,
    reasons_to_sell TEXT NOT NULL,
    conditions_to_change TEXT NOT NULL,
    sources TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(thesis_id, revision),
    UNIQUE(thesis_id, request_hash),
    FOREIGN KEY (portfolio_id, holding_id) REFERENCES lots(portfolio_id, id)
);
CREATE INDEX idx_thesis_revisions_history ON thesis_revisions(thesis_id, revision DESC);
CREATE INDEX idx_thesis_revisions_portfolio ON thesis_revisions(portfolio_id, holding_id, revision DESC);

CREATE TRIGGER portfolio_theses_scope_insert
BEFORE INSERT ON portfolio_theses BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis holding scope mismatch') END;
END;
CREATE TRIGGER portfolio_theses_scope_update
BEFORE UPDATE OF portfolio_id, holding_id, instrument_id ON portfolio_theses BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis holding scope mismatch') END;
END;
CREATE TRIGGER thesis_revisions_scope_insert
BEFORE INSERT ON thesis_revisions BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis revision holding scope mismatch') END;
END;

CREATE TRIGGER portfolio_theses_append_only_delete
BEFORE DELETE ON portfolio_theses BEGIN
    SELECT RAISE(ABORT, 'theses are append-only');
END;
CREATE TRIGGER thesis_revisions_append_only_update
BEFORE UPDATE ON thesis_revisions BEGIN
    SELECT RAISE(ABORT, 'thesis revisions are append-only');
END;
CREATE TRIGGER thesis_revisions_append_only_delete
BEFORE DELETE ON thesis_revisions BEGIN
    SELECT RAISE(ABORT, 'thesis revisions are append-only');
END;
