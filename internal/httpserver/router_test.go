package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestRoutesPrioritizeAPIHealthAndSPA(t *testing.T) {
	handler, err := NewStaticHandler()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path    string
		status  int
		content string
	}{
		{"/healthz", http.StatusOK, `"status":"ok"`},
		{"/", http.StatusOK, "Portfolio Dashboard"},
		{"/holdings", http.StatusOK, "Portfolio Dashboard"},
		{"/api/v1/nope", http.StatusNotFound, `"error":"not_found"`},
		{"/assets/missing.js", http.StatusNotFound, ""},
		{"/missing.js", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Errorf("%s status = %d, want %d", tc.path, res.Code, tc.status)
		}
		if tc.content != "" && !contains(res.Body.String(), tc.content) {
			t.Errorf("%s body lacks %q: %s", tc.path, tc.content, res.Body.String())
		}
		if tc.path == "/api/v1/nope" {
			var body map[string]string
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Errorf("api error is not JSON: %v", err)
			}
		}
	}
}

func TestHealthHasExplicitMethodSemantics(t *testing.T) {
	handler, err := NewStaticHandler()
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/healthz", nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s /healthz status = %d, want 200", method, response.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/healthz", nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /healthz = %d allow=%q body=%s", method, response.Code, response.Header().Get("Allow"), response.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] != "method_not_allowed" {
			t.Errorf("%s /healthz body = %q, err=%v", method, response.Body.String(), err)
		}
	}
}

func TestSlashlessAndUnknownAPIRoutesNeverRedirectOrServeSPA(t *testing.T) {
	handler, err := NewStaticHandler()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api", "/api/v1", "/api/v1/auth", "/api/v1/portfolios"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" || response.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s = %d location=%q content-type=%q body=%s", path, response.Code, response.Header().Get("Location"), response.Header().Get("Content-Type"), response.Body.String())
		}
	}
	for _, path := range []string{"/api", "/api/v1/no-such-route", "/api/v1/portfolios/not-a-valid-route"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader("ignored")))
		if response.Code == http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
			t.Errorf("unknown API POST %s = %d content-type=%q body=%s", path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}
}

func TestOverviewRouteValidatesPathBeforeMethod(t *testing.T) {
	handler, err := NewStaticHandler()
	if err != nil {
		t.Fatal(err)
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/fixture/not-overview", nil))
	if unknown.Code != http.StatusNotFound || unknown.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unknown route = %d %s %q", unknown.Code, unknown.Body.String(), unknown.Header().Get("Content-Type"))
	}
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/fixture/overview", nil))
	if valid.Code != http.StatusMethodNotAllowed || valid.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("valid non-GET route = %d allow=%q body=%s", valid.Code, valid.Header().Get("Allow"), valid.Body.String())
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestProductionPrivateRouteIsDeniedAndNeverCacheable(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(nil, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-default/overview", nil))
	if anonymous.Code != http.StatusUnauthorized || anonymous.Header().Get("Cache-Control") != "no-store" || anonymous.Header().Get("Vary") != "Cookie" || anonymous.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("anonymous private response = %d cache=%q vary=%q type=%q body=%s", anonymous.Code, anonymous.Header().Get("Cache-Control"), anonymous.Header().Get("Vary"), anonymous.Header().Get("Content-Type"), anonymous.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("fixture login status = %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	authenticated := httptest.NewRecorder()
	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-default/overview", nil)
	authenticatedRequest.AddCookie(loginResponse.Result().Cookies()[0])
	handler.ServeHTTP(authenticated, authenticatedRequest)
	if authenticated.Code != http.StatusNotFound || authenticated.Header().Get("Cache-Control") != "no-store" || authenticated.Header().Get("Vary") != "Cookie" {
		t.Fatalf("authenticated private response = %d cache=%q vary=%q body=%s", authenticated.Code, authenticated.Header().Get("Cache-Control"), authenticated.Header().Get("Vary"), authenticated.Body.String())
	}
	staticHandler, err := NewStaticHandler()
	if err != nil {
		t.Fatal(err)
	}
	staticResponse := httptest.NewRecorder()
	staticHandler.ServeHTTP(staticResponse, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-default/overview", nil))
	if staticResponse.Code != http.StatusNotFound || staticResponse.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("static private response = %d type=%q body=%s", staticResponse.Code, staticResponse.Header().Get("Content-Type"), staticResponse.Body.String())
	}
}

func TestLoginRejectsMalformedAndThrottledRequestsUniformly(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(nil, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "wrong content type", contentType: "text/plain", body: `{}`},
		{name: "malformed JSON", contentType: "application/json", body: `{"username":`},
		{name: "unknown field", contentType: "application/json", body: `{"username":"fixture-user","password":"fixture-only-value","extra":true}`},
		{name: "trailing value", contentType: "application/json", body: `{"username":"fixture-user","password":"fixture-only-value"}{}`},
		{name: "oversize body", contentType: "application/json", body: `{"username":"fixture-user","password":"` + strings.Repeat("x", 9000) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Origin", "https://example.test")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), `"error":"invalid_request"`) {
				t.Fatalf("response = %d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
	login := func(username, password string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://example.test")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	first := login("wrong-user", "wrong-password")
	second := login("fixture-user", "wrong-password")
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusUnauthorized || first.Body.String() != second.Body.String() {
		t.Fatalf("credential responses differ: first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	for index := 0; index < 4; index++ {
		if response := login("churn-"+string(rune('a'+index)), "wrong-password"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d body=%s", index, response.Code, response.Body.String())
		}
	}
	throttled := login("another-user", "wrong-password")
	if throttled.Code != http.StatusUnauthorized || throttled.Body.String() != first.Body.String() {
		t.Fatalf("throttled response = %d %s, want %d %s", throttled.Code, throttled.Body.String(), first.Code, first.Body.String())
	}
}

func TestLoginAcceptsFullSameOriginReferer(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(nil, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Referer", "https://example.test/portfolio?x=1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("same-origin referer login status = %d, body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Referer", "https://attacker.test/portfolio?x=1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin referer login status = %d", response.Code)
	}
}
