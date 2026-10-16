package httpserver

import (
	"bytes"
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

type feedbackHTTPFixture struct {
	handler  http.Handler
	database *store.Store
	cookie   *http.Cookie
	csrf     string
	review   domain.AdvisoryReview
}

func newFeedbackHTTPFixture(t *testing.T) *feedbackHTTPFixture {
	t.Helper()
	handler, database, submission, _ := reviewBoundaryFixture(t)
	review := submitReviewFixture(t, database, submission, auth.MachinePrincipal{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}, "feedback-matrix-review-key-0001")
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		database.Close()
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || session.CSRF == "" {
		database.Close()
		t.Fatalf("login body=%s err=%v", loginResponse.Body.String(), err)
	}
	return &feedbackHTTPFixture{handler: handler, database: database, cookie: loginResponse.Result().Cookies()[0], csrf: session.CSRF, review: review}
}

func (f *feedbackHTTPFixture) close() { _ = f.database.Close() }

func (f *feedbackHTTPFixture) request(method, path, body string, headers map[string][]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	req.AddCookie(f.cookie)
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	return response
}

func (f *feedbackHTTPFixture) validHeaders(key string) map[string][]string {
	return map[string][]string{
		auth.CSRFHeaderName: {f.csrf}, "Origin": {"https://example.test"},
		"Idempotency-Key": {key}, "Content-Type": {"application/json"},
	}
}

func (f *feedbackHTTPFixture) validPath() string {
	return "/api/v1/portfolios/portfolio-api/reviews/" + f.review.ReviewID + "/feedback"
}

func assertFeedbackHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertPhaseDError(t, response, status, code)
	if response.Header().Get("Allow") != "" && status != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected Allow=%q", response.Header().Get("Allow"))
	}
	if response.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("feedback must not challenge with WWW-Authenticate: %q", response.Header().Get("WWW-Authenticate"))
	}
}

func TestFeedbackHTTPCanonicalSuccessDecisionNoteMatrix(t *testing.T) {
	f := newFeedbackHTTPFixture(t)
	defer f.close()
	f.database.SetClock(func() time.Time { return time.Date(2026, time.January, 9, 10, 11, 12, 123456789, time.UTC) })
	cases := []struct {
		name, decision, bodyNote, wantNote string
	}{
		{"agree-omitted", "agree", ``, ""},
		{"agree-null", "agree", `,"note":null`, ""},
		{"disagree-present", "disagree", `,"note":"quote \" marker"`, "quote \" marker"},
		{"defer-omitted", "defer", ``, ""},
		{"request-another-review-present", "request_another_review", `,"note":"needs review"`, "needs review"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := f.validPath()
			if index > 0 {
				// A unique key on the same review is sufficient because each row has
				// a distinct canonical payload; use one review per decision below.
				path = f.validPath()
			}
			body := `{"decision":"` + tc.decision + `"` + tc.bodyNote + `}`
			response := f.request(http.MethodPost, path, body, f.validHeaders("feedback-canonical-"+strings.ReplaceAll(tc.name, "-", "")+"-0001"))
			if response.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			for name, want := range map[string]string{"Cache-Control": "no-store", "Vary": "Cookie", "X-Content-Type-Options": "nosniff", "Content-Type": "application/json"} {
				if got := response.Header().Get(name); got != want {
					t.Fatalf("header %s=%q want %q", name, got, want)
				}
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"feedback_id", "portfolio_id", "review_id", "decision", "note", "occurred_at", "submitted_by"} {
				if _, ok := wire[field]; !ok {
					t.Fatalf("missing response field %q: %s", field, response.Body.String())
				}
			}
			if len(wire) != 7 || string(wire["note"]) == "" && tc.wantNote != "" {
				t.Fatalf("response fields=%v", wire)
			}
			var result domain.ReviewFeedback
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.FeedbackID == "" || result.PortfolioID != "portfolio-api" || result.ReviewID != f.review.ReviewID || result.Decision != tc.decision || result.OccurredAt != "2026-01-09T10:11:12.123456789Z" || result.SubmittedBy.PrincipalID != "local-owner" || result.SubmittedBy.PrincipalType != "browser_user" || len(result.SubmittedBy.Permissions) != 1 || result.SubmittedBy.Permissions[0] != "portfolio:read" {
				t.Fatalf("result=%+v", result)
			}
			if tc.wantNote == "" && result.Note != nil {
				t.Fatalf("note=%q want nil", *result.Note)
			}
			if tc.wantNote != "" && (result.Note == nil || *result.Note != tc.wantNote) {
				t.Fatalf("note=%v want %q", result.Note, tc.wantNote)
			}
			if strings.Contains(response.Body.String(), "<script>") || strings.Contains(response.Body.String(), "&\nmarker") {
				t.Fatalf("response did not JSON-escape note: %s", response.Body.String())
			}
		})
	}
}

