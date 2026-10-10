package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutesPrioritizeAPIHealthAndSPA(t *testing.T) {
	handler, err := NewHandler()
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

func TestOverviewRouteValidatesPathBeforeMethod(t *testing.T) {
	handler, err := NewHandler()
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
