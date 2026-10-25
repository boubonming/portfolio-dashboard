package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/robfig/cron/v3"
)

type Config struct {
	BindAddress       string
	DataPath          string
	ReportingCurrency string
	MarketRefresh     MarketRefreshConfig
	ProviderKeys      ProviderKeys
	ProviderURLs      ProviderURLs
	Auth              auth.Config
}

// MarketRefreshConfig contains the optional in-process market refresh job.
// Location is derived from Timezone and is intentionally not serialized.
type MarketRefreshConfig struct {
	Cron              string         `json:"cron,omitempty"`
	Timezone          string         `json:"timezone"`
	PortfolioID       string         `json:"portfolio_id"`
	ReportingCurrency string         `json:"reporting_currency,omitempty"`
	Location          *time.Location `json:"-"`
}

func (c MarketRefreshConfig) Enabled() bool { return c.Cron != "" }

func (c MarketRefreshConfig) String() string {
	return fmt.Sprintf("MarketRefreshConfig{cron:%q timezone:%q portfolio_id:%q reporting_currency:%q}", c.Cron, c.Timezone, c.PortfolioID, c.ReportingCurrency)
}

func (c MarketRefreshConfig) GoString() string { return c.String() }

func (c MarketRefreshConfig) MarshalJSON() ([]byte, error) {
	type public MarketRefreshConfig
	return json.Marshal(public(c))
}

// MarketRefreshConfigError never includes configuration values. It therefore
// remains safe when startup errors are formatted by arbitrary diagnostics.
type MarketRefreshConfigError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e MarketRefreshConfigError) Error() string {
	return fmt.Sprintf("market refresh configuration: invalid %s (%s)", e.Field, e.Reason)
}

func (e MarketRefreshConfigError) String() string   { return e.Error() }
func (e MarketRefreshConfigError) GoString() string { return e.Error() }
func (e MarketRefreshConfigError) MarshalJSON() ([]byte, error) {
	type public MarketRefreshConfigError
	return json.Marshal(public(e))
}
func (e MarketRefreshConfigError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, e.Error())
}

var marketRefreshPortfolioID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)
var marketRefreshCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

// ParseMarketRefreshConfig validates the strict five-field standard cron
// expression and all scheduler settings. The empty expression disables the
// scheduler but the remaining settings are still validated at startup.
func ParseMarketRefreshConfig(expression, timezone, portfolioID, currency string) (MarketRefreshConfig, error) {
	if strings.TrimSpace(expression) != expression {
		return MarketRefreshConfig{}, MarketRefreshConfigError{"PORTFOLIO_MARKET_REFRESH_CRON", "surrounding whitespace is not allowed"}
	}
	expression = strings.TrimSpace(expression)
	if timezone == "" {
		timezone = "Asia/Singapore"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return MarketRefreshConfig{}, MarketRefreshConfigError{"PORTFOLIO_MARKET_REFRESH_TIMEZONE", "must be a valid IANA timezone"}
	}
	if portfolioID == "" {
		portfolioID = "portfolio-default"
	}
	if !marketRefreshPortfolioID.MatchString(portfolioID) {
		return MarketRefreshConfig{}, MarketRefreshConfigError{"PORTFOLIO_MARKET_REFRESH_PORTFOLIO_ID", "must be 1 to 128 bounded identifier characters"}
	}
	if currency != "" && !marketRefreshCurrency.MatchString(currency) {
		return MarketRefreshConfig{}, MarketRefreshConfigError{"PORTFOLIO_MARKET_REFRESH_REPORTING_CURRENCY", "must be blank or exactly three uppercase letters"}
	}
	if expression != "" {
		if strings.HasPrefix(expression, "@") || len(strings.Fields(expression)) != 5 {
			return MarketRefreshConfig{}, MarketRefreshConfigError{"PORTFOLIO_MARKET_REFRESH_CRON", "must be a standard five-field expression"}
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(expression); err != nil {
			return MarketRefreshConfig{}, MarketRefreshConfigError{"PORTFOLIO_MARKET_REFRESH_CRON", "must be a valid standard five-field expression"}
		}
	}
	return MarketRefreshConfig{Cron: expression, Timezone: timezone, PortfolioID: portfolioID, ReportingCurrency: currency, Location: location}, nil
}

func marketRefreshConfigFromEnv() MarketRefreshConfig {
	return MarketRefreshConfig{
		Cron:              os.Getenv("PORTFOLIO_MARKET_REFRESH_CRON"),
		Timezone:          envOr("PORTFOLIO_MARKET_REFRESH_TIMEZONE", "Asia/Singapore"),
		PortfolioID:       envOr("PORTFOLIO_MARKET_REFRESH_PORTFOLIO_ID", "portfolio-default"),
		ReportingCurrency: os.Getenv("PORTFOLIO_MARKET_REFRESH_REPORTING_CURRENCY"),
	}
}

// ProviderKeys are read once at process startup and are never persisted or
// included in aggregate refresh output.
type ProviderKeys struct {
	Finnhub           string
	AlphaVantage      string
	OpenExchangeRates string
}

// ProviderURLs are server-side transport settings. They are injectable for
// deterministic HTTP tests and never contain credentials.
type ProviderURLs struct {
	Finnhub           string
	AlphaVantage      string
	CoinGecko         string
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
		MarketRefresh:     marketRefreshConfigFromEnv(),
		ProviderKeys: ProviderKeys{
			Finnhub:           os.Getenv("FINNHUB_API_KEY"),
			AlphaVantage:      os.Getenv("ALPHA_VANTAGE_API_KEY"),
			OpenExchangeRates: os.Getenv("OPEN_EXCHANGE_RATES_APP_ID"),
		},
		ProviderURLs: ProviderURLs{
			Finnhub: envFirst("PORTFOLIO_FINNHUB_BASE_URL", "FINNHUB_BASE_URL"), AlphaVantage: envFirst("PORTFOLIO_ALPHA_VANTAGE_BASE_URL", "ALPHA_VANTAGE_BASE_URL"),
			CoinGecko: envFirst("PORTFOLIO_COINGECKO_BASE_URL", "COINGECKO_BASE_URL"), OpenExchangeRates: envFirst("PORTFOLIO_OPEN_EXCHANGE_RATES_BASE_URL", "OPEN_EXCHANGE_RATES_BASE_URL"),
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
				MarketRefresh: machineMarketRefresh(),
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

func machineMarketRefresh() bool {
	value := os.Getenv("PORTFOLIO_MACHINE_MARKET_REFRESH")
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
	marketRefresh, marketRefreshErr := ParseMarketRefreshConfig(cfg.MarketRefresh.Cron, cfg.MarketRefresh.Timezone, cfg.MarketRefresh.PortfolioID, cfg.MarketRefresh.ReportingCurrency)
	if marketRefreshErr != nil {
		return Config{}, marketRefreshErr
	}
	cfg.MarketRefresh = marketRefresh
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
	if value := os.Getenv("PORTFOLIO_MACHINE_MARKET_REFRESH"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return Config{}, fmt.Errorf("machine authentication configuration: invalid PORTFOLIO_MACHINE_MARKET_REFRESH")
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

func envFirst(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