func TestFeedbackHTTPAuthenticationCSRFOriginMethodAndPathMatrix(t *testing.T) {
	f := newFeedbackHTTPFixture(t)
	defer f.close()
	validPath := f.validPath()
	body := `{"decision":"agree"}`
	for _, tc := range []struct {
		name, cookie, authorization string
		status                      int
		code                        string
	}{
		{"missing-cookie", "missing", "", http.StatusUnauthorized, "unauthorized"},
		{"invalid-cookie", "invalid", "", http.StatusUnauthorized, "unauthorized"},
		{"bearer-only", "valid", "Bearer " + reviewBoundarySecret, http.StatusUnauthorized, "unauthorized"},
		{"malformed-bearer", "valid", "Bearer", http.StatusUnauthorized, "unauthorized"},
		{"cookie-and-bearer", "valid", "Bearer " + reviewBoundarySecret, http.StatusUnauthorized, "unauthorized"},
	} {
		t.Run("auth/"+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://example.test"+validPath, strings.NewReader(body))
			if tc.cookie == "valid" {
				req.AddCookie(f.cookie)
			} else if tc.cookie == "invalid" {
				req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "invalid"})
			}
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			response := httptest.NewRecorder()
			f.handler.ServeHTTP(response, req)
			assertFeedbackHTTPError(t, response, tc.status, tc.code)
		})
	}
	duplicateAuthorization := httptest.NewRequest(http.MethodPost, "https://example.test"+validPath, strings.NewReader(body))
	duplicateAuthorization.AddCookie(f.cookie)
	duplicateAuthorization.Header.Add("Authorization", "Bearer "+reviewBoundarySecret)
	duplicateAuthorization.Header.Add("Authorization", "Bearer "+reviewBoundarySecret)
	duplicateAuthorizationResponse := httptest.NewRecorder()
	f.handler.ServeHTTP(duplicateAuthorizationResponse, duplicateAuthorization)
	assertFeedbackHTTPError(t, duplicateAuthorizationResponse, http.StatusUnauthorized, "unauthorized")
	for _, tc := range []struct {
		name, path string
		status     int
		code       string
	}{
		{"foreign-portfolio", "/api/v1/portfolios/foreign/reviews/" + f.review.ReviewID + "/feedback", http.StatusForbidden, "forbidden"},
		{"missing-review", "/api/v1/portfolios/portfolio-api/reviews/missing-review/feedback", http.StatusNotFound, "not_found"},
		{"wrong-method", validPath, http.StatusMethodNotAllowed, "method_not_allowed"},
		{"query", validPath + "?unexpected=1", http.StatusBadRequest, "invalid_request"},
		{"encoded-segment", strings.Replace(validPath, "portfolio-api", "%70ortfolio-api", 1), http.StatusBadRequest, "invalid_request"},
		{"trailing-slash", validPath + "/", http.StatusBadRequest, "invalid_request"},
		{"extra-segment", validPath + "/extra", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run("route/"+tc.name, func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "wrong-method" {
				method = http.MethodGet
			}
			response := f.request(method, tc.path, body, f.validHeaders("feedback-route-"+tc.name+"-0001"))
			assertFeedbackHTTPError(t, response, tc.status, tc.code)
			if tc.name == "wrong-method" && response.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow=%q", response.Header().Get("Allow"))
			}
			if tc.name != "wrong-method" && response.Header().Get("Allow") != "" {
				t.Fatalf("premature Allow disclosure=%q", response.Header().Get("Allow"))
			}
		})
	}
	for _, tc := range []struct {
		name, origin, referer, csrf string
		status                      int
		code                        string
	}{
		{"csrf-missing", "https://example.test", "", "", http.StatusBadRequest, "invalid_request"},
		{"csrf-short", "https://example.test", "", "short", http.StatusForbidden, "forbidden"},
		{"csrf-invalid", "https://example.test", "", strings.Repeat("x", 32), http.StatusForbidden, "forbidden"},
		{"csrf-overlength", "https://example.test", "", strings.Repeat("x", 257), http.StatusForbidden, "forbidden"},
		{"csrf-duplicate", "https://example.test", "", f.csrf, http.StatusBadRequest, "invalid_request"},
		{"origin-missing", "", "", f.csrf, http.StatusForbidden, "forbidden"},
		{"origin-same", "https://example.test", "", f.csrf, http.StatusCreated, ""},
		{"referer-same", "", "https://example.test/page", f.csrf, http.StatusCreated, ""},
		{"origin-wrong", "https://attacker.example", "", f.csrf, http.StatusForbidden, "forbidden"},
		{"referer-wrong", "", "https://attacker.example/page", f.csrf, http.StatusForbidden, "forbidden"},
	} {
		t.Run("csrf-origin/"+tc.name, func(t *testing.T) {
			headers := f.validHeaders("feedback-csrf-" + tc.name + "-0001")
			headers[auth.CSRFHeaderName] = nil
			if tc.csrf != "" {
				headers[auth.CSRFHeaderName] = []string{tc.csrf}
			}
			if tc.name == "csrf-duplicate" {
				headers[auth.CSRFHeaderName] = []string{f.csrf, f.csrf}
			}
			delete(headers, "Origin")
			if tc.origin != "" {
				headers["Origin"] = []string{tc.origin}
			}
			if tc.referer != "" {
				headers["Referer"] = []string{tc.referer}
			}
			response := f.request(http.MethodPost, validPath, body, headers)
			if tc.status == http.StatusCreated {
				if response.Code != tc.status {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
			} else {
				assertFeedbackHTTPError(t, response, tc.status, tc.code)
			}
		})
	}
}

