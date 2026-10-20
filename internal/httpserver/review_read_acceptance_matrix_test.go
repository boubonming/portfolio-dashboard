package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

const reviewFixtureTimestampFormat = "2006-01-02T15:04:05.000000000Z"

// These tests intentionally use the shipped review and activity HTTP handlers.
// Store calls only create valid fixture rows; no cursor or authorization helper
// is tested in isolation here.

func reviewMachineRequest(handler http.Handler, method, target, secret string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func reviewPageIDs(t *testing.T, response *httptest.ResponseRecorder) (domain.ReviewPage, []string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var page domain.ReviewPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(page.Items))
	for index, item := range page.Items {
		ids[index] = item.ReviewID
	}
	return page, ids
}

func assertReviewHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertPhaseDError(t, response, status, code)
	if status == http.StatusUnauthorized && response.Header().Get("WWW-Authenticate") != auth.MachineBearerRealm {
		t.Fatalf("WWW-Authenticate=%q", response.Header().Get("WWW-Authenticate"))
	}
}

func submitReviewFixture(t *testing.T, database *store.Store, submission domain.ReviewSubmission, principal auth.MachinePrincipal, key string) domain.AdvisoryReview {
	t.Helper()
	fingerprint, err := store.ReviewFingerprint(submission)
	if err != nil {
		t.Fatal(err)
	}
	result, replay, err := database.SubmitAdvisoryReview(context.Background(), principal, principal.PortfolioID, key, fingerprint, submission)
	if err != nil || replay {
		t.Fatalf("submit %s replay=%t err=%v", key, replay, err)
	}
	return result
}

func boolPointer(value bool) *bool { return &value }

