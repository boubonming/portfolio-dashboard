package domain

// ProviderReadiness exposes only provider identity and whether server-side
// configuration is present. Secrets never cross this boundary.
type ProviderReadiness struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Public     bool   `json:"public"`
}

type MarketMappingView struct {
	Provider       string `json:"provider"`
	ProviderSymbol string `json:"provider_symbol"`
	QuoteCurrency  string `json:"quote_currency"`
	ActiveFrom     string `json:"active_from"`
	Provenance     string `json:"provenance,omitempty"`
}

type MarketInstrumentView struct {
	InstrumentID string              `json:"instrument_id"`
	Symbol       string              `json:"symbol"`
	Description  string              `json:"description,omitempty"`
	Currency     string              `json:"currency"`
	Mappings     []MarketMappingView `json:"mappings"`
}

type MarketDataSetup struct {
	PortfolioID string                 `json:"portfolio_id"`
	ETag        string                 `json:"etag"`
	Providers   []ProviderReadiness    `json:"providers"`
	Instruments []MarketInstrumentView `json:"instruments"`
}

type MappingBatchItem struct {
	InstrumentID      string `json:"instrument_id"`
	Provider          string `json:"provider"`
	ProviderSymbol    string `json:"provider_symbol"`
	QuoteCurrency     string `json:"quote_currency"`
	ActiveFrom        string `json:"active_from,omitempty"`
	Provenance        string `json:"provenance"`
	ConfirmUnverified bool   `json:"confirm_unverified"`
}

type MappingBatchOutcome struct {
	InstrumentID string           `json:"instrument_id"`
	Status       string           `json:"status"`
	Mapping      *ProviderMapping `json:"mapping,omitempty"`
	Error        string           `json:"error,omitempty"`
}

type MappingBatchResult struct {
	Status   string                `json:"status"`
	ETag     string                `json:"etag"`
	Outcomes []MappingBatchOutcome `json:"outcomes"`
}

type MarketRefreshResult struct {
	Refresh   RefreshResult  `json:"refresh"`
	Snapshot  SnapshotStatus `json:"snapshot"`
	Complete  bool           `json:"complete"`
	Freshness string         `json:"freshness_policy"`
	Missing   []string       `json:"missing_instruments,omitempty"`
}
