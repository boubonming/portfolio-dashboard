package domain

// PortfolioOverview is the read model exposed by the Phase C dashboard API.
// It intentionally contains source-traceable lots rather than database rows.
type PortfolioOverview struct {
	Portfolio         PortfolioSummary `json:"portfolio"`
	ReportingCurrency string           `json:"reporting_currency"`
	Snapshot          SnapshotView     `json:"snapshot"`
	Holdings          []HoldingView    `json:"holdings"`
}

type PortfolioSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DefaultCurrency string `json:"default_reporting_currency"`
}

type SnapshotView struct {
	State               string             `json:"state"`
	SnapshotID          string             `json:"snapshot_id,omitempty"`
	CalculationVersion  string             `json:"calculation_version,omitempty"`
	CreatedAt           string             `json:"created_at,omitempty"`
	AsOf                string             `json:"as_of,omitempty"`
	Complete            bool               `json:"complete"`
	ReportingTotal      string             `json:"reporting_total,omitempty"`
	MissingDependencies []string           `json:"missing_dependencies,omitempty"`
	StaleDependencies   []string           `json:"stale_dependencies,omitempty"`
	InvalidDependencies []string           `json:"invalid_dependencies,omitempty"`
	SourceTimestamps    []string           `json:"source_timestamps,omitempty"`
	Subtotals           []CurrencySubtotal `json:"subtotals"`
}

type HoldingView struct {
	LotID                  string    `json:"lot_id"`
	Symbol                 string    `json:"symbol"`
	Name                   string    `json:"name,omitempty"`
	Description            string    `json:"description,omitempty"`
	Quantity               string    `json:"quantity"`
	Currency               string    `json:"currency"`
	UnitCost               *string   `json:"unit_cost,omitempty"`
	CostBasis              *string   `json:"cost_basis,omitempty"`
	LatestPrice            *string   `json:"latest_price,omitempty"`
	LatestValue            *string   `json:"latest_value,omitempty"`
	ReportingValue         *string   `json:"reporting_value,omitempty"`
	QuoteSource            string    `json:"quote_source,omitempty"`
	Freshness              Freshness `json:"freshness"`
	DataQuality            []string  `json:"data_quality"`
	Broker                 *string   `json:"broker,omitempty"`
	AcquisitionDate        *string   `json:"acquisition_date,omitempty"`
	MissingBroker          bool      `json:"missing_broker"`
	MissingAcquisitionDate bool      `json:"missing_acquisition_date"`
	SourceRow              int       `json:"source_row,omitempty"`
}
