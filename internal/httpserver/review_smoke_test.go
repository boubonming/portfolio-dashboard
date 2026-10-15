package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestReviewSubmissionHTTPSmoke(t *testing.T) {
	db := apiFixture(t)
	defer db.Close()
	snapshot, err := db.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "review.v1", AsOf: time.Now().UTC(), MaxAge: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	secret := "machine-secret-with-enough-diversity-0123456789-ABCDEF"
	h, err := NewHandlerWithAuth(db, auth.Config{Username: "u", PasswordHash: sessionPasswordHash(t), SessionSecret: "YXBpLWZpeHR1cmUtc2Vzc2lvbi1zZWNyZXQtd2l0aC1kaXZlcnNpdHktMTIz", PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "machine-review", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true, ReviewsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	body := "{\"snapshot_id\":\"" + snapshot.SnapshotID + "\",\"calculation_version\":\"review.v1\",\"scope\":{\"type\":\"portfolio\",\"holding_ids\":[],\"instrument_ids\":[]},\"status\":\"complete\",\"recommendation\":\"hold\",\"rationale\":\"rationale\",\"risks\":[],\"counterarguments\":[],\"conditions\":[],\"metrics\":[],\"sourced_facts\":[{\"statement\":\"fact\",\"evidence_ids\":[\"e\"]}],\"model_opinion\":\"opinion\",\"evidence\":[{\"evidence_id\":\"e\",\"url\":\"https://example.test/source\",\"title\":\"source\",\"accessed_at\":\"2026-01-15T08:00:00.000000000Z\",\"availability\":\"available\",\"freshness\":\"current\",\"excerpt\":\"excerpt\"}],\"model\":{\"provider\":\"local\",\"model\":\"model\",\"version\":\"v1\",\"runtime\":\"hermes-environment\"},\"generated_at\":\"2026-01-15T08:00:00.000000000Z\"}"
	var parsed domain.ReviewSubmission
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatal(err)
	}
	if snap, err := db.ReviewSnapshot(context.Background(), "portfolio-api", snapshot.SnapshotID, false); err != nil {
		t.Fatal(err)
	} else if err := store.ValidateReviewSubmission(parsed, snap); err != nil {
		t.Fatalf("validation=%v snapshot=%+v", err, snap)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "review-key-123456")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["review_id"] == nil {
		t.Fatalf("response=%s", res.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "review-key-123456")
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("replay status=%d body=%s", res.Code, res.Body.String())
	}
}
