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

const reviewBoundarySecret = "review-boundary-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"

func reviewBoundaryFixture(t *testing.T) (http.Handler, *store.Store, domain.ReviewSubmission, string) {
	t.Helper()
	database := apiFixture(t)
	status, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "review.v1", AsOf: time.Now().UTC(), MaxAge: 2 * time.Hour})
	if err != nil || !status.Complete {
		database.Close()
		t.Fatalf("snapshot=%+v err=%v", status, err)
	}
	snapshot, err := database.ReviewSnapshot(context.Background(), "portfolio-api", status.SnapshotID, false)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	submission := domain.ReviewSubmission{
		SnapshotID: snapshot.SnapshotID, CalculationVersion: snapshot.CalculationVersion,
		Scope:  domain.ReviewScope{Type: "portfolio", HoldingIDs: []string{}, InstrumentIDs: []string{}},
		Status: "complete", Recommendation: "hold", Rationale: "rationale", Risks: []string{}, Counterarguments: []string{}, Conditions: []string{}, Metrics: []domain.ReviewMetric{},
		SourcedFacts: []domain.SourcedFact{{Statement: "fact", EvidenceIDs: []string{"e1"}}}, ModelOpinion: "opinion",
		Evidence: []domain.ReviewEvidence{{EvidenceID: "e1", URL: "https://example.test/source", Title: "source", AccessedAt: "2026-01-15T08:00:00.000000000Z", Availability: "available", Freshness: "current", Excerpt: "excerpt"}},
		Model:    domain.ModelProvenance{Provider: "local", Model: "review-model", Version: "v1", Runtime: "hermes-environment"}, GeneratedAt: "2026-01-15T08:00:00.000000000Z",
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
		Machine: auth.MachineConfig{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true},
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return handler, database, submission, snapshot.SnapshotID
}

func reviewBoundaryBody(t *testing.T, submission domain.ReviewSubmission) []byte {
	t.Helper()
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func reviewBoundaryRequest(handler http.Handler, method, target, secret, key, contentType string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertPhaseDError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var body phaseDErrorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("error envelope: %v body=%s", err, response.Body.String())
	}
	if body.Error.Code != code || body.Error.Message == "" || body.Error.RequestID == "" {
		t.Fatalf("error=%+v body=%s", body.Error, response.Body.String())
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "Vary": "Cookie", "X-Content-Type-Options": "nosniff"} {
		if response.Header().Get(header) != want {
			t.Fatalf("header %s=%q want %q", header, response.Header().Get(header), want)
		}
	}
}

func TestReviewHTTPBearerBoundaryMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	cases := []struct {
		name, authorization string
	}{
		{"missing", ""},
		{"malformed-scheme", "Basic " + reviewBoundarySecret},
		{"missing-space", "Bearer" + reviewBoundarySecret},
		{"31-bytes", "Bearer " + strings.Repeat("a", 31)},
		{"32-bytes-wrong", "Bearer " + strings.Repeat("a", 32)},
		{"512-bytes-wrong", "Bearer " + strings.Repeat("a", 512)},
		{"513-bytes", "Bearer " + strings.Repeat("a", 513)},
		{"nonprintable", "Bearer " + strings.Repeat("a", 31) + "\x01"},
		{"wrong-token", "Bearer wrong-review-token-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
			if tc.authorization != "" {
				request.Header.Set("Authorization", tc.authorization)
			}
			request.Header.Set("Idempotency-Key", "bearer-"+tc.name+"-boundary")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertPhaseDError(t, response, http.StatusUnauthorized, "unauthorized")
			if response.Header().Get("WWW-Authenticate") != auth.MachineBearerRealm {
				t.Fatalf("WWW-Authenticate=%q", response.Header().Get("WWW-Authenticate"))
			}
		})
	}
	valid := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "bearer-valid-boundary", "application/json", body)
	if valid.Code != http.StatusCreated {
		t.Fatalf("valid bearer status=%d body=%s", valid.Code, valid.Body.String())
	}
}

