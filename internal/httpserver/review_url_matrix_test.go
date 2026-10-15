package httpserver

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestReviewHTTPURLStorageFidelityMatrix(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		status int
	}{
		{"host-only", "https://example.test", http.StatusCreated},
		{"slash", "https://example.test/", http.StatusCreated},
		{"path-query", "https://example.test/source/demo-001?b=c", http.StatusCreated},
		{"query-only", "https://example.test?", http.StatusCreated},
		{"explicit-default-port", "https://example.test:443/source", http.StatusBadRequest},
		{"explicit-nondefault-port", "https://example.test:444/source", http.StatusBadRequest},
		{"fragment", "https://example.test/source#fragment", http.StatusBadRequest},
		{"userinfo", "https://user@example.test/source", http.StatusBadRequest},
		{"uppercase-host", "https://EXAMPLE.test/source", http.StatusBadRequest},
		{"http", "http://example.test/source", http.StatusBadRequest},
		{"control", "https://example.test/source\x00", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, database, submission, _ := reviewBoundaryFixture(t)
			defer database.Close()
			submission.Evidence[0].URL = tc.raw
			body, err := json.Marshal(submission)
			if err != nil {
				t.Fatal(err)
			}
			key := "url-fidelity-" + tc.name + "-key"
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, "application/json", body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.status != http.StatusCreated {
				return
			}
			var result struct {
				ReviewID string `json:"review_id"`
				Evidence []struct {
					URL string `json:"url"`
				} `json:"evidence"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.ReviewID == "" || len(result.Evidence) != 1 || result.Evidence[0].URL != tc.raw {
				t.Fatalf("round trip=%+v raw=%q", result, tc.raw)
			}
			var host, suffix string
			if err := database.DB.QueryRow(`SELECT url_host,url_suffix FROM review_evidence WHERE review_id=?`, result.ReviewID).Scan(&host, &suffix); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(host, "://") || strings.Contains(suffix, "example.test") || strings.Contains(host+suffix, tc.raw) {
				t.Fatalf("full URL stored in components host=%q suffix=%q", host, suffix)
			}
			for _, table := range []string{"review_evidence", "audit_events", "activity_events"} {
				var payload sql.NullString
				if table == "review_evidence" {
					payload = sql.NullString{}
					err = database.DB.QueryRow(`SELECT url_host||url_suffix FROM review_evidence WHERE review_id=?`, result.ReviewID).Scan(&payload)
				} else if table == "audit_events" {
					err = database.DB.QueryRow(`SELECT payload FROM audit_events WHERE entity_id=?`, result.ReviewID).Scan(&payload)
				} else {
					err = database.DB.QueryRow(`SELECT details FROM activity_events WHERE entity_id=?`, result.ReviewID).Scan(&payload)
				}
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(payload.String, tc.raw) {
					t.Fatalf("full URL found in %s: %q", table, payload.String)
				}
			}
		})
	}
}
