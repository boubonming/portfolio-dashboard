package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestPrivateTemplateDisabledOptionalConfigurationStaysDisabled(t *testing.T) {
	templatePath := filepath.Join("..", "..", "deploy", "private.env.example")
	content, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	rootContent, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rootContent) != string(content) {
		t.Fatal("root and compatibility environment templates diverge")
	}
	if strings.Contains(string(content), "PORTFOLIO_DASHBOARD_IMAGE") {
		t.Fatal("private environment template exposes the removed image override")
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("template line is not KEY=VALUE: %q", line)
		}
		values[name] = value
	}
	optional := []string{
		"PORTFOLIO_MACHINE_PRINCIPAL_ID",
		"PORTFOLIO_MACHINE_PORTFOLIO_ID",
		"PORTFOLIO_MACHINE_BEARER_SECRET",
		"FINNHUB_API_KEY",
		"ALPHA_VANTAGE_API_KEY",
		"OPEN_EXCHANGE_RATES_APP_ID",
	}
	for _, name := range optional {
		if values[name] != "" {
			t.Errorf("%s template value = %q, want blank", name, values[name])
		}
	}
	for _, name := range []string{"PORTFOLIO_MACHINE_PORTFOLIO_READ", "PORTFOLIO_MACHINE_REVIEWS_WRITE"} {
		if values[name] != "false" {
			t.Errorf("%s template value = %q, want false", name, values[name])
		}
	}

	setValidEnvironment(t)
	for _, name := range optional {
		t.Setenv(name, values[name])
	}
	t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_READ", values["PORTFOLIO_MACHINE_PORTFOLIO_READ"])
	t.Setenv("PORTFOLIO_MACHINE_REVIEWS_WRITE", values["PORTFOLIO_MACHINE_REVIEWS_WRITE"])
	cfg, err := Load()
	if err != nil {
		t.Fatalf("disabled template configuration rejected: %v", err)
	}
	if cfg.Auth.Machine.Enabled() || cfg.Auth.Machine.PortfolioRead || cfg.Auth.Machine.ReviewsWrite {
		t.Fatalf("disabled machine configuration became active: %s", cfg.Auth.Machine)
	}
	if cfg.ProviderKeys.Finnhub != "" || cfg.ProviderKeys.AlphaVantage != "" || cfg.ProviderKeys.OpenExchangeRates != "" {
		t.Fatalf("disabled provider credentials became configured: %#v", cfg.ProviderKeys)
	}
}

func TestAuthHashTemplateAndDocumentationStayLiteralAndSecretFree(t *testing.T) {
	const hashPlaceholder = "PORTFOLIO_AUTH_PASSWORD_HASH='REPLACE_WITH_BCRYPT_PASSWORD_HASH'"
	realBcrypt := regexp.MustCompile(`\$(2[aby])\$[0-9]{2}\$[./A-Za-z0-9]{53}`)
	templatePaths := []string{".env.example", "deploy/private.env.example"}
	for _, name := range templatePaths {
		content, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if strings.Count(text, hashPlaceholder) != 1 {
			t.Errorf("%s must contain exactly one single-quoted hash placeholder", name)
		}
		if strings.Contains(text, "PORTFOLIO_AUTH_PASSWORD_HASH=REPLACE_WITH_BCRYPT_PASSWORD_HASH") {
			t.Errorf("%s exposes an unquoted bcrypt placeholder", name)
		}
		if realBcrypt.MatchString(text) {
			t.Errorf("%s contains a real-looking bcrypt hash", name)
		}
		values := make(map[string]string)
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = value
			}
		}
		for key, want := range map[string]string{
			"PORTFOLIO_AUTH_PASSWORD_HASH":    "'REPLACE_WITH_BCRYPT_PASSWORD_HASH'",
			"PORTFOLIO_SESSION_SECRET":        "REPLACE_WITH_INDEPENDENT_RANDOM_BASE64_SECRET",
			"PORTFOLIO_MACHINE_BEARER_SECRET": "",
			"FINNHUB_API_KEY":                 "",
			"ALPHA_VANTAGE_API_KEY":           "",
			"OPEN_EXCHANGE_RATES_APP_ID":      "",
		} {
			if got := values[key]; got != want {
				t.Errorf("%s %s = %q, want the non-secret placeholder %q", name, key, got, want)
			}
		}
	}

	htpasswd := regexp.MustCompile(`htpasswd\s+-nBC\s+([0-9]+)\s+portfolio-admin`)
	for _, name := range []string{"README.md", "docs/private-deployment.md"} {
		content, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		match := htpasswd.FindStringSubmatch(text)
		if len(match) == 0 {
			t.Errorf("%s does not document interactive bcrypt generation", name)
		} else if cost, err := strconv.Atoi(match[1]); err != nil || cost < 10 {
			t.Errorf("%s documents bcrypt cost %q, want at least 10", name, match[1])
		}
		if realBcrypt.MatchString(text) {
			t.Errorf("%s contains a real-looking bcrypt hash", name)
		}
		normalizedText := strings.Join(strings.Fields(text), " ")
		for _, phrase := range []string{
			"interactively",
			"after the first colon",
			"not `portfolio-admin:`",
			"single quotes",
			"Never paste the hash or plaintext password into chat",
		} {
			normalizedPhrase := strings.Join(strings.Fields(phrase), " ")
			if !strings.Contains(normalizedText, normalizedPhrase) {
				t.Errorf("%s is missing the credential-safety instruction %q", name, phrase)
			}
		}
	}
}