func TestFeedbackHTTPStrictHeaderAndBodyBoundaryMatrix(t *testing.T) {
	f := newFeedbackHTTPFixture(t)
	defer f.close()
	path := f.validPath()
	validBody := `{"decision":"agree"}`
	for _, tc := range []struct {
		name   string
		key    string
		keys   []string
		status int
	}{
		{"missing", "", nil, http.StatusBadRequest},
		{"under", strings.Repeat("a", 15), nil, http.StatusBadRequest},
		{"at-lower", strings.Repeat("a", 16), nil, http.StatusCreated},
		{"at-upper", strings.Repeat("b", 128), nil, http.StatusCreated},
		{"over", strings.Repeat("c", 129), nil, http.StatusBadRequest},
		{"space", "bad key-123456", nil, http.StatusBadRequest},
		{"control", "bad-key-12345\x01", nil, http.StatusBadRequest},
		{"duplicate", "feedback-header-duplicate-0001", []string{"feedback-header-duplicate-0001", "feedback-header-duplicate-0001"}, http.StatusBadRequest},
	} {
		t.Run("idempotency/"+tc.name, func(t *testing.T) {
			headers := f.validHeaders(tc.key)
			if tc.key == "" {
				delete(headers, "Idempotency-Key")
			}
			if len(tc.keys) > 0 {
				headers["Idempotency-Key"] = tc.keys
			}
			response := f.request(http.MethodPost, path, validBody, headers)
			if tc.status == http.StatusCreated {
				if response.Code != tc.status {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
			} else {
				assertFeedbackHTTPError(t, response, tc.status, "invalid_request")
			}
		})
	}
	for _, tc := range []struct {
		name         string
		contentTypes []string
		status       int
	}{
		{"missing", nil, http.StatusUnsupportedMediaType},
		{"wrong", []string{"text/plain"}, http.StatusUnsupportedMediaType},
		{"charset-utf8", []string{"application/json; charset=utf-8"}, http.StatusCreated},
		{"charset-wrong", []string{"application/json; charset=latin-1"}, http.StatusUnsupportedMediaType},
		{"duplicate", []string{"application/json", "application/json"}, http.StatusUnsupportedMediaType},
	} {
		t.Run("content-type/"+tc.name, func(t *testing.T) {
			headers := f.validHeaders("feedback-content-" + tc.name + "-0001")
			if tc.contentTypes == nil {
				delete(headers, "Content-Type")
			} else {
				headers["Content-Type"] = tc.contentTypes
			}
			response := f.request(http.MethodPost, path, validBody, headers)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	bodyCases := []struct {
		name   string
		body   []byte
		status int
	}{
		{"empty", nil, http.StatusBadRequest},
		{"null", []byte("null"), http.StatusBadRequest},
		{"array", []byte("[]"), http.StatusBadRequest},
		{"invalid-utf8", []byte{'{', '"', 'd', 'e', 'c', 'i', 's', 'i', 'o', 'n', '"', ':', '"', 0xff, '"', '}'}, http.StatusBadRequest},
		{"unknown", []byte(`{"decision":"agree","extra":true}`), http.StatusBadRequest},
		{"duplicate", []byte(`{"decision":"agree","decision":"defer"}`), http.StatusBadRequest},
		{"duplicate-note", []byte(`{"decision":"agree","note":"one","note":"two"}`), http.StatusBadRequest},
		{"trailing", []byte(`{"decision":"agree"}{}`), http.StatusBadRequest},
		{"html", []byte(`<b>raw</b>`), http.StatusBadRequest},
		{"control-note", []byte(`{"decision":"agree","note":"\u0001"}`), http.StatusBadRequest},
		{"missing-decision", []byte(`{"note":"x"}`), http.StatusBadRequest},
		{"null-decision", []byte(`{"decision":null}`), http.StatusBadRequest},
		{"invalid-decision", []byte(`{"decision":"unknown"}`), http.StatusBadRequest},
		{"empty-note", []byte(`{"decision":"agree","note":""}`), http.StatusBadRequest},
		{"null-note", []byte(`{"decision":"agree","note":null}`), http.StatusCreated},
		{"overlong-note", []byte(`{"decision":"agree","note":"` + strings.Repeat("x", 2001) + `"}`), http.StatusBadRequest},
	}
	for _, tc := range bodyCases {
		t.Run("body/"+tc.name, func(t *testing.T) {
			response := f.request(http.MethodPost, path, string(tc.body), f.validHeaders("feedback-body-"+tc.name+"-0001"))
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	atLimit := append([]byte{}, []byte(validBody)...)
	atLimit = append(atLimit, bytes.Repeat([]byte{' '}, store.ReviewBodyLimit-len(atLimit))...)
	if len(atLimit) != store.ReviewBodyLimit {
		t.Fatalf("body boundary=%d", len(atLimit))
	}
	if response := f.request(http.MethodPost, path, string(atLimit), f.validHeaders("feedback-body-limit-0001")); response.Code != http.StatusCreated {
		t.Fatalf("exact body limit status=%d body=%s", response.Code, response.Body.String())
	}
	overLimit := append(append([]byte{}, atLimit...), ' ')
	if response := f.request(http.MethodPost, path, string(overLimit), f.validHeaders("feedback-body-over-0001")); response.Code != http.StatusRequestEntityTooLarge {
		assertFeedbackHTTPError(t, response, http.StatusRequestEntityTooLarge, "payload_too_large")
	}
}