func canonicalReviewFixtureTimestamp(value time.Time) string {
	return value.UTC().Format(reviewFixtureTimestampFormat)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestReviewHTTPBrowserMachineAuthorizationDisclosureMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	validReviewPath := "/api/v1/portfolios/portfolio-api/reviews"
	missingQuery := validReviewPath + "?limit=0"

	// Browser login is exercised through the shipped auth route, not by
	// constructing an in-memory session.
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	var loginPayload struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginPayload); err != nil || loginPayload.CSRFToken == "" {
		t.Fatalf("login payload=%s err=%v", loginResponse.Body.String(), err)
	}

	browser := httptest.NewRequest(http.MethodGet, validReviewPath, nil)
	browser.AddCookie(cookie)
	browserResponse := httptest.NewRecorder()
	handler.ServeHTTP(browserResponse, browser)
	if browserResponse.Code != http.StatusOK {
		t.Fatalf("browser valid status=%d body=%s", browserResponse.Code, browserResponse.Body.String())
	}

	cases := []struct {
		name, authorization, path string
		status                    int
		code                      string
	}{
		{"machine-valid", "Bearer " + reviewBoundarySecret, validReviewPath, http.StatusOK, ""},
		{"machine-malformed", "Bearer", validReviewPath, http.StatusUnauthorized, "unauthorized"},
		{"machine-invalid", "Bearer wrong-review-token-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ", validReviewPath, http.StatusUnauthorized, "unauthorized"},
		{"missing-credentials", "", validReviewPath, http.StatusUnauthorized, "unauthorized"},
		{"auth-precedes-query", "", missingQuery, http.StatusUnauthorized, "unauthorized"},
		{"auth-precedes-method", "", validReviewPath, http.StatusUnauthorized, "unauthorized"},
		{"auth-precedes-missing-review", "", validReviewPath + "/missing-review", http.StatusUnauthorized, "unauthorized"},
		{"auth-precedes-foreign-filter", "", validReviewPath + "?holding_id=foreign-holding", http.StatusUnauthorized, "unauthorized"},
		{"missing-portfolio-read", "Bearer " + reviewBoundarySecret, validReviewPath, http.StatusForbidden, "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "missing-portfolio-read" {
				if _, err := database.DB.Exec(`UPDATE machine_principals SET permission_portfolio_read=0 WHERE id=?`, "review-boundary-machine"); err != nil {
					t.Fatal(err)
				}
				defer database.DB.Exec(`UPDATE machine_principals SET permission_portfolio_read=1 WHERE id=?`, "review-boundary-machine")
			}
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.authorization != "" {
				request.Header.Set("Authorization", tc.authorization)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if tc.code == "" {
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
				}
				return
			}
			assertReviewHTTPError(t, response, tc.status, tc.code)
		})
	}

	both := httptest.NewRequest(http.MethodGet, validReviewPath, nil)
	both.AddCookie(cookie)
	both.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
	bothResponse := httptest.NewRecorder()
	handler.ServeHTTP(bothResponse, both)
	assertReviewHTTPError(t, bothResponse, http.StatusUnauthorized, "unauthorized")

	cookiePost := httptest.NewRequest(http.MethodPost, validReviewPath, bytes.NewReader(body))
	cookiePost.AddCookie(cookie)
	cookiePost.Header.Set("Content-Type", "application/json")
	cookiePost.Header.Set("Idempotency-Key", "browser-post-never-reaches")
	cookiePostResponse := httptest.NewRecorder()
	handler.ServeHTTP(cookiePostResponse, cookiePost)
	assertReviewHTTPError(t, cookiePostResponse, http.StatusUnauthorized, "unauthorized")

	machinePost := httptest.NewRequest(http.MethodPost, validReviewPath, bytes.NewReader(body))
	machinePost.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
	machinePost.Header.Set("Content-Type", "application/json")
	machinePost.Header.Set("Idempotency-Key", "machine-post-auth-matrix")
	machinePostResponse := httptest.NewRecorder()
	handler.ServeHTTP(machinePostResponse, machinePost)
	if machinePostResponse.Code != http.StatusCreated {
		t.Fatalf("machine POST status=%d body=%s", machinePostResponse.Code, machinePostResponse.Body.String())
	}

	// Revocation is observable only through the route after the logout route
	// removes the in-memory session.
	logout := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/logout", nil)
	logout.AddCookie(cookie)
	logout.Header.Set("Origin", "https://example.test")
	logout.Header.Set(auth.CSRFHeaderName, loginPayload.CSRFToken)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	revoked := httptest.NewRequest(http.MethodGet, validReviewPath, nil)
	revoked.AddCookie(cookie)
	revokedResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokedResponse, revoked)
	assertReviewHTTPError(t, revokedResponse, http.StatusUnauthorized, "unauthorized")

	malformedCookie := httptest.NewRequest(http.MethodGet, validReviewPath, nil)
	malformedCookie.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "malformed-session"})
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformedCookie)
	assertReviewHTTPError(t, malformedResponse, http.StatusUnauthorized, "unauthorized")

	// Deleting the persisted identity distinguishes an absent principal from a
	// malformed bearer. Use a second principal with no review references so
	// foreign-key ownership of the fixture's submitted reviews is preserved.
	absentHandler, err := NewHandlerWithAuth(database, auth.Config{Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "absent-review-machine", PortfolioID: "portfolio-api", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DELETE FROM machine_principals WHERE id=?`, "absent-review-machine"); err != nil {
		t.Fatal(err)
	}
	absent := reviewMachineRequest(absentHandler, http.MethodGet, validReviewPath, reviewBoundarySecret)
	assertReviewHTTPError(t, absent, http.StatusForbidden, "forbidden")
}

func TestReviewHTTPBrowserSessionExpiryMatrix(t *testing.T) {
	handler, database, _, _ := reviewBoundaryFixture(t)
	defer database.Close()
	clock := time.Now().UTC()
	// Rebuild the handler with a deterministic clock while retaining the same
	// persisted fixture. Login and expiry remain route-level operations.
	if _, err := database.DB.Exec(`DELETE FROM machine_principals WHERE id=?`, "review-boundary-machine"); err != nil {
		t.Fatal(err)
	}
	var err error
	handler, err = NewHandlerWithAuth(database, auth.Config{Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Second, Now: func() time.Time { return clock }, Machine: auth.MachineConfig{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	clock = clock.Add(2 * time.Second)
	expired := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews", nil)
	expired.AddCookie(cookie)
	expiredResponse := httptest.NewRecorder()
	handler.ServeHTTP(expiredResponse, expired)
	assertReviewHTTPError(t, expiredResponse, http.StatusUnauthorized, "unauthorized")
}

func TestReviewHTTPReviewCursorPersistenceIsolationAndNamespaceMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	body := reviewBoundaryBody(t, submission)
	for index := 0; index < 3; index++ {
		response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "cursor-persistence-"+string(rune('a'+index))+"-key", "application/json", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create[%d] status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	first, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1", reviewBoundarySecret))
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("cursor missing: %+v", first)
	}
	cursor := first.NextCursor

	// Activity and review cursors must not be interchangeable. Login gives the
	// browser credential required by the activity route.
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d", loginResponse.Code)
	}
	cookie := loginResponse.Result().Cookies()[0]
	activityWithReview := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity?limit=1&cursor="+cursor, nil)
	activityWithReview.AddCookie(cookie)
	activityWithReviewResponse := httptest.NewRecorder()
	handler.ServeHTTP(activityWithReviewResponse, activityWithReview)
	if activityWithReviewResponse.Code != http.StatusBadRequest {
		t.Fatalf("review cursor accepted by activity: status=%d body=%s", activityWithReviewResponse.Code, activityWithReviewResponse.Body.String())
	}
	activityRequest := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity?limit=1", nil)
	activityRequest.AddCookie(cookie)
	activityResponse := httptest.NewRecorder()
	handler.ServeHTTP(activityResponse, activityRequest)
	if activityResponse.Code != http.StatusOK {
		t.Fatalf("activity status=%d body=%s", activityResponse.Code, activityResponse.Body.String())
	}
	var activityPage struct {
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(activityResponse.Body.Bytes(), &activityPage); err != nil || activityPage.NextCursor == nil || *activityPage.NextCursor == "" {
		t.Fatalf("activity cursor=%s err=%v", activityResponse.Body.String(), err)
	}
	reviewWithActivity := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1&cursor="+*activityPage.NextCursor, reviewBoundarySecret)
	assertReviewHTTPError(t, reviewWithActivity, http.StatusBadRequest, "invalid_request")

	// Reopen the database and configure the same and a different session secret.
	var databasePath string
	if err := database.DB.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &databasePath); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sameSecretHandler, err := NewHandlerWithAuth(reopened, auth.Config{Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	continued, _ := reviewPageIDs(t, reviewMachineRequest(sameSecretHandler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1&cursor="+cursor, reviewBoundarySecret))
	if len(continued.Items) != 1 {
		t.Fatalf("same-secret cursor did not survive reopen: %+v", continued)
	}
	differentSecretHandler, err := NewHandlerWithAuth(reopened, auth.Config{Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: "c2Vjb25kLXJldmlldy1jdXJzb3Itc2VjcmV0LXdpdGgtZGl2ZXJzaXR5LTEyMw==", PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	different := reviewMachineRequest(differentSecretHandler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1&cursor="+cursor, reviewBoundarySecret)
	assertReviewHTTPError(t, different, http.StatusBadRequest, "invalid_request")
	foreignNow := canonicalReviewFixtureTimestamp(time.Now().UTC())
	if _, err := reopened.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-cursor-foreign", "Cursor Foreign", "MYR", foreignNow, foreignNow); err != nil {
		t.Fatal(err)
	}
	foreignHandler, err := NewHandlerWithAuth(reopened, auth.Config{Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "cursor-foreign-machine", PortfolioID: "portfolio-cursor-foreign", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, query string }{
		{"tampered", "cursor=" + cursor[:len(cursor)-1] + "A"},
		{"noncanonical-envelope", "cursor=" + strings.ToUpper(cursor)},
		{"foreign-portfolio", "cursor=" + cursor},
		{"filter-presence", "cursor=" + cursor + "&outdated=false"},
		{"filter-value", "cursor=" + cursor + "&status=incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/api/v1/portfolios/portfolio-api/reviews?limit=1&" + tc.query
			requestHandler := sameSecretHandler
			if tc.name == "foreign-portfolio" {
				path = "/api/v1/portfolios/portfolio-cursor-foreign/reviews?limit=1&" + tc.query
				requestHandler = foreignHandler
			}
			response := reviewMachineRequest(requestHandler, http.MethodGet, path, reviewBoundarySecret)
			assertReviewHTTPError(t, response, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestReviewHTTPPaginationBoundaryAndInsertionMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	principal := auth.MachinePrincipal{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}
	fixed := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	database.SetClock(func() time.Time { return fixed })
	for index := 0; index < 55; index++ {
		submitReviewFixture(t, database, submission, principal, "pagination-boundary-"+string(rune('a'+index%26))+"-"+string(rune('0'+index/26))+"-key")
	}
	zero, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?holding_id=lot-api", reviewBoundarySecret))
	if len(zero.Items) != 0 || zero.HasMore || zero.NextCursor != "" {
		t.Fatalf("zero boundary=%+v", zero)
	}
	one, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1", reviewBoundarySecret))
	if len(one.Items) != 1 || !one.HasMore || one.NextCursor == "" {
		t.Fatalf("one boundary=%+v", one)
	}
	defaultPage, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret))
	if len(defaultPage.Items) != 20 || !defaultPage.HasMore || defaultPage.NextCursor == "" {
		t.Fatalf("default boundary=%+v", defaultPage)
	}
	fifty, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=50", reviewBoundarySecret))
	if len(fifty.Items) != 50 || !fifty.HasMore || fifty.NextCursor == "" {
		t.Fatalf("fifty boundary=%+v", fifty)
	}
	tooLarge := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=51", reviewBoundarySecret)
	assertReviewHTTPError(t, tooLarge, http.StatusBadRequest, "invalid_request")

	seen := map[string]bool{}
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		path := "/api/v1/portfolios/portfolio-api/reviews?limit=7"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page, ids := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, path, reviewBoundarySecret))
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("duplicate review %s on page %d", id, pageNumber)
			}
			seen[id] = true
		}
		for index := 1; index < len(page.Items); index++ {
			previous, current := page.Items[index-1], page.Items[index]
			if previous.SubmittedAt < current.SubmittedAt || (previous.SubmittedAt == current.SubmittedAt && previous.ReviewID < current.ReviewID) {
				t.Fatalf("ordering page=%d previous=%s/%s current=%s/%s", pageNumber, previous.SubmittedAt, previous.ReviewID, current.SubmittedAt, current.ReviewID)
			}
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatalf("terminal page exposed cursor: %+v", page)
			}
			break
		}
		if page.NextCursor == "" {
			t.Fatalf("more page omitted cursor: %+v", page)
		}
		cursor = page.NextCursor
	}
	if len(seen) != 55 {
		t.Fatalf("traversal count=%d want=55", len(seen))
	}

}

func TestReviewHTTPPaginationConcurrentInsertionKeysetMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	principal := auth.MachinePrincipal{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}
	when := time.Date(2026, 2, 2, 10, 0, 0, 0, time.UTC)
	database.SetClock(func() time.Time { return when })
	firstReview := submitReviewFixture(t, database, submission, principal, "pagination-concurrent-first-key")
	when = time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC)
	secondReview := submitReviewFixture(t, database, submission, principal, "pagination-concurrent-second-key")
	when = time.Date(2026, 2, 2, 8, 0, 0, 0, time.UTC)
	thirdReview := submitReviewFixture(t, database, submission, principal, "pagination-concurrent-third-key")

	first, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1", reviewBoundarySecret))
	if len(first.Items) != 1 || first.Items[0].ReviewID != firstReview.ReviewID || first.NextCursor == "" {
		t.Fatalf("initial boundary=%+v", first)
	}

	// The read goroutine is held at the page boundary while two independent
	// insertion goroutines commit. This is a real concurrent operation, but the
	// channel barrier makes the intended keyset timing deterministic: both rows
	// exist before the boundary query runs, without relying on sleeps.
	readReady := make(chan struct{})
	readGate := make(chan struct{})
	type pageResult struct {
		page domain.ReviewPage
		err  error
	}
	pageResults := make(chan pageResult, 1)
	go func() {
		close(readReady)
		<-readGate
		request := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=2&cursor="+first.NextCursor, reviewBoundarySecret)
		var page domain.ReviewPage
		err := json.Unmarshal(request.Body.Bytes(), &page)
		if request.Code != http.StatusOK && err == nil {
			err = fmt.Errorf("page 2 status=%d body=%s", request.Code, request.Body.String())
		}
		pageResults <- pageResult{page: page, err: err}
	}()
	<-readReady

	insertStart := make(chan struct{})
	beforeInserted := make(chan struct{})
	type insertResult struct {
		name   string
		review domain.AdvisoryReview
		err    error
	}
	insertResults := make(chan insertResult, 2)
	var insertGroup sync.WaitGroup
	insertGroup.Add(1)
	go func() {
		defer insertGroup.Done()
		<-insertStart
		when = time.Date(2026, 2, 2, 11, 0, 0, 0, time.UTC)
		review, _, err := func() (domain.AdvisoryReview, bool, error) {
			fingerprint, fingerprintErr := store.ReviewFingerprint(submission)
			if fingerprintErr != nil {
				return domain.AdvisoryReview{}, false, fingerprintErr
			}
			return database.SubmitAdvisoryReview(context.Background(), principal, principal.PortfolioID, "pagination-concurrent-before-key", fingerprint, submission)
		}()
		insertResults <- insertResult{name: "before", review: review, err: err}
		close(beforeInserted)
	}()
	insertGroup.Add(1)
	go func() {
		defer insertGroup.Done()
		<-insertStart
		<-beforeInserted
		when = time.Date(2026, 2, 2, 8, 30, 0, 0, time.UTC)
		review, _, err := func() (domain.AdvisoryReview, bool, error) {
			fingerprint, fingerprintErr := store.ReviewFingerprint(submission)
			if fingerprintErr != nil {
				return domain.AdvisoryReview{}, false, fingerprintErr
			}
			return database.SubmitAdvisoryReview(context.Background(), principal, principal.PortfolioID, "pagination-concurrent-between-key", fingerprint, submission)
		}()
		insertResults <- insertResult{name: "between", review: review, err: err}
	}()
	close(insertStart)
	insertGroup.Wait()
	close(insertResults)
	inserted := map[string]domain.AdvisoryReview{}
	for result := range insertResults {
		if result.err != nil {
			t.Fatalf("concurrent insertion %s: %v", result.name, result.err)
		}
		if result.review.ReviewID == "" {
			t.Fatalf("concurrent insertion %s returned no review", result.name)
		}
		inserted[result.name] = result.review
	}
	if len(inserted) != 2 {
		t.Fatalf("collected %d concurrent insertion results", len(inserted))
	}
	close(readGate)
	result := <-pageResults
	if result.err != nil {
		t.Fatal(result.err)
	}
	page2 := result.page
	if len(page2.Items) != 2 || page2.Items[0].ReviewID != secondReview.ReviewID || page2.Items[1].ReviewID != inserted["between"].ReviewID || page2.NextCursor == "" {
		t.Fatalf("concurrent page2=%+v", page2)
	}
	if page2.Items[0].ReviewID == inserted["before"].ReviewID || page2.Items[1].ReviewID == inserted["before"].ReviewID {
		t.Fatalf("keyset replayed before-boundary review=%s", inserted["before"].ReviewID)
	}

	page3, page3IDs := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=2&cursor="+page2.NextCursor, reviewBoundarySecret))
	if len(page3IDs) != 1 || len(page3.Items) != 1 || page3.Items[0].ReviewID != thirdReview.ReviewID || page3.HasMore || page3.NextCursor != "" {
		t.Fatalf("concurrent page3=%+v", page3)
	}
	seen := map[string]bool{}
	for _, item := range append(append([]domain.AdvisoryReview{}, page2.Items...), page3.Items...) {
		if seen[item.ReviewID] {
			t.Fatalf("concurrent traversal duplicated review=%s", item.ReviewID)
		}
		seen[item.ReviewID] = true
	}
	for _, reviewID := range []string{secondReview.ReviewID, inserted["between"].ReviewID, thirdReview.ReviewID} {
		if !seen[reviewID] {
			t.Fatalf("concurrent traversal omitted review=%s", reviewID)
		}
	}
}

func TestReviewHTTPFilterCombinationAndCursorBindingMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	principal := auth.MachinePrincipal{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}
	completeHolding := submission
	completeHolding.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{"lot-api"}, InstrumentIDs: []string{"instrument-api"}}
	incomplete := submission
	incomplete.Status = "incomplete"
	incomplete.Recommendation = "no_recommendation"
	incomplete.SourcedFacts = []domain.SourcedFact{}
	incomplete.Evidence = []domain.ReviewEvidence{}
	oldCompleteHolding := submitReviewFixture(t, database, completeHolding, principal, "filter-old-complete-holding-key")
	oldIncompletePortfolio := submitReviewFixture(t, database, incomplete, principal, "filter-old-incomplete-portfolio-key")
	oldIncompleteHolding := submitReviewFixture(t, database, func() domain.ReviewSubmission { value := incomplete; value.Scope = completeHolding.Scope; return value }(), principal, "filter-old-incomplete-holding-key")

	// Add a newer immutable snapshot through the production/store setup. The
	// changed quote and calculation version ensure this is a distinct snapshot;
	// all three reviews above remain historical and the review below is current.
	currentNow := time.Now().UTC()
	if _, err := database.AddQuote(context.Background(), store.QuoteInput{ID: "filter-current-quote", InstrumentID: "instrument-api", Price: "124.4500", Currency: "USD", Source: "fixture", MarketAt: canonicalReviewFixtureTimestamp(currentNow), Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	currentStatus, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "review.v2", AsOf: currentNow, MaxAge: 2 * time.Hour})
	if err != nil || !currentStatus.Complete {
		t.Fatalf("current snapshot=%+v err=%v", currentStatus, err)
	}
	currentSnapshot, err := database.ReviewSnapshot(context.Background(), "portfolio-api", currentStatus.SnapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	currentSubmission := completeHolding
	currentSubmission.SnapshotID = currentSnapshot.SnapshotID
	currentSubmission.CalculationVersion = currentSnapshot.CalculationVersion
	currentReview := submitReviewFixture(t, database, currentSubmission, principal, "filter-current-complete-holding-key")
	if currentReview.SnapshotID != currentSnapshot.SnapshotID {
		t.Fatalf("current review snapshot=%s want=%s", currentReview.SnapshotID, currentSnapshot.SnapshotID)
	}

	cases := []struct {
		name, query string
		expected    []string
		holding     string
		status      string
		outdated    *bool
	}{
		{"holding", "holding_id=lot-api", []string{oldCompleteHolding.ReviewID, oldIncompleteHolding.ReviewID, currentReview.ReviewID}, "lot-api", "", nil},
		{"status-complete", "status=complete", []string{oldCompleteHolding.ReviewID, currentReview.ReviewID}, "", "complete", nil},
		{"status-incomplete", "status=incomplete", []string{oldIncompletePortfolio.ReviewID, oldIncompleteHolding.ReviewID}, "", "incomplete", nil},
		{"outdated-absent", "", []string{oldCompleteHolding.ReviewID, oldIncompletePortfolio.ReviewID, oldIncompleteHolding.ReviewID, currentReview.ReviewID}, "", "", nil},
		{"outdated-true", "outdated=true", []string{oldCompleteHolding.ReviewID, oldIncompletePortfolio.ReviewID, oldIncompleteHolding.ReviewID}, "", "", boolPointer(true)},
		{"outdated-false", "outdated=false", []string{currentReview.ReviewID}, "", "", boolPointer(false)},
		{"holding-status-complete", "holding_id=lot-api&status=complete", []string{oldCompleteHolding.ReviewID, currentReview.ReviewID}, "lot-api", "complete", nil},
		{"holding-status-incomplete", "holding_id=lot-api&status=incomplete", []string{oldIncompleteHolding.ReviewID}, "lot-api", "incomplete", nil},
		{"holding-outdated-true", "holding_id=lot-api&outdated=true", []string{oldCompleteHolding.ReviewID, oldIncompleteHolding.ReviewID}, "lot-api", "", boolPointer(true)},
		{"holding-outdated-false", "holding_id=lot-api&outdated=false", []string{currentReview.ReviewID}, "lot-api", "", boolPointer(false)},
		{"status-outdated-complete", "status=complete&outdated=true", []string{oldCompleteHolding.ReviewID}, "", "complete", boolPointer(true)},
		{"status-outdated-incomplete", "status=incomplete&outdated=true", []string{oldIncompletePortfolio.ReviewID, oldIncompleteHolding.ReviewID}, "", "incomplete", boolPointer(true)},
		{"holding-status-outdated-complete", "holding_id=lot-api&status=complete&outdated=false", []string{currentReview.ReviewID}, "lot-api", "complete", boolPointer(false)},
		{"holding-status-outdated-incomplete", "holding_id=lot-api&status=incomplete&outdated=false", []string{}, "lot-api", "incomplete", boolPointer(false)},
		{"foreign-holding-empty", "holding_id=foreign-holding", []string{}, "foreign-holding", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/api/v1/portfolios/portfolio-api/reviews"
			if tc.query != "" {
				path += "?" + tc.query
			}
			page, ids := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, path, reviewBoundarySecret))
			got := append([]string(nil), ids...)
			want := append([]string(nil), tc.expected...)
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("filter %s returned exact IDs=%v want=%v", tc.query, got, want)
			}
			for _, item := range page.Items {
				if tc.holding != "" && !containsString(item.Scope.HoldingIDs, tc.holding) {
					t.Fatalf("filter %s returned item %s without holding %s", tc.query, item.ReviewID, tc.holding)
				}
				if tc.status != "" && item.Status != tc.status {
					t.Fatalf("filter %s returned item %s status=%s want=%s", tc.query, item.ReviewID, item.Status, tc.status)
				}
				if tc.outdated != nil && item.Lifecycle.Outdated != *tc.outdated {
					t.Fatalf("filter %s returned item %s outdated=%t want=%t", tc.query, item.ReviewID, item.Lifecycle.Outdated, *tc.outdated)
				}
				if item.Lifecycle.CurrentSnapshotID == nil || *item.Lifecycle.CurrentSnapshotID != currentSnapshot.SnapshotID {
					t.Fatalf("filter %s returned item %s current snapshot=%v want=%s", tc.query, item.ReviewID, item.Lifecycle.CurrentSnapshotID, currentSnapshot.SnapshotID)
				}
				if item.Lifecycle.Outdated != (item.SnapshotID != currentSnapshot.SnapshotID) {
					t.Fatalf("filter %s returned item %s lifecycle outdated=%t snapshot=%s current=%s", tc.query, item.ReviewID, item.Lifecycle.Outdated, item.SnapshotID, currentSnapshot.SnapshotID)
				}
			}
		})
	}

	first, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1", reviewBoundarySecret))
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("filter cursor unavailable: %+v", first)
	}
	for _, query := range []string{
		"limit=1&holding_id=lot-api",
		"limit=1&status=incomplete",
		"limit=1&outdated=false",
		"limit=1&outdated=true",
	} {
		response := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?"+query+"&cursor="+first.NextCursor, reviewBoundarySecret)
		assertReviewHTTPError(t, response, http.StatusBadRequest, "invalid_request")
	}
}

func TestReviewHTTPForeignPortfolioFilterDoesNotDisclose(t *testing.T) {
	handler, database, _, _ := reviewBoundaryFixture(t)
	defer database.Close()
	foreignNow := canonicalReviewFixtureTimestamp(time.Now().UTC())
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-api-other", "Other", "MYR", foreignNow, foreignNow); err != nil {
		t.Fatal(err)
	}
	response := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?holding_id=foreign-holding", reviewBoundarySecret)
	if response.Code != http.StatusOK {
		t.Fatalf("foreign filter status=%d body=%s", response.Code, response.Body.String())
	}
	foreign := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api-other/reviews?holding_id=lot-api", reviewBoundarySecret)
	assertReviewHTTPError(t, foreign, http.StatusForbidden, "forbidden")
}

func TestReviewHTTPPathAndMethodErrorMatrixRemainsNested(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	foreignNow := canonicalReviewFixtureTimestamp(time.Now().UTC().Add(-time.Hour))
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-api-other", "Other", "MYR", foreignNow, foreignNow); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-api-other", "FIX-OTHER", "Foreign fixture instrument", "USD", foreignNow); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-api-other", "instrument-api-other", foreignNow); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?,?)`, "lot-api-other", "portfolio-api-other", "instrument-api-other", "0.125", "1.20", "0.15", "USD", foreignNow, foreignNow); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddQuote(context.Background(), store.QuoteInput{ID: "quote-api-other", InstrumentID: "instrument-api-other", Price: "123.4500", Currency: "USD", Source: "fixture", MarketAt: foreignNow, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddFXRate(context.Background(), store.FXInput{ID: "fx-api-other", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4.5", Source: "fixture", MarketAt: foreignNow, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	foreignSnapshotStatus, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api-other", ReportingCurrency: "MYR", CalculationVersion: "foreign.v1", AsOf: time.Now().UTC(), MaxAge: 2 * time.Hour})
	if err != nil || !foreignSnapshotStatus.Complete {
		t.Fatalf("foreign snapshot=%+v err=%v", foreignSnapshotStatus, err)
	}
	if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,active,created_at) VALUES(?,?,?,?,?,?)`, "foreign-review-machine", "portfolio-api-other", 1, 1, 1, foreignNow); err != nil {
		t.Fatal(err)
	}
	foreignSnapshot, err := database.ReviewSnapshot(context.Background(), "portfolio-api-other", foreignSnapshotStatus.SnapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	foreignSubmission := submission
	foreignSubmission.SnapshotID = foreignSnapshot.SnapshotID
	foreignSubmission.CalculationVersion = foreignSnapshot.CalculationVersion
	foreignReview := submitReviewFixture(t, database, foreignSubmission, auth.MachinePrincipal{PrincipalID: "foreign-review-machine", PortfolioID: "portfolio-api-other", PortfolioRead: true, ReviewsWrite: true}, "foreign-review-owned-by-other-key")
	if foreignReview.PortfolioID != "portfolio-api-other" {
		t.Fatalf("foreign review owner=%s", foreignReview.PortfolioID)
	}

	foreignResponse := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews/"+foreignReview.ReviewID, reviewBoundarySecret)
	assertReviewHTTPError(t, foreignResponse, http.StatusNotFound, "not_found")
	if strings.Contains(foreignResponse.Body.String(), foreignReview.ReviewID) || strings.Contains(foreignResponse.Body.String(), "portfolio-api-other") {
		t.Fatalf("foreign review disclosed in body=%s", foreignResponse.Body.String())
	}
	for _, tc := range []struct {
		name, method, path string
		status             int
		code               string
	}{
		{"nonexistent-review", http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews/missing-review", http.StatusNotFound, "not_found"},
		{"wrong-method", http.MethodPut, "/api/v1/portfolios/portfolio-api/reviews", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"detail-wrong-method", http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews/review-id", http.StatusMethodNotAllowed, "method_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := reviewMachineRequest(handler, tc.method, tc.path, reviewBoundarySecret)
			assertReviewHTTPError(t, response, tc.status, tc.code)
			if tc.status == http.StatusMethodNotAllowed && response.Header().Get("Allow") == "" {
				t.Fatal("method error omitted Allow")
			}
		})
	}
}

func TestReviewHTTPCursorPayloadNeverUsesUnrelatedQueryEncoding(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	for index := 0; index < 2; index++ {
		response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "encoding-matrix-"+string(rune('a'+index))+"-key", "application/json", reviewBoundaryBody(t, submission))
		if response.Code != http.StatusCreated {
			t.Fatal(response.Body.String())
		}
	}
	page, _ := reviewPageIDs(t, reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1", reviewBoundarySecret))
	for _, value := range []string{"%" + page.NextCursor, "+" + page.NextCursor, page.NextCursor + "%20"} {
		response := reviewMachineRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?cursor="+value, reviewBoundarySecret)
		assertReviewHTTPError(t, response, http.StatusBadRequest, "invalid_request")
	}
}
