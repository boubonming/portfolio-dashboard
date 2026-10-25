package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseMarketRefreshConfigContract(t *testing.T) {
	defaultConfig, err := ParseMarketRefreshConfig("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.Enabled() || defaultConfig.Timezone != "Asia/Singapore" || defaultConfig.PortfolioID != "portfolio-default" || defaultConfig.Location == nil {
		t.Fatalf("default scheduler config = %#v", defaultConfig)
	}
	valid, err := ParseMarketRefreshConfig("0 5 * * 1-5", "Asia/Singapore", "portfolio-default", "USD")
	if err != nil || !valid.Enabled() || valid.ReportingCurrency != "USD" {
		t.Fatalf("valid scheduler config=%#v err=%v", valid, err)
	}
	for _, expression := range []string{"*/5 * * * * *", "@daily", "61 * * * *", "0 0 0 1 *", " 0 5 * * *"} {
		if _, err := ParseMarketRefreshConfig(expression, "Asia/Singapore", "portfolio-default", ""); err == nil {
			t.Errorf("accepted invalid cron expression %q", expression)
		}
	}
	for _, timezone := range []string{"Not/AZone", "UTC/Nope"} {
		if _, err := ParseMarketRefreshConfig("", timezone, "portfolio-default", ""); err == nil {
			t.Errorf("accepted invalid timezone %q", timezone)
		}
	}
	for _, portfolioID := range []string{"", "bad id", strings.Repeat("x", 129), "-not-bounded"} {
		if portfolioID == "" {
			continue
		}
		if _, err := ParseMarketRefreshConfig("", "Asia/Singapore", portfolioID, ""); err == nil {
			t.Errorf("accepted invalid portfolio ID %q", portfolioID)
		}
	}
	for _, currency := range []string{"usd", "US", "US$", "USDX"} {
		if _, err := ParseMarketRefreshConfig("", "Asia/Singapore", "portfolio-default", currency); err == nil {
			t.Errorf("accepted invalid currency %q", currency)
		}
	}
}

func TestMarketRefreshConfigErrorFormattingIsSecretSafe(t *testing.T) {
	err := MarketRefreshConfigError{Field: "PORTFOLIO_MARKET_REFRESH_CRON", Reason: "invalid"}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, rendered := range []string{err.Error(), err.String(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), string(encoded)} {
		if strings.Contains(rendered, "secret") {
			t.Fatalf("configuration error leaked secret text: %s", rendered)
		}
	}
}
