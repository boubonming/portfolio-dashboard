package domain

// Advisory review wire models. These types intentionally keep decimal and time
// values as strings so no JSON number or float conversion can alter a review.
type ReviewScope struct {
	Type          string   `json:"type"`
	HoldingIDs    []string `json:"holding_ids"`
	InstrumentIDs []string `json:"instrument_ids"`
}

type ReviewSubmission struct {
	SnapshotID         string           `json:"snapshot_id"`
	CalculationVersion string           `json:"calculation_version"`
	Scope              ReviewScope      `json:"scope"`
	Status             string           `json:"status"`
	Recommendation     string           `json:"recommendation"`
	Rationale          string           `json:"rationale"`
	Risks              []string         `json:"risks"`
	Counterarguments   []string         `json:"counterarguments"`
	Conditions         []string         `json:"conditions"`
	Metrics            []ReviewMetric   `json:"metrics"`
	SourcedFacts       []SourcedFact    `json:"sourced_facts"`
	ModelOpinion       string           `json:"model_opinion"`
	Evidence           []ReviewEvidence `json:"evidence"`
	Model              ModelProvenance  `json:"model"`
	GeneratedAt        string           `json:"generated_at"`
}

type ReviewMetric struct {
	Name          string `json:"name"`
	Value         string `json:"value"`
	Unit          string `json:"unit"`
	AsOf          string `json:"as_of"`
	Source        string `json:"source"`
	Deterministic bool   `json:"deterministic"`
}

type ReviewEvidence struct {
	EvidenceID   string  `json:"evidence_id"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	PublishedAt  *string `json:"published_at"`
	AccessedAt   string  `json:"accessed_at"`
	Availability string  `json:"availability"`
	Freshness    string  `json:"freshness"`
	Excerpt      string  `json:"excerpt"`
}

type SourcedFact struct {
	Statement   string   `json:"statement"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type ModelProvenance struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Version  string `json:"version"`
	Runtime  string `json:"runtime"`
}

type PrincipalAttribution struct {
	PrincipalID   string   `json:"principal_id"`
	PrincipalType string   `json:"principal_type"`
	Permissions   []string `json:"permissions"`
}

type ReviewLifecycle struct {
	HistoricalStatus  string  `json:"historical_status"`
	CurrentSnapshotID *string `json:"current_snapshot_id"`
	Outdated          bool    `json:"outdated"`
}

type AdvisoryReview struct {
	ReviewID           string               `json:"review_id"`
	PortfolioID        string               `json:"portfolio_id"`
	SnapshotID         string               `json:"snapshot_id"`
	CalculationVersion string               `json:"calculation_version"`
	Scope              ReviewScope          `json:"scope"`
	Status             string               `json:"status"`
	Recommendation     string               `json:"recommendation"`
	Rationale          string               `json:"rationale"`
	Risks              []string             `json:"risks"`
	Counterarguments   []string             `json:"counterarguments"`
	Conditions         []string             `json:"conditions"`
	Metrics            []ReviewMetric       `json:"metrics"`
	SourcedFacts       []SourcedFact        `json:"sourced_facts"`
	ModelOpinion       string               `json:"model_opinion"`
	Evidence           []ReviewEvidence     `json:"evidence"`
	Model              ModelProvenance      `json:"model"`
	GeneratedAt        string               `json:"generated_at"`
	SubmittedAt        string               `json:"submitted_at"`
	SubmittedBy        PrincipalAttribution `json:"submitted_by"`
	Lifecycle          ReviewLifecycle      `json:"lifecycle"`
}
