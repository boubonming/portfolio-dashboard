package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateTemplateDisabledOptionalConfigurationStaysDisabled(t *testing.T) {
	templatePath := filepath.Join("..", "..", "deploy", "private.env.example")
	content, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
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
