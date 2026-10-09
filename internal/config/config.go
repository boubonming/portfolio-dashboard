package config

import "os"

type Config struct {
	BindAddress       string
	DataPath          string
	ReportingCurrency string
}

func FromEnv() Config {
	return Config{
		BindAddress:       envOr("PORTFOLIO_BIND_ADDRESS", ":8080"),
		DataPath:          envOr("PORTFOLIO_DATA_PATH", "portfolio-dashboard.sqlite"),
		ReportingCurrency: envOr("PORTFOLIO_REPORTING_CURRENCY", "MYR"),
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
