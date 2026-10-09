package main

import (
	"strings"
	"testing"
)

func TestApplyRequiresExplicitApproval(t *testing.T) {
	err := runApply([]string{"-input", "Portfolio.md", "-db", "portfolio.sqlite", "-expected-sha256", strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "explicit approval is required") {
		t.Fatalf("error = %v", err)
	}
}
