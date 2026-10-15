package httpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestReviewHTTPDomainValidationMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.ReviewSubmission)
		status int
		code   string
	}{
		{"complete-evidence-freshness", func(s *domain.ReviewSubmission) { s.Evidence[0].Freshness = "stale" }, http.StatusUnprocessableEntity, "incomplete_review"},
		{"incomplete-supported", func(s *domain.ReviewSubmission) {
			s.Status = "incomplete"
			s.Recommendation = "no_recommendation"
			s.Evidence[0].Freshness = "stale"
		}, http.StatusCreated, ""},
		{"holding-empty", func(s *domain.ReviewSubmission) {
			s.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{}, InstrumentIDs: []string{}}
		}, http.StatusBadRequest, "invalid_request"},
		{"holding-mismatched-instrument", func(s *domain.ReviewSubmission) {
			s.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{"lot-api"}, InstrumentIDs: []string{"wrong-instrument"}}
		}, http.StatusNotFound, "not_found"},
		{"evidence-invalid-url", func(s *domain.ReviewSubmission) { s.Evidence[0].URL = "http://example.test/source" }, http.StatusBadRequest, "invalid_request"},
		{"fact-unknown-evidence", func(s *domain.ReviewSubmission) { s.SourcedFacts[0].EvidenceIDs = []string{"unknown-evidence"} }, http.StatusBadRequest, "invalid_request"},
		{"model-runtime", func(s *domain.ReviewSubmission) { s.Model.Runtime = "external-runtime" }, http.StatusBadRequest, "invalid_request"},
		{"timestamp", func(s *domain.ReviewSubmission) { s.GeneratedAt = "2026-01-15T08:00:00Z" }, http.StatusBadRequest, "invalid_request"},
		{"decimal", func(s *domain.ReviewSubmission) {
			s.Metrics = []domain.ReviewMetric{{Name: "metric", Value: "01.2", Unit: "USD", AsOf: "2026-01-15T08:00:00.000000000Z", Source: "submitted", Deterministic: true}}
		}, http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, database, submission, _ := reviewBoundaryFixture(t)
			defer database.Close()
			tc.mutate(&submission)
			body, err := json.Marshal(submission)
			if err != nil {
				t.Fatal(err)
			}
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "domain-"+tc.name+"-key", "application/json", body)
			if tc.status == http.StatusCreated {
				if response.Code != tc.status {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				return
			}
			assertPhaseDError(t, response, tc.status, tc.code)
		})
	}
}
