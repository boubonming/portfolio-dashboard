package domain

import "time"

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

const (
	Fresh   = "fresh"
	Stale   = "stale"
	Missing = "missing"
	Future  = "future"
	Invalid = "invalid"
)

// Quote and FXRate are provider-neutral observations. Values remain strings
// at storage/API boundaries and are parsed as Decimal in calculations.
type Quote struct {
	ID           string `json:"id"`
	InstrumentID string `json:"instrument_id"`
	Symbol       string `json:"symbol,omitempty"`
	Price        string `json:"price"`
	Currency     string `json:"currency"`
	Source       string `json:"source"`
	MarketAt     string `json:"market_at"`
	FetchedAt    string `json:"fetched_at"`
	Basis        string `json:"basis"`
	Provenance   string `json:"provenance"`
}

type FXRate struct {
	ID            string `json:"id"`
	BaseCurrency  string `json:"base_currency"`
	QuoteCurrency string `json:"quote_currency"`
	Rate          string `json:"rate"`
	Source        string `json:"source"`
	MarketAt      string `json:"market_at"`
	FetchedAt     string `json:"fetched_at"`
	Basis         string `json:"basis,omitempty"`
	Provenance    string `json:"provenance"`
}

// ProviderMapping is an explicit, versioned mapping. Provider symbols are
// never inferred from the instrument's original symbol.
type ProviderMapping struct {
	ID             string `json:"id"`
	InstrumentID   string `json:"instrument_id"`
	Provider       string `json:"provider"`
	ProviderSymbol string `json:"provider_symbol"`
	QuoteCurrency  string `json:"quote_currency"`
	ActiveFrom     string `json:"active_from"`
	Provenance     string `json:"provenance"`
	CreatedAt      string `json:"created_at"`
}

type ProviderRefreshStatus struct {
	Provider       string   `json:"provider"`
	Status         string   `json:"status"`
	RequestCount   int      `json:"request_count"`
	SucceededCount int      `json:"succeeded_count"`
	FailedCount    int      `json:"failed_count"`
	MissingMapping int      `json:"missing_mapping_count"`
	ObservationIDs []string `json:"observation_ids,omitempty"`
	Errors         []string `json:"errors,omitempty"`
}

// RefreshResult is deliberately aggregate-only. It contains no holdings,
// symbols, quantities, or provider credentials.
type RefreshResult struct {
	RefreshID      string                  `json:"refresh_id"`
	PortfolioID    string                  `json:"portfolio_id"`
	Status         string                  `json:"status"`
	StartedAt      string                  `json:"started_at"`
	EndedAt        string                  `json:"ended_at"`
	ObservationIDs []string                `json:"observation_ids,omitempty"`
	Providers      []ProviderRefreshStatus `json:"providers"`
}

type Freshness struct {
	Status   string `json:"status"`
	MarketAt string `json:"market_at,omitempty"`
	Age      string `json:"age,omitempty"`
}

type ValuationLot struct {
	ID           string  `json:"lot_id"`
	ImportRunID  string  `json:"import_run_id,omitempty"`
	SourceSHA256 string  `json:"source_sha256,omitempty"`
	InstrumentID string  `json:"instrument_id"`
	Symbol       string  `json:"symbol"`
	Currency     string  `json:"currency"`
	Quantity     string  `json:"quantity"`
	UnitCost     *string `json:"unit_cost,omitempty"`
	CostBasis    *string `json:"cost_basis,omitempty"`
}

type LotValuation struct {
	LotID          string    `json:"lot_id"`
	Symbol         string    `json:"symbol"`
	InstrumentID   string    `json:"instrument_id,omitempty"`
	Currency       string    `json:"currency"`
	Quantity       string    `json:"quantity"`
	Price          string    `json:"price,omitempty"`
	MarketValue    string    `json:"market_value,omitempty"`
	CostBasis      *string   `json:"cost_basis,omitempty"`
	UnrealisedPnL  *string   `json:"unrealised_pnl,omitempty"`
	ReportingValue *string   `json:"reporting_value,omitempty"`
	QuoteID        string    `json:"quote_id,omitempty"`
	FXIDs          []string  `json:"fx_ids,omitempty"`
	Freshness      Freshness `json:"freshness"`
}

type CurrencySubtotal struct {
	Currency      string `json:"currency"`
	MarketValue   string `json:"market_value"`
	CostBasis     string `json:"cost_basis,omitempty"`
	UnrealisedPnL string `json:"unrealised_pnl,omitempty"`
}

type ValuationInput struct {
	Lots              []ValuationLot `json:"lots"`
	Quotes            []Quote        `json:"quotes"`
	FXRates           []FXRate       `json:"fx_rates"`
	ReportingCurrency string         `json:"reporting_currency"`
	AsOf              time.Time      `json:"as_of"`
	MaxAge            time.Duration  `json:"-"`
}

type Valuation struct {
	Complete             bool               `json:"complete"`
	ReportingCurrency    string             `json:"reporting_currency"`
	ReportingTotal       string             `json:"reporting_total,omitempty"`
	Subtotals            []CurrencySubtotal `json:"subtotals"`
	Lots                 []LotValuation     `json:"lots,omitempty"`
	MissingDependencies  []string           `json:"missing_dependencies,omitempty"`
	StaleDependencies    []string           `json:"stale_dependencies,omitempty"`
	InvalidDependencies  []string           `json:"invalid_dependencies,omitempty"`
	QuoteIDs             []string           `json:"quote_ids"`
	FXIDs                []string           `json:"fx_ids"`
	SourceTimestamps     []string           `json:"source_timestamps"`
	PresentationTimezone string             `json:"presentation_timezone"`
}

type SnapshotStatus struct {
	SnapshotID           string             `json:"snapshot_id"`
	PortfolioID          string             `json:"portfolio_id"`
	CalculationVersion   string             `json:"calculation_version"`
	CreatedAt            string             `json:"created_at"`
	AsOf                 string             `json:"as_of"`
	Complete             bool               `json:"complete"`
	ReportingCurrency    string             `json:"reporting_currency"`
	ReportingTotal       string             `json:"reporting_total,omitempty"`
	Currencies           []string           `json:"currencies"`
	Subtotals            []CurrencySubtotal `json:"subtotals"`
	MissingCount         int                `json:"missing_count"`
	StaleCount           int                `json:"stale_count"`
	InvalidCount         int                `json:"invalid_count"`
	QuoteIDs             []string           `json:"quote_ids"`
	FXIDs                []string           `json:"fx_ids"`
	SourceTimestamps     []string           `json:"source_timestamps"`
	PresentationTimezone string             `json:"presentation_timezone"`
}
