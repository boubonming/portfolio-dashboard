package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestReviewHTTPTextControlMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()

	mutations := []struct {
		name string
		edit func(*domain.ReviewSubmission, string)
	}{
		{"rationale", func(s *domain.ReviewSubmission, value string) { s.Rationale = value }},
		{"model-opinion", func(s *domain.ReviewSubmission, value string) { s.ModelOpinion = value }},
		{"risk", func(s *domain.ReviewSubmission, value string) { s.Risks = []string{value} }},
		{"counterargument", func(s *domain.ReviewSubmission, value string) { s.Counterarguments = []string{value} }},
		{"condition", func(s *domain.ReviewSubmission, value string) { s.Conditions = []string{value} }},
		{"metric-name", func(s *domain.ReviewSubmission, value string) {
			s.Metrics = []domain.ReviewMetric{{Name: value, Value: "1", Unit: "USD", AsOf: "2026-01-02T12:00:00.000000000Z", Source: "submitted", Deterministic: true}}
		}},
		{"metric-unit", func(s *domain.ReviewSubmission, value string) {
			s.Metrics = []domain.ReviewMetric{{Name: "metric", Value: "1", Unit: value, AsOf: "2026-01-02T12:00:00.000000000Z", Source: "submitted", Deterministic: true}}
		}},
		{"fact-statement", func(s *domain.ReviewSubmission, value string) { s.SourcedFacts[0].Statement = value }},
		{"evidence-title", func(s *domain.ReviewSubmission, value string) { s.Evidence[0].Title = value }},
		{"evidence-excerpt", func(s *domain.ReviewSubmission, value string) { s.Evidence[0].Excerpt = value }},
		{"model-provider", func(s *domain.ReviewSubmission, value string) { s.Model.Provider = value }},
		{"model-name", func(s *domain.ReviewSubmission, value string) { s.Model.Model = value }},
		{"model-version", func(s *domain.ReviewSubmission, value string) { s.Model.Version = value }},
	}
	controls := []string{"\x00", "\x1f", "\x7f", "\u0080", "\u009f"}
	before := reviewHTTPAccountingCounts(t, database)
	for _, mutation := range mutations {
		for _, control := range controls {
			mutation, control := mutation, control
			t.Run(fmt.Sprintf("%s/U%04X", mutation.name, []rune(control)[0]), func(t *testing.T) {
				caseSubmission := submission
				encoded, err := json.Marshal(submission)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &caseSubmission); err != nil {
					t.Fatal(err)
				}
				mutation.edit(&caseSubmission, "safe"+control+"text")
				body, err := json.Marshal(caseSubmission)
				if err != nil {
					t.Fatal(err)
				}
				response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "control-"+mutation.name+fmt.Sprintf("-%04x", []rune(control)[0]), "application/json", body)
				assertPhaseDError(t, response, http.StatusBadRequest, "invalid_request")
				if got := reviewHTTPAccountingCounts(t, database); !reflect.DeepEqual(got, before) {
					t.Fatalf("invalid control persisted rows: before=%v after=%v", before, got)
				}
			})
		}
	}
}

func TestReviewHTTPAllowedWhitespaceMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"newline", "line\nbreak"},
		{"tab", "tab\tvalue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseSubmission := submission
			caseSubmission.Rationale = tc.value
			body, err := json.Marshal(caseSubmission)
			if err != nil {
				t.Fatal(err)
			}
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "allowed-whitespace-"+tc.name+"-key", "application/json", body)
			if response.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func reviewHTTPAccountingCounts(t *testing.T, database *store.Store) map[string]int {
	t.Helper()
	counts := make(map[string]int, 3)
	for _, table := range []string{"advisory_reviews", "review_idempotency", "audit_events"} {
		var count int
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}
