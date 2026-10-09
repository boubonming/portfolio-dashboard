package domain

// Lot is a source-traceable opening holding parsed from the authoritative note.
type Lot struct {
	Symbol          string   `json:"symbol"`
	Description     string   `json:"description,omitempty"`
	Currency        string   `json:"currency"`
	AccountBroker   *string  `json:"account_broker"`
	Quantity        string   `json:"quantity"`
	QuantityRaw     string   `json:"quantity_raw"`
	UnitCost        *string  `json:"unit_cost"`
	CostBasis       *string  `json:"cost_basis"`
	AcquisitionDate *string  `json:"acquisition_date"`
	SourceRow       int      `json:"source_row"`
	SourceText      string   `json:"source_text"`
	Classification  string   `json:"classification"`
	Included        bool     `json:"included"`
	Issues          []string `json:"issues,omitempty"`
}

type UnsupportedRow struct {
	SourceRow  int      `json:"source_row"`
	SourceText string   `json:"source_text"`
	Reason     string   `json:"reason"`
	Issues     []string `json:"issues,omitempty"`
}

type Preview struct {
	Version         string           `json:"version"`
	Source          string           `json:"source"`
	SourceSHA256    string           `json:"source_sha256"`
	CurrencyHeaders []string         `json:"currency_headers"`
	Lots            []Lot            `json:"lots"`
	Unsupported     []UnsupportedRow `json:"unsupported_rows"`
	Summary         PreviewSummary   `json:"summary"`
}

type PreviewSummary struct {
	SourceTableRows int      `json:"source_table_rows"`
	HoldingRows     int      `json:"holding_rows"`
	LotsIncluded    int      `json:"lots_included"`
	AggregateRows   int      `json:"aggregate_rows"`
	DistinctSymbols int      `json:"distinct_symbols"`
	AnomalyCount    int      `json:"anomaly_count"`
	Anomalies       []string `json:"anomalies"`
}

// ImportResult is the aggregate outcome of an approved import.
type ImportResult struct {
	Status         string `json:"status"`
	AlreadyApplied bool   `json:"already_applied"`
	ImportRunID    string `json:"import_run_id,omitempty"`
	PortfolioID    string `json:"portfolio_id,omitempty"`
	SourceSHA256   string `json:"source_sha256"`
	LotsImported   int    `json:"lots_imported"`
	ItemsImported  int    `json:"items_imported"`
}

// ImportStatus is safe for CLI display: it contains aggregates, not lot values.
type ImportStatus struct {
	PortfolioCount  int      `json:"portfolio_count"`
	InstrumentCount int      `json:"instrument_count"`
	LotCount        int      `json:"lot_count"`
	ImportRunCount  int      `json:"import_run_count"`
	ImportItemCount int      `json:"import_item_count"`
	AuditEventCount int      `json:"audit_event_count"`
	Currencies      []string `json:"currencies"`
	SourceSHA256    string   `json:"source_sha256,omitempty"`
	ImportedAt      string   `json:"imported_at,omitempty"`
}
