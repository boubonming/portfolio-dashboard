package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
)

type Config struct {
	BindAddress       string
	DataPath          string
	ReportingCurrency string
	ProviderKeys      ProviderKeys
	Auth              auth.Config
}

// ProviderKeys are read once at process startup and are never persisted or
// included in aggregate refresh output.
type ProviderKeys struct {
	Finnhub           string
	AlphaVantage      string
	OpenExchangeRates string
}

// FromEnv preserves the non-auth configuration API used by import tooling.
// The server must use Load, which validates the required authentication gate.
func FromEnv() Config {
	cookieSecure, _ := strconv.ParseBool(envOr("PORTFOLIO_COOKIE_SECURE", "true"))
	ttl, _ := time.ParseDuration(envOr("PORTFOLIO_SESSION_TTL", "12h"))
	return Config{
		BindAddress:       envOr("PORTFOLIO_BIND_ADDRESS", ":8080"),
		DataPath:          envOr("PORTFOLIO_DATA_PATH", "portfolio-dashboard.sqlite"),
		ReportingCurrency: envOr("PORTFOLIO_REPORTING_CURRENCY", "MYR"),
		ProviderKeys: ProviderKeys{
			Finnhub:           os.Getenv("FINNHUB_API_KEY"),
			AlphaVantage:      os.Getenv("ALPHA_VANTAGE_API_KEY"),
			OpenExchangeRates: os.Getenv("OPEN_EXCHANGE_RATES_APP_ID"),
		},
		Auth: auth.Config{
			Username: os.Getenv("PORTFOLIO_AUTH_USERNAME"), PasswordHash: os.Getenv("PORTFOLIO_AUTH_PASSWORD_HASH"),
			SessionSecret: os.Getenv("PORTFOLIO_SESSION_SECRET"), PublicOrigin: os.Getenv("PORTFOLIO_PUBLIC_ORIGIN"), Environment: os.Getenv("PORTFOLIO_ENV"), CookieSecure: cookieSecure, SessionTTL: ttl,
			Machine: auth.MachineConfig{
				PrincipalID:   os.Getenv("PORTFOLIO_MACHINE_PRINCIPAL_ID"),
				PortfolioID:   os.Getenv("PORTFOLIO_MACHINE_PORTFOLIO_ID"),
				BearerSecret:  os.Getenv("PORTFOLIO_MACHINE_BEARER_SECRET"),
				PortfolioRead: machinePortfolioRead(),
				ReviewsWrite:  machineReviewsWrite(),
				HoldingsWrite: machineHoldingsWrite(),
			},
		},
	}
}

func machinePortfolioRead() bool {
	value := os.Getenv("PORTFOLIO_MACHINE_PORTFOLIO_READ")
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func machineReviewsWrite() bool {
	value := os.Getenv("PORTFOLIO_MACHINE_REVIEWS_WRITE")
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func machineHoldingsWrite() bool {
	value := os.Getenv("PORTFOLIO_MACHINE_HOLDINGS_WRITE")
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

// Load reads and validates all server configuration. Authentication is
// intentionally fail-closed: there are no development credentials or secret
// fallbacks. Local HTTP use must explicitly opt into PORTFOLIO_ENV=development
// and PORTFOLIO_COOKIE_SECURE=false.
func Load() (Config, error) {
	cfg := FromEnv()
	if _, err := strconv.ParseBool(envOr("PORTFOLIO_COOKIE_SECURE", "true")); err != nil {
		return Config{}, fmt.Errorf("authentication configuration: invalid PORTFOLIO_COOKIE_SECURE")
	}
	if _, err := time.ParseDuration(envOr("PORTFOLIO_SESSION_TTL", "12h")); err != nil {
		return Config{}, fmt.Errorf("authentication configuration: invalid PORTFOLIO_SESSION_TTL")
	}
	if value := os.Getenv("PORTFOLIO_MACHINE_PORTFOLIO_READ"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return Config{}, fmt.Errorf("machine authentication configuration: invalid PORTFOLIO_MACHINE_PORTFOLIO_READ")
		}
	}
	if value := os.Getenv("PORTFOLIO_MACHINE_REVIEWS_WRITE"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return Config{}, fmt.Errorf("machine authentication configuration: invalid PORTFOLIO_MACHINE_REVIEWS_WRITE")
		}
	}
	if value := os.Getenv("PORTFOLIO_MACHINE_HOLDINGS_WRITE"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return Config{}, fmt.Errorf("machine authentication configuration: invalid PORTFOLIO_MACHINE_HOLDINGS_WRITE")
		}
	}
	if !cfg.Auth.CookieSecure && !strings.EqualFold(os.Getenv("PORTFOLIO_ENV"), "development") && !strings.EqualFold(os.Getenv("PORTFOLIO_ENV"), "test") {
		return Config{}, fmt.Errorf("authentication configuration: insecure cookies require PORTFOLIO_ENV=development or test")
	}
	if !cfg.Auth.CookieSecure && !loopbackBind(cfg.BindAddress) {
		return Config{}, fmt.Errorf("authentication configuration: insecure cookies require an explicit loopback bind address")
	}
	if err := publicOriginEnvironment(cfg.Auth.PublicOrigin); err != nil {
		return Config{}, err
	}
	if cfg.Auth.Machine.Enabled() && !cfg.Auth.Machine.PortfolioRead {
		return Config{}, fmt.Errorf("machine authentication configuration: portfolio read is required when machine credentials are configured")
	}
	if err := cfg.Auth.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loopbackBind(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	return net.ParseIP(strings.Trim(host, "[]")).IsLoopback()
}

func publicOriginEnvironment(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("authentication configuration: invalid PORTFOLIO_PUBLIC_ORIGIN")
	}
	if strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(os.Getenv("PORTFOLIO_ENV"), "development") && !strings.EqualFold(os.Getenv("PORTFOLIO_ENV"), "test") {
		return fmt.Errorf("authentication configuration: production requires an HTTPS public origin")
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
