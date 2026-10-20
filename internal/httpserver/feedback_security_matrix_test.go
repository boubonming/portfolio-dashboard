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

func TestFeedbackHTTPRevokedAndExpiredCookiesAreUniformUnauthorized(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		fixture := newFeedbackHTTPFixture(t)
		defer fixture.close()
		oldCookie := *fixture.cookie
		logout := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/logout", nil)
		logout.AddCookie(&oldCookie)
		logout.Header.Set("Origin", "https://example.test")
		logout.Header.Set(auth.CSRFHeaderName, fixture.csrf)
		logoutResponse := httptest.NewRecorder()
		fixture.handler.ServeHTTP(logoutResponse, logout)
		if logoutResponse.Code != http.StatusOK {
			t.Fatalf("logout status=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
		}
		request := httptest.NewRequest(http.MethodPost, "https://example.test"+fixture.validPath(), strings.NewReader(`{"decision":"agree"}`))
		request.AddCookie(&oldCookie)
		request.Header.Set(auth.CSRFHeaderName, fixture.csrf)
		request.Header.Set("Origin", "https://example.test")
		request.Header.Set("Idempotency-Key", "feedback-revoked-cookie-0001")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		assertFeedbackHTTPError(t, response, http.StatusUnauthorized, "unauthorized")
	})

	t.Run("expired", func(t *testing.T) {
		_, database, submission, _ := reviewBoundaryFixture(t)
		defer database.Close()
		review := submitReviewFixture(t, database, submission, auth.MachinePrincipal{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}, "feedback-expired-review-key-0001")
		clock := time.Date(2026, time.January, 9, 10, 0, 0, 0, time.UTC)
		handler, err := NewHandlerWithAuth(database, auth.Config{
			Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(),
			PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Now: func() time.Time { return clock },
			Machine: auth.MachineConfig{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
		login.Header.Set("Content-Type", "application/json")
		login.Header.Set("Origin", "https://example.test")
		loginResponse := httptest.NewRecorder()
		handler.ServeHTTP(loginResponse, login)
		if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
			t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
		}
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || session.CSRF == "" {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Hour)
		request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-api/reviews/"+review.ReviewID+"/feedback", strings.NewReader(`{"decision":"agree"}`))
		request.AddCookie(loginResponse.Result().Cookies()[0])
		request.Header.Set(auth.CSRFHeaderName, session.CSRF)
		request.Header.Set("Origin", "https://example.test")
		request.Header.Set("Idempotency-Key", "feedback-expired-cookie-0001")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertFeedbackHTTPError(t, response, http.StatusUnauthorized, "unauthorized")
	})
}

func TestFeedbackHTTPCorruptedActivityIDFailsClosed(t *testing.T) {
	fixture := newFeedbackHTTPFixture(t)
	defer fixture.close()
	key := "feedback-http-corrupt-activity-0001"
	created := fixture.request(http.MethodPost, fixture.validPath(), `{"decision":"agree"}`, fixture.validHeaders(key))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var feedback domain.ReviewFeedback
	if err := json.Unmarshal(created.Body.Bytes(), &feedback); err != nil || feedback.FeedbackID == "" {
		t.Fatalf("created feedback=%s err=%v", created.Body.String(), err)
	}
	if _, err := fixture.database.DB.Exec(`DROP TRIGGER activity_events_append_only_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.DB.Exec(`UPDATE activity_events SET id='activity-corrupt-id' WHERE entity_id=?`, feedback.FeedbackID); err != nil {
		t.Fatal(err)
	}
	replayed := fixture.request(http.MethodPost, fixture.validPath(), `{"decision":"agree"}`, fixture.validHeaders(key))
	assertFeedbackHTTPError(t, replayed, http.StatusInternalServerError, "internal_error")
}

func TestFeedbackHTTPRealForeignReviewIsNotVisibleThroughOwnedPortfolio(t *testing.T) {
	fixture := newFeedbackHTTPFixture(t)
	defer fixture.close()
	now := time.Now().UTC()
	nowText := canonicalReviewFixtureTimestamp(now)
	if _, err := fixture.database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-foreign", "Foreign", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-foreign", "instrument-api", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?,?)`, "lot-foreign", "portfolio-foreign", "instrument-api", "1", "1", "1", "USD", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.AddQuote(context.Background(), store.QuoteInput{InstrumentID: "instrument-api", Price: "123.45", Currency: "USD", Source: "foreign-fixture", MarketAt: nowText, Basis: "close", Provenance: "foreign test"}); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-foreign", ReportingCurrency: "MYR", CalculationVersion: "review.v1", AsOf: now, MaxAge: 2 * time.Hour})
	if err != nil || !status.Complete {
		t.Fatalf("foreign snapshot=%+v err=%v", status, err)
	}
	snapshot, err := fixture.database.ReviewSnapshot(context.Background(), "portfolio-foreign", status.SnapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	submission := domain.ReviewSubmission{
		SnapshotID: snapshot.SnapshotID, CalculationVersion: snapshot.CalculationVersion,
		Scope:  domain.ReviewScope{Type: "portfolio", HoldingIDs: []string{}, InstrumentIDs: []string{}},
		Status: "incomplete", Recommendation: "no_recommendation", Rationale: "rationale",
		Risks: []string{"risk"}, Counterarguments: []string{"counter"}, Conditions: []string{"condition"},
		SourcedFacts: []domain.SourcedFact{}, ModelOpinion: "opinion", Evidence: []domain.ReviewEvidence{},
		Model:       domain.ModelProvenance{Provider: "local", Model: "review-model", Version: "v1", Runtime: "hermes-environment"},
		GeneratedAt: "2026-01-09T10:00:00.000000000Z",
	}
	principal := auth.MachinePrincipal{PrincipalID: "foreign-machine", PortfolioID: "portfolio-foreign", PortfolioRead: true, ReviewsWrite: true}
	if err := fixture.database.RegisterMachinePrincipalIdentity(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := store.ReviewFingerprint(submission)
	if err != nil {
		t.Fatal(err)
	}
	review, replay, err := fixture.database.SubmitAdvisoryReview(context.Background(), principal, principal.PortfolioID, "feedback-foreign-review-key-0001", fingerprint, submission)
	if err != nil || replay {
		t.Fatalf("foreign review=%+v replay=%v err=%v", review, replay, err)
	}
	response := fixture.request(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews/"+review.ReviewID+"/feedback", `{"decision":"agree"}`, fixture.validHeaders("feedback-foreign-review-0001"))
	assertFeedbackHTTPError(t, response, http.StatusNotFound, "not_found")
}
