package domain

// ReviewSnapshot is the immutable, portfolio-scoped snapshot wire model. The
// snapshot identity fields are omitted for the explicit no_snapshot branch.
type ReviewSnapshot struct {
	PortfolioID         string                 `json:"portfolio_id"`
	SnapshotID          string                 `json:"snapshot_id,omitempty"`
	CalculationVersion  string                 `json:"calculation_version,omitempty"`
	State               string                 `json:"state"`
	CreatedAt           string                 `json:"created_at,omitempty"`
	AsOf                string                 `json:"as_of,omitempty"`
	Complete            bool                   `json:"complete"`
	MissingDependencies []string               `json:"missing_dependencies"`
	StaleDependencies   []string               `json:"stale_dependencies"`
	InvalidDependencies []string               `json:"invalid_dependencies"`
	Holdings            []ReviewHoldingContext `json:"holdings"`
}

// ReviewHoldingContext contains only data copied from the immutable snapshot
// and its provider observations. All decimal and timestamp values remain
// strings at the HTTP boundary.
type ReviewHoldingContext struct {
	PortfolioID         string  `json:"portfolio_id"`
	SnapshotID          string  `json:"snapshot_id"`
	HoldingID           string  `json:"holding_id"`
	InstrumentID        string  `json:"instrument_id"`
	Symbol              string  `json:"symbol"`
	Name                string  `json:"name"`
	Quantity            string  `json:"quantity"`
	Currency            string  `json:"currency"`
	UnitCost            *string `json:"unit_cost,omitempty"`
	CostBasis           *string `json:"cost_basis,omitempty"`
	LatestPrice         *string `json:"latest_price,omitempty"`
	LatestValue         *string `json:"latest_value,omitempty"`
	ReportingValue      *string `json:"reporting_value,omitempty"`
	QuoteSource         string  `json:"quote_source,omitempty"`
	MarketAt            string  `json:"market_at,omitempty"`
	FreshnessState      string  `json:"freshness_state"`
	FreshnessAgeSeconds *string `json:"freshness_age_seconds,omitempty"`
}