func TestReviewHTTPBrowserMachineAmbiguityAndMutationBoundaries(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	loginBody := strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`)
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", loginBody)
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]

	cookieOnly := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
	cookieOnly.AddCookie(cookie)
	cookieOnly.Header.Set("Idempotency-Key", "cookie-only-boundary")
	cookieOnly.Header.Set("Content-Type", "application/json")
	cookieResponse := httptest.NewRecorder()
	handler.ServeHTTP(cookieResponse, cookieOnly)
	assertPhaseDError(t, cookieResponse, http.StatusUnauthorized, "unauthorized")
	if cookieResponse.Header().Get("WWW-Authenticate") != auth.MachineBearerRealm {
		t.Fatalf("cookie-only challenge=%q", cookieResponse.Header().Get("WWW-Authenticate"))
	}

	both := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
	both.AddCookie(cookie)
	both.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
	both.Header.Set("Idempotency-Key", "both-credentials-boundary")
	both.Header.Set("Content-Type", "application/json")
	bothResponse := httptest.NewRecorder()
	handler.ServeHTTP(bothResponse, both)
	assertPhaseDError(t, bothResponse, http.StatusUnauthorized, "unauthorized")

	browserReview := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
	browserReview.AddCookie(cookie)
	browserReview.Header.Set("Idempotency-Key", "browser-review-boundary")
	browserReview.Header.Set("Content-Type", "application/json")
	browserReviewResponse := httptest.NewRecorder()
	handler.ServeHTTP(browserReviewResponse, browserReview)
	assertPhaseDError(t, browserReviewResponse, http.StatusUnauthorized, "unauthorized")

	machineMutation := httptest.NewRequest(http.MethodPut, "/api/v1/portfolios/portfolio-api/holdings/lot-api/thesis", strings.NewReader(`{}`))
	machineMutation.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
	machineMutation.Header.Set("Content-Type", "application/json")
	machineMutationResponse := httptest.NewRecorder()
	handler.ServeHTTP(machineMutationResponse, machineMutation)
	if machineMutationResponse.Code != http.StatusUnauthorized {
		t.Fatalf("machine mutation status=%d body=%s", machineMutationResponse.Code, machineMutationResponse.Body.String())
	}

	csrfMissing := httptest.NewRequest(http.MethodPut, "/api/v1/portfolios/portfolio-api/holdings/lot-api/thesis", strings.NewReader(`{}`))
	csrfMissing.AddCookie(cookie)
	csrfMissing.Header.Set("Content-Type", "application/json")
	csrfResponse := httptest.NewRecorder()
	handler.ServeHTTP(csrfResponse, csrfMissing)
	if csrfResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", csrfResponse.Code, csrfResponse.Body.String())
	}
}

func TestReviewHTTPRoutingAndAuthenticationPrecedesDisclosure(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	valid := "Bearer " + reviewBoundarySecret
	cases := []struct {
		name, method, path, authorization string
		status, allow                     int
	}{
		{"missing-auth-exact", http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", "", http.StatusUnauthorized, 0},
		{"missing-auth-extra", http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews/extra", "", http.StatusUnauthorized, 0},
		{"encoded-segment", http.MethodPost, "/api/v1/%70ortfolios/portfolio-api/reviews", valid, http.StatusBadRequest, 0},
		{"trailing-slash", http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews/", valid, http.StatusBadRequest, 0},
		{"extra-segment", http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews/extra", valid, http.StatusBadRequest, 0},
		{"wrong-portfolio-id", http.MethodPost, "/api/v1/portfolios/other/reviews", valid, http.StatusForbidden, 0},
		{"authenticated-get-post-only", http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews", valid, http.StatusMethodNotAllowed, 1},
		{"unsupported-method", http.MethodPut, "/api/v1/portfolios/portfolio-api/reviews", valid, http.StatusMethodNotAllowed, 1},
		{"head-method", http.MethodHead, "/api/v1/portfolios/portfolio-api/reviews", valid, http.StatusMethodNotAllowed, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := reviewBoundaryRequest(handler, tc.method, tc.path, strings.TrimPrefix(tc.authorization, "Bearer "), "routing-"+tc.name+"-key", "application/json", body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.status == http.StatusUnauthorized {
				assertPhaseDError(t, response, tc.status, "unauthorized")
			}
			if tc.status == http.StatusMethodNotAllowed {
				assertPhaseDError(t, response, tc.status, "method_not_allowed")
				var body phaseDErrorBody
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Message != "Only POST is supported for this resource." {
					t.Fatalf("method error=%+v err=%v", body.Error, err)
				}
			}
			if tc.allow == 1 && response.Header().Get("Allow") != "POST" {
				t.Fatalf("Allow=%q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestReviewHTTPHeaderBoundaryMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	for _, tc := range []struct {
		name   string
		key    string
		keys   []string
		status int
	}{
		{"absent", "", nil, http.StatusBadRequest},
		{"15-bytes", strings.Repeat("a", 15), nil, http.StatusBadRequest},
		{"16-bytes", strings.Repeat("a", 16), nil, http.StatusCreated},
		{"128-bytes", strings.Repeat("b", 128), nil, http.StatusCreated},
		{"129-bytes", strings.Repeat("c", 129), nil, http.StatusBadRequest},
		{"invalid-space", "invalid key-123456", nil, http.StatusBadRequest},
		{"invalid-comma-folded", "key-1234567890,key-2", nil, http.StatusBadRequest},
		{"invalid-control", "key-1234567890\x01", nil, http.StatusBadRequest},
		{"duplicate-lines", "key-duplicate-123", []string{"key-duplicate-123", "key-duplicate-123"}, http.StatusBadRequest},
	} {
		t.Run("idempotency/"+tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
			if tc.key != "" {
				request.Header.Set("iDeMpOtEnCy-KeY", tc.key)
			}
			for _, key := range tc.keys {
				request.Header.Add("Idempotency-Key", key)
			}
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		name, value string
		status      int
	}{
		{"absent", "", http.StatusUnsupportedMediaType},
		{"valid", "application/json", http.StatusCreated},
		{"allowed-parameter", "application/json; charset=utf-8", http.StatusCreated},
		{"invalid-charset", "application/json; charset=latin-1", http.StatusUnsupportedMediaType},
		{"wrong-type", "text/plain", http.StatusUnsupportedMediaType},
		{"malformed-parameter", "application/json; charset", http.StatusUnsupportedMediaType},
	} {
		t.Run("content-type/"+tc.name, func(t *testing.T) {
			key := "content-type-" + tc.name + "-key"
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, tc.value, body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", bytes.NewReader(body))
	duplicate.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
	duplicate.Header.Add("Content-Type", "application/json")
	duplicate.Header.Add("Content-Type", "application/json")
	duplicate.Header.Set("Idempotency-Key", "content-type-duplicate-key")
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("duplicate Content-Type status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
}

func TestReviewHTTPBodyBoundaryMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	base := reviewBoundaryBody(t, submission)
	cases := []struct {
		name   string
		body   []byte
		status int
	}{
		{"empty", nil, http.StatusBadRequest},
		{"malformed-json", []byte(`{"snapshot_id":`), http.StatusBadRequest},
		{"trailing-value", append(append([]byte{}, base...), []byte(` {}`)...), http.StatusBadRequest},
		{"raw-html", []byte(`<html>not-json</html>`), http.StatusBadRequest},
		{"invalid-utf8", append(append([]byte(`{"snapshot_id":"`), 0xff), []byte(`"}`)...), http.StatusBadRequest},
		{"unknown-field", append(append([]byte{}, base[:len(base)-1]...), []byte(`,"unknown":true}`)...), http.StatusBadRequest},
		{"escaped-control", bytes.Replace(base, []byte(`"rationale":"rationale"`), []byte(`"rationale":"bad\u0001text"`), 1), http.StatusBadRequest},
		{"duplicate-top-level", bytes.Replace(base, []byte(`"rationale":"rationale"`), []byte(`"rationale":"rationale","rationale":"again"`), 1), http.StatusBadRequest},
		{"duplicate-scope", bytes.Replace(base, []byte(`"type":"portfolio"`), []byte(`"type":"portfolio","type":"holding"`), 1), http.StatusBadRequest},
		{"duplicate-model", bytes.Replace(base, []byte(`"provider":"local"`), []byte(`"provider":"local","provider":"other"`), 1), http.StatusBadRequest},
		{"duplicate-evidence", bytes.Replace(base, []byte(`"title":"source"`), []byte(`"title":"source","title":"other"`), 1), http.StatusBadRequest},
		{"duplicate-fact", bytes.Replace(base, []byte(`"statement":"fact"`), []byte(`"statement":"fact","statement":"other"`), 1), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "body-"+tc.name+"-key", "application/json", tc.body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	atLimit := append([]byte{}, base...)
	if len(atLimit) > store.ReviewBodyLimit {
		t.Fatal("fixture unexpectedly exceeds body limit")
	}
	atLimit = append(atLimit, bytes.Repeat([]byte{' '}, store.ReviewBodyLimit-len(atLimit))...)
	if len(atLimit) != store.ReviewBodyLimit {
		t.Fatalf("boundary body length=%d", len(atLimit))
	}
	response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "body-exact-limit-key", "application/json", atLimit)
	if response.Code != http.StatusCreated {
		t.Fatalf("exact limit status=%d body=%s", response.Code, response.Body.String())
	}
	overLimit := append(append([]byte{}, atLimit...), ' ')
	response = reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "body-over-limit-key", "application/json", overLimit)
	assertPhaseDError(t, response, http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestReviewHTTPResponseAndValidationMatrix(t *testing.T) {
	handler, database, submission, snapshotID := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "response-success-key", "application/json", body)
	if created.Code != http.StatusCreated || created.Header().Get("Location") == "" || created.Header().Get("Location") == "/" {
		t.Fatalf("created status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	firstBody := append([]byte{}, created.Body.Bytes()...)
	replay := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "response-success-key", "application/json", body)
	if replay.Code != http.StatusCreated || replay.Header().Get("Location") != created.Header().Get("Location") || !bytes.Equal(firstBody, replay.Body.Bytes()) {
		t.Fatalf("replay status=%d location=%q body=%s", replay.Code, replay.Header().Get("Location"), replay.Body.String())
	}
	conflicting := append([]byte{}, body...)
	conflicting = bytes.Replace(conflicting, []byte(`"rationale":"rationale"`), []byte(`"rationale":"different"`), 1)
	conflict := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "response-success-key", "application/json", conflicting)
	assertPhaseDError(t, conflict, http.StatusConflict, "idempotency_conflict")

	for _, tc := range []struct {
		name, field, value string
		status             int
		code               string
	}{
		{"wrong-snapshot", "snapshot_id", "missing-snapshot", http.StatusNotFound, "not_found"},
		{"wrong-version", "calculation_version", "wrong-version", http.StatusBadRequest, "invalid_request"},
		{"invalid-recommendation", "recommendation", "no_recommendation", http.StatusUnprocessableEntity, "incomplete_review"},
		{"invalid-status", "status", "unknown", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := bytes.Replace(body, []byte(`"`+tc.field+`":"`+map[string]string{"snapshot_id": snapshotID, "calculation_version": "review.v1", "recommendation": "hold", "status": "complete"}[tc.field]+`"`), []byte(`"`+tc.field+`":"`+tc.value+`"`), 1)
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "response-"+tc.name+"-key", "application/json", mutated)
			assertPhaseDError(t, response, tc.status, tc.code)
		})
	}
	for _, tc := range []struct {
		name, path string
		status     int
		code       string
	}{
		{"unknown-portfolio", "/api/v1/portfolios/foreign/reviews", http.StatusForbidden, "forbidden"},
		{"unknown-target", "/api/v1/portfolios/portfolio-api/reviews/not-a-collection", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := reviewBoundaryRequest(handler, http.MethodPost, tc.path, reviewBoundarySecret, "response-"+tc.name+"-key", "application/json", body)
			assertPhaseDError(t, response, tc.status, tc.code)
		})
	}
}
