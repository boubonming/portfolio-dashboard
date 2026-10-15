package httpserver

import (
	"bytes"
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

func TestReviewHTTPReplayPreservesLocationAndBodyAfterSnapshotPayloadUnavailable(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	status, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "review.v1", AsOf: time.Now().UTC(), MaxAge: 2 * time.Hour})
	if err != nil || !status.Complete {
		t.Fatalf("snapshot=%+v err=%v", status, err)
	}
	snapshot, err := database.ReviewSnapshot(context.Background(), "portfolio-api", status.SnapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	secret := "machine-secret-with-enough-diversity-0123456789-ABCDEF"
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: "YXBpLWZpeHR1cmUtc2Vzc2lvbi1zZWNyZXQtd2l0aC1kaXZlcnNpdHktMTIz",
		PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
		Machine: auth.MachineConfig{PrincipalID: "machine-review-http", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true, ReviewsWrite: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := database.MachineReviewPrincipalAllowedForRuntime(context.Background(), auth.MachinePrincipal{PrincipalID: "machine-review-http", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}, "portfolio-api")
	if err != nil || !allowed {
		t.Fatalf("machine fixture not allowed=%v err=%v", allowed, err)
	}
	submission := domain.ReviewSubmission{
		SnapshotID: snapshot.SnapshotID, CalculationVersion: snapshot.CalculationVersion,
		Scope:  domain.ReviewScope{Type: "portfolio", HoldingIDs: []string{}, InstrumentIDs: []string{}},
		Status: "complete", Recommendation: "hold", Rationale: "rationale", Risks: []string{}, Counterarguments: []string{}, Conditions: []string{}, Metrics: []domain.ReviewMetric{},
		SourcedFacts: []domain.SourcedFact{{Statement: "fact", EvidenceIDs: []string{"e1"}}}, ModelOpinion: "opinion",
		Evidence: []domain.ReviewEvidence{{EvidenceID: "e1", URL: "https://example.test/source?", Title: "source", AccessedAt: "2026-01-15T08:00:00.000000000Z", Availability: "available", Freshness: "current", Excerpt: "excerpt"}},
		Model:    domain.ModelProvenance{Provider: "local", Model: "review-model", Version: "v1", Runtime: "hermes-environment"}, GeneratedAt: "2026-01-15T08:00:00.000000000Z",
	}
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateReviewSubmission(submission, snapshot); err != nil {
		t.Fatalf("fixture submission validation=%v snapshot=%+v", err, snapshot)
	}
	doRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "http-replay-key-1234")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	first := doRequest()
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	reviewID := strings.TrimPrefix(first.Header().Get("Location"), "/api/v1/portfolios/portfolio-api/reviews/")
	if reviewID == "" || reviewID == first.Header().Get("Location") || !strings.HasPrefix(first.Header().Get("Location"), "/api/v1/portfolios/portfolio-api/reviews/") {
		t.Fatalf("invalid Location=%q", first.Header().Get("Location"))
	}
	if _, err := database.DB.Exec(`DROP TRIGGER snapshots_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`UPDATE snapshots SET payload='{}' WHERE id=?`, snapshot.SnapshotID); err != nil {
		t.Fatal(err)
	}
	second := doRequest()
	if second.Code != http.StatusCreated {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Location") != first.Header().Get("Location") {
		t.Fatalf("replay Location=%q first=%q", second.Header().Get("Location"), first.Header().Get("Location"))
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("replay body changed:\nfirst=%s\nsecond=%s", first.Body.String(), second.Body.String())
	}
	if _, err := database.DB.Exec(`DROP TRIGGER phase_d_review_evidence_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DELETE FROM review_evidence WHERE review_id=?`, reviewID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	corrupt := doRequest()
	if corrupt.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt replay status=%d body=%s", corrupt.Code, corrupt.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(corrupt.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "internal_error" {
		t.Fatalf("corrupt replay error=%q body=%s", envelope.Error.Code, corrupt.Body.String())
	}
	if _, err := database.DB.Exec(`SELECT 1 FROM advisory_reviews WHERE id=?`, reviewID); err != nil {
		t.Fatal(err)
	}
}
