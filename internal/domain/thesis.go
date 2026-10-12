package domain

// ThesisSource is a user-supplied, typed reference. PublicationDate is an
// ISO calendar date and is nil when the source has no known publication date.
type ThesisSource struct {
	Label           string  `json:"label"`
	URL             string  `json:"url"`
	PublicationDate *string `json:"publication_date,omitempty"`
}

// Thesis is the current private investment thesis for one holding.
type Thesis struct {
	ID                    string         `json:"id"`
	PortfolioID           string         `json:"portfolio_id"`
	HoldingID             string         `json:"holding_id"`
	InstrumentID          string         `json:"instrument_id"`
	Summary               string         `json:"summary"`
	IntendedHoldingPeriod string         `json:"intended_holding_period"`
	AllocationLimit       *string        `json:"allocation_limit,omitempty"`
	ReasonsToHold         []string       `json:"reasons_to_hold"`
	Risks                 []string       `json:"risks"`
	ReasonsToSell         []string       `json:"reasons_to_sell"`
	ConditionsToChange    []string       `json:"conditions_to_change"`
	Sources               []ThesisSource `json:"sources"`
	Revision              int64          `json:"revision"`
	RevisionID            string         `json:"revision_id"`
	UpdatedAt             string         `json:"updated_at"`
}

// ThesisRevision is an immutable historical thesis revision.
type ThesisRevision struct {
	Thesis
	CreatedAt string `json:"created_at"`
}

// ThesisUpdate is the complete replacement document used by the API and store.
// ExpectedRevision is zero for create and must match the current revision for
// update. It is deliberately an integer to keep optimistic concurrency explicit.
type ThesisUpdate struct {
	PortfolioID           string         `json:"portfolio_id"`
	HoldingID             string         `json:"holding_id"`
	Summary               string         `json:"summary"`
	IntendedHoldingPeriod string         `json:"intended_holding_period"`
	AllocationLimit       *string        `json:"allocation_limit,omitempty"`
	ReasonsToHold         []string       `json:"reasons_to_hold"`
	Risks                 []string       `json:"risks"`
	ReasonsToSell         []string       `json:"reasons_to_sell"`
	ConditionsToChange    []string       `json:"conditions_to_change"`
	Sources               []ThesisSource `json:"sources"`
	ExpectedRevision      int64          `json:"expected_revision"`
}
