package domain

// HoldingMutationActor is the immutable, credential-free actor witness stored
// with a holding mutation receipt.
type HoldingMutationActor struct {
	Category    string   `json:"category"`
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
}

type HoldingRecord struct {
	PortfolioID     string  `json:"portfolio_id"`
	HoldingID       string  `json:"holding_id"`
	InstrumentID    string  `json:"instrument_id"`
	Symbol          string  `json:"symbol"`
	Description     string  `json:"description,omitempty"`
	Quantity        string  `json:"quantity"`
	UnitCost        string  `json:"unit_cost"`
	CostBasis       string  `json:"cost_basis"`
	Currency        string  `json:"currency"`
	AccountID       *string `json:"account_id"`
	AcquisitionDate *string `json:"acquisition_date"`
	Lifecycle       string  `json:"lifecycle"`
	CreatedAt       string  `json:"created_at"`
	RemovedAt       *string `json:"removed_at"`
}

type HoldingMutationReceipt struct {
	Operation         string               `json:"operation"`
	Result            string               `json:"result"`
	Holding           HoldingRecord        `json:"holding"`
	PortfolioRevision int                  `json:"portfolio_revision"`
	SnapshotState     string               `json:"snapshot_state"`
	CurrentSnapshotID *string              `json:"current_snapshot_id"`
	Actor             HoldingMutationActor `json:"actor"`
	AuditEventID      string               `json:"audit_event_id"`
	ActivityEventID   string               `json:"activity_event_id"`
	IdempotencyKey    string               `json:"idempotency_key"`
}
