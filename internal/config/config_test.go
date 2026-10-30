package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func setValidEnvironment(t *testing.T) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTFOLIO_BIND_ADDRESS", "127.0.0.1:8080")
	t.Setenv("PORTFOLIO_DATA_PATH", "fixture.sqlite")
	t.Setenv("PORTFOLIO_AUTH_USERNAME", "fixture-user")
	t.Setenv("PORTFOLIO_AUTH_PASSWORD_HASH", string(hash))
	t.Setenv("PORTFOLIO_SESSION_SECRET", base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123")))
	t.Setenv("PORTFOLIO_PUBLIC_ORIGIN", "https://example.test")
	t.Setenv("PORTFOLIO_COOKIE_SECURE", "true")
	t.Setenv("PORTFOLIO_SESSION_TTL", "12h")
	t.Setenv("PORTFOLIO_ENV", "production")
	for _, name := range []string{"PORTFOLIO_MACHINE_PRINCIPAL_ID", "PORTFOLIO_MACHINE_PORTFOLIO_ID", "PORTFOLIO_MACHINE_BEARER_SECRET", "PORTFOLIO_MACHINE_PORTFOLIO_READ", "PORTFOLIO_MACHINE_REVIEWS_WRITE", "PORTFOLIO_MACHINE_HOLDINGS_WRITE"} {
		t.Setenv(name, "")
	}
}

func TestLoadFailsClosedForCookieAndOriginDeploymentModes(t *testing.T) {
	t.Run("insecure cookies need development loopback", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("PORTFOLIO_COOKIE_SECURE", "false")
		if _, err := Load(); err == nil {
			t.Fatal("insecure cookies were accepted in production")
		}
		t.Setenv("PORTFOLIO_ENV", "development")
		t.Setenv("PORTFOLIO_BIND_ADDRESS", ":8080")
		if _, err := Load(); err == nil {
			t.Fatal("insecure cookies were accepted without an explicit loopback bind")
		}
		t.Setenv("PORTFOLIO_BIND_ADDRESS", "127.0.0.1:8080")
		t.Setenv("PORTFOLIO_PUBLIC_ORIGIN", "http://127.0.0.1:8080")
		if _, err := Load(); err != nil {
			t.Fatalf("development loopback configuration rejected: %v", err)
		}
	})

	t.Run("production requires https public origin", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("PORTFOLIO_PUBLIC_ORIGIN", "http://127.0.0.1:8080")
		if _, err := Load(); err == nil {
			t.Fatal("HTTP public origin was accepted in production")
		}
	})
}

func TestLoadValidatesSecretAndSessionTTL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "invalid base64", value: "not base64"},
		{name: "low diversity", value: base64.StdEncoding.EncodeToString([]byte("ssssssssssssssssssssssssssssssss"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("PORTFOLIO_SESSION_SECRET", tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid session secret was accepted")
			}
		})
	}
	for _, ttl := range []string{"999ms", "24h1m"} {
		t.Run("reject ttl "+ttl, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("PORTFOLIO_SESSION_TTL", ttl)
			if _, err := Load(); err == nil {
				t.Fatalf("session TTL %s was accepted", ttl)
			}
		})
	}
	for _, ttl := range []string{"1s", "24h"} {
		t.Run("accept ttl "+ttl, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("PORTFOLIO_SESSION_TTL", ttl)
			if _, err := Load(); err != nil {
				t.Fatalf("session TTL %s rejected: %v", ttl, err)
			}
		})
	}
}

func TestLoadMachineConfigurationIsDisabledOrFailClosed(t *testing.T) {
	t.Run("absent is disabled", func(t *testing.T) {
		setValidEnvironment(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Auth.Machine.Enabled() {
			t.Fatal("machine authentication unexpectedly enabled")
		}
	})
	tests := []struct {
		name string
		set  func(*testing.T)
	}{
		{"partial principal", func(t *testing.T) { t.Setenv("PORTFOLIO_MACHINE_PRINCIPAL_ID", "machine") }},
		{"partial portfolio", func(t *testing.T) { t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_ID", "portfolio") }},
		{"partial secret", func(t *testing.T) {
			t.Setenv("PORTFOLIO_MACHINE_BEARER_SECRET", "machine-secret-that-is-long-but-not-a-complete-config-0123456789")
		}},
		{"grant without principal", func(t *testing.T) { t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_READ", "true") }},
		{"weak secret", func(t *testing.T) {
			t.Setenv("PORTFOLIO_MACHINE_PRINCIPAL_ID", "machine")
			t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_ID", "portfolio")
			t.Setenv("PORTFOLIO_MACHINE_BEARER_SECRET", "ssssssssssssssssssssssssssssssss")
		}},
		{"invalid grant", func(t *testing.T) { t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_READ", "maybe") }},
		{"secret newline", func(t *testing.T) {
			t.Setenv("PORTFOLIO_MACHINE_PRINCIPAL_ID", "machine")
			t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_ID", "portfolio")
			t.Setenv("PORTFOLIO_MACHINE_BEARER_SECRET", "machine-secret-with-enough-diversity-0123456789\n")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnvironment(t)
			tc.set(t)
			if _, err := Load(); err == nil {
				t.Fatal("invalid machine configuration was accepted")
			} else if strings.Contains(err.Error(), "machine-secret") {
				t.Fatalf("machine secret leaked in configuration error: %v", err)
			}
		})
	}
}

func TestLoadValidMachineConfigurationDoesNotExposeSecret(t *testing.T) {
	setValidEnvironment(t)
	secret := "machine-secret-with-enough-diversity-0123456789-ABCDEF"
	t.Setenv("PORTFOLIO_MACHINE_PRINCIPAL_ID", "machine-reader")
	t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_ID", "portfolio-api")
	t.Setenv("PORTFOLIO_MACHINE_BEARER_SECRET", secret)
	t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_READ", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.Machine.Enabled() || !cfg.Auth.Machine.PortfolioRead {
		t.Fatalf("machine config = %s", cfg.Auth.Machine)
	}
	if strings.Contains(cfg.Auth.Machine.String(), secret) {
		t.Fatal("machine secret leaked in String rendering")
	}
}
