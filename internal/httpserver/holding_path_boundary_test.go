package httpserver

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

type holdingPathTrackingBody struct {
	reads int
}

func (b *holdingPathTrackingBody) Read(_ []byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *holdingPathTrackingBody) Close() error { return nil }

func TestHoldingMutationUnsafePathsAreRejectedBeforeServeMux(t *testing.T) {
	handler, cookie := holdingPathBoundaryFixture(t)
	paths := []string{
		"/api/v1/portfolios/foo//holdings/lot",
		"/api//v1/portfolios/foo/holdings/lot",
		"/api/v1/./portfolios/foo/holdings/lot",
		"/api/v1/portfolios/foo//holdings",
		"/api/v1/portfolios/foo/./holdings/lot",
		"/api/v1/portfolios/foo/holdings/../holdings/lot",
		"/api/v1/portfolios/foo/holdings/lot/..",
		"/api/v1/portfolios/foo/holdings//lot",
		"/api/v1/portfolios/foo/holdings/lot/extra",
		"/api/v1/portfolios/foo/holdings/",
		"/api/v1/portfolios/foo/holdings/lot/",
		"/api/v1/portfolios/foo/%2e/holdings/lot",
		"/api/v1/portfolios/foo/holdings/%2e%2e/holdings/lot",
		"/api/v1/portfolios/foo/holdings%2flot",
	}
	for _, path := range paths {
		for _, authenticated := range []bool{false, true} {
			for _, method := range []string{http.MethodPost, http.MethodDelete} {
				t.Run(strings.Join([]string{method, path, map[bool]string{false: "anonymous", true: "session"}[authenticated]}, "_"), func(t *testing.T) {
					body := &holdingPathTrackingBody{}
					request := httptest.NewRequest(method, path, body)
					if authenticated {
						request.AddCookie(cookie)
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					if response.Code != http.StatusBadRequest {
						t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
					}
					if response.Header().Get("Location") != "" {
						t.Fatalf("unexpected redirect location %q", response.Header().Get("Location"))
					}
					if !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
						t.Fatalf("generic invalid request body=%s", response.Body.String())
					}
					if body.reads != 0 {
						t.Fatalf("request body was read %d times", body.reads)
					}
				})
			}
		}
	}
}

func TestHoldingMutationCanonicalPathsStillReachAuthenticationAndMethodDispatch(t *testing.T) {
	handler, cookie := holdingPathBoundaryFixture(t)
	for _, test := range []struct {
		name   string
		method string
		path   string
		want   int
		cookie bool
	}{
		{name: "anonymous collection", method: http.MethodPost, path: "/api/v1/portfolios/foo/holdings", want: http.StatusUnauthorized},
		{name: "anonymous item", method: http.MethodDelete, path: "/api/v1/portfolios/foo/holdings/lot", want: http.StatusUnauthorized},
		{name: "session collection", method: http.MethodGet, path: "/api/v1/portfolios/foo/holdings", want: http.StatusMethodNotAllowed, cookie: true},
		{name: "session item", method: http.MethodGet, path: "/api/v1/portfolios/foo/holdings/lot", want: http.StatusMethodNotAllowed, cookie: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.cookie {
				request.AddCookie(cookie)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Location") != "" {
				t.Fatalf("unexpected redirect location=%q", response.Header().Get("Location"))
			}
			if test.want == http.StatusUnauthorized && response.Header().Get("WWW-Authenticate") != auth.MachineBearerRealm {
				t.Fatalf("challenge=%q", response.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestHoldingMutationBoundaryDoesNotInterceptThesisOrAuthRoutes(t *testing.T) {
	handler, _ := holdingPathBoundaryFixture(t)
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/api/v1/portfolios/foo/holdings/lot/thesis", want: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/api/v1/auth/login", want: http.StatusBadRequest},
		{method: http.MethodGet, path: "/api/v1/auth/session", want: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/api/v1/portfolios/foo/overview", want: http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if test.path == "/api/v1/auth/login" {
			request = httptest.NewRequest(test.method, test.path, strings.NewReader("{"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://example.test")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Location") != "" {
			t.Fatalf("unexpected redirect location=%q", response.Header().Get("Location"))
		}
	}
}

func holdingPathBoundaryFixture(t *testing.T) (http.Handler, *http.Cookie) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(nil, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("holding-path-boundary-session-secret-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
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
		t.Fatalf("fixture login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	return handler, loginResponse.Result().Cookies()[0]
}
