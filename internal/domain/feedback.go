package domain

// FeedbackSubmission is the strict, normalized browser feedback request.
// A nil Note represents both an omitted note and an explicit JSON null.
type FeedbackSubmission struct {
	Decision string  `json:"decision"`
	Note     *string `json:"note"`
}

// ReviewFeedback is the immutable feedback event returned by the API.
type ReviewFeedback struct {
	FeedbackID  string               `json:"feedback_id"`
	PortfolioID string               `json:"portfolio_id"`
	ReviewID    string               `json:"review_id"`
	Decision    string               `json:"decision"`
	Note        *string              `json:"note"`
	OccurredAt  string               `json:"occurred_at"`
	SubmittedBy PrincipalAttribution `json:"submitted_by"`
}
