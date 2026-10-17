package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := runHealthcheck([]string{"-url", server.URL + "/healthz", "-timeout", "1s"}); err != nil {
		t.Fatalf("healthcheck returned error: %v", err)
	}
	for _, target := range []string{server.URL + "/api/v1/portfolios/private", server.URL + "/healthz?private=true", server.URL + "/healthz#fragment"} {
		if err := runHealthcheck([]string{"-url", target}); err == nil {
			t.Fatalf("healthcheck accepted non-public target %q", target)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want one successful request", got)
	}
}

func TestHealthcheckRejectsNonLoopbackAndAmbiguousHosts(t *testing.T) {
	for _, target := range []string{
		"http://localhost:8080/healthz",
		"http://127.0.0.2:8080/healthz",
		"http://192.0.2.1:8080/healthz",
		"http://[::2]:8080/healthz",
		"http://127.0.0.1/healthz",
		"http://127.0.0.1:8080@127.0.0.2:8080/healthz",
		"http://user@127.0.0.1:8080/healthz",
		"ftp://127.0.0.1:8080/healthz",
		"http://127.0.0.1:abc/healthz",
		"http://127.0.0.1:8080/%68ealthz",
	} {
		if err := runHealthcheck([]string{"-url", target}); err == nil {
			t.Errorf("healthcheck accepted invalid target %q", target)
		}
	}
}

func TestHealthcheckRejectsRedirectWithoutTouchingTargets(t *testing.T) {
	var privateRequests atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		privateRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer private.Close()
	var redirectRequests atomic.Int32
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectRequests.Add(1)
		http.Redirect(w, r, private.URL+"/private", http.StatusFound)
	}))
	defer redirect.Close()

	err := runHealthcheck([]string{"-url", redirect.URL + "/healthz"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect result = %v, want redirect error", err)
	}
	if got := redirectRequests.Load(); got != 1 {
		t.Fatalf("redirect request count = %d, want one", got)
	}
	if got := privateRequests.Load(); got != 0 {
		t.Fatalf("redirect target request count = %d, want zero", got)
	}
}

func TestHealthcheckIgnoresEnvironmentProxies(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	if err := runHealthcheck([]string{"-url", target.URL + "/healthz"}); err != nil {
		t.Fatalf("proxied environment changed healthcheck result: %v", err)
	}
	if got := proxyRequests.Load(); got != 0 {
		t.Fatalf("proxy request count = %d, want zero", got)
	}
	if got := targetRequests.Load(); got != 1 {
		t.Fatalf("target request count = %d, want one", got)
	}
}

func TestHealthcheckRejectsNonOKOversizedAndTimeoutResponses(t *testing.T) {
	notOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer notOK.Close()
	if err := runHealthcheck([]string{"-url", notOK.URL + "/healthz"}); err == nil {
		t.Fatal("healthcheck accepted a non-200 status")
	}
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4<<10+1)))
	}))
	defer oversized.Close()
	if err := runHealthcheck([]string{"-url", oversized.URL + "/healthz"}); err == nil {
		t.Fatal("healthcheck accepted an oversized response")
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
	if err := runHealthcheck([]string{"-url", slow.URL + "/healthz", "-timeout", "31s"}); err == nil {
		t.Fatal("healthcheck accepted an unbounded timeout")
	}
}

func TestHealthcheckSupportsLiteralIPv6Loopback(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err := server.Listener.Close(); err != nil {
		t.Skip("IPv6 loopback is unavailable")
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := runHealthcheck([]string{"-url", "http://[::1]:" + parsed.Port() + "/healthz"}); err != nil {
		t.Fatalf("IPv6 loopback healthcheck failed: %v", err)
	}
}
