package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplyRequiresExplicitApproval(t *testing.T) {
	err := runApply([]string{"-input", "Portfolio.md", "-db", "portfolio.sqlite", "-expected-sha256", strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "explicit approval is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewHTTPServerUsesProductionTimeouts(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newHTTPServer("127.0.0.1:8080", handler)
	if server.Addr != "127.0.0.1:8080" || server.Handler == nil {
		t.Fatalf("server identity = addr %q handler=%v", server.Addr, server.Handler)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 15*time.Second || server.WriteTimeout != 15*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatalf("server timeouts = header=%s read=%s write=%s idle=%s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestHealthcheckUsesOnlyHealthEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := runHealthcheck([]string{"-url", server.URL + "/healthz", "-timeout", "1s"}); err != nil {
		t.Fatalf("healthcheck returned error: %v", err)
	}
	for _, target := range []string{server.URL + "/api/v1/portfolios/private", server.URL + "/healthz?private=true"} {
		if err := runHealthcheck([]string{"-url", target}); err == nil {
			t.Fatalf("healthcheck accepted non-public target %q", target)
		}
	}
}

func TestHealthcheckRejectsNonOKAndTimeout(t *testing.T) {
	notOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer notOK.Close()
	if err := runHealthcheck([]string{"-url", notOK.URL + "/healthz"}); err == nil {
		t.Fatal("healthcheck accepted a non-200 status")
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	if err := runHealthcheck([]string{"-url", slow.URL + "/healthz", "-timeout", "1ms"}); err == nil {
		t.Fatal("healthcheck accepted a request beyond its timeout")
	}
	if err := runHealthcheck([]string{"-url", slow.URL + "/healthz", "-timeout", "0s"}); err == nil {
		t.Fatal("healthcheck accepted a non-positive timeout")
	}
}
