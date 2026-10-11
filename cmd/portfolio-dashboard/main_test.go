package main

import (
	"net/http"
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
