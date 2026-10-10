package config

import "os"

type Config struct {
	BindAddress       string
	DataPath          string
	ReportingCurrency string
	ProviderKeys      ProviderKeys
}

// ProviderKeys are read once at process startup and are never persisted or
// included in aggregate refresh output.
type ProviderKeys struct {
	Finnhub           string
	AlphaVantage      string
	OpenExchangeRates string
}

func FromEnv() Config {
	return Config{
		BindAddress:       envOr("PORTFOLIO_BIND_ADDRESS", ":8080"),
		DataPath:          envOr("PORTFOLIO_DATA_PATH", "portfolio-dashboard.sqlite"),
		ReportingCurrency: envOr("PORTFOLIO_REPORTING_CURRENCY", "MYR"),
		ProviderKeys: ProviderKeys{
			Finnhub:           os.Getenv("FINNHUB_API_KEY"),
			AlphaVantage:      os.Getenv("ALPHA_VANTAGE_API_KEY"),
			OpenExchangeRates: os.Getenv("OPEN_EXCHANGE_RATES_APP_ID"),
		},
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
