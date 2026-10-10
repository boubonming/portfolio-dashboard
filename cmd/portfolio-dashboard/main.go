package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/httpserver"
	"github.com/boubonming/portfolio-dashboard/internal/importer"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func main() {
	if len(os.Args) > 1 {
		if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cfg := config.FromEnv()
	handler, err := httpserver.NewHandler()
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "portfolio dashboard listening on %s\n", cfg.BindAddress)
	fatal(http.ListenAndServe(cfg.BindAddress, handler))
}

func runCommand(command string, args []string) error {
	switch command {
	case "import-preview":
		return runPreview(args)
	case "import-apply":
		return runApply(args)
	case "import-status":
		return runStatus(args)
	case "quote-add":
		return runQuoteAdd(args)
	case "fx-add":
		return runFXAdd(args)
	case "snapshot-create":
		return runSnapshotCreate(args)
	case "snapshot-status":
		return runSnapshotStatus(args)
	case "snapshot-show":
		return runSnapshotShow(args)
	case "mapping-add":
		return runMappingAdd(args)
	case "mapping-list":
		return runMappingList(args)
	case "market-refresh":
		return runMarketRefresh(args)
	default:
		return fmt.Errorf("unknown command %q (use import-preview, import-apply, import-status, mapping-add, mapping-list, market-refresh, quote-add, fx-add, snapshot-create, snapshot-status, or snapshot-show)", command)
	}
}

func runPreview(args []string) error {
	flags := flag.NewFlagSet("import-preview", flag.ContinueOnError)
	input := flags.String("input", "", "authoritative Markdown input path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("-input is required")
	}
	preview, err := importer.PreviewFile(*input)
	if err != nil {
		return err
	}
	return writeJSON(preview)
}

func runApply(args []string) error {
	flags := flag.NewFlagSet("import-apply", flag.ContinueOnError)
	input := flags.String("input", "", "authoritative Markdown input path")
	database := flags.String("db", "", "SQLite database path")
	expectedHash := flags.String("expected-sha256", "", "exact source SHA-256 from the approved preview")
	approved := flags.Bool("approve", false, "explicitly approve this exact preview hash for persistence")
	reportingCurrency := flags.String("reporting-currency", "MYR", "portfolio reporting currency")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *database == "" || *expectedHash == "" {
		return errors.New("-input, -db, and -expected-sha256 are required")
	}
	if !*approved {
		return errors.New("explicit approval is required: pass -approve")
	}
	preview, err := importer.PreviewFile(*input)
	if err != nil {
		return err
	}
	if preview.SourceSHA256 != *expectedHash {
		return fmt.Errorf("source hash mismatch: got %s, expected %s", preview.SourceSHA256, *expectedHash)
	}
	if err := importer.ValidateApproved(preview); err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, *database)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Apply(ctx, preview, *input, *expectedHash, *reportingCurrency)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func runStatus(args []string) error {
	flags := flag.NewFlagSet("import-status", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *database == "" {
		return errors.New("-db is required")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, *database)
	if err != nil {
		return err
	}
	defer db.Close()
	status, err := db.Status(ctx)
	if err != nil {
		return err
	}
	return writeJSON(status)
}

func openDatabase(path string) (*store.Store, context.Context, error) {
	if path == "" {
		return nil, nil, errors.New("-db is required")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	return db, ctx, nil
}

func runQuoteAdd(args []string) error {
	flags := flag.NewFlagSet("quote-add", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	instrument := flags.String("instrument", "", "instrument ID (or use -symbol and -currency)")
	symbol := flags.String("symbol", "", "instrument symbol")
	currency := flags.String("currency", "", "quote currency")
	price := flags.String("price", "", "positive decimal price")
	source := flags.String("source", "", "source name")
	marketAt := flags.String("market-at", "", "UTC market timestamp in RFC3339")
	fetchedAt := flags.String("fetched-at", "", "UTC fetch timestamp in RFC3339; defaults to controlled current UTC")
	basis := flags.String("basis", "", "quote basis, e.g. close")
	provenance := flags.String("provenance", "", "provenance note or reference")
	if err := flags.Parse(args); err != nil {
		return err
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	quote, err := db.AddQuote(ctx, store.QuoteInput{InstrumentID: *instrument, Symbol: *symbol, Currency: *currency, Price: *price, Source: *source, MarketAt: *marketAt, FetchedAt: *fetchedAt, Basis: *basis, Provenance: *provenance})
	if err != nil {
		return err
	}
	return writeJSON(quote)
}

func runFXAdd(args []string) error {
	flags := flag.NewFlagSet("fx-add", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	base := flags.String("base", "", "base currency")
	quote := flags.String("quote", "", "quote currency")
	rate := flags.String("rate", "", "positive decimal FX rate")
	source := flags.String("source", "", "source name")
	marketAt := flags.String("market-at", "", "UTC market timestamp in RFC3339")
	fetchedAt := flags.String("fetched-at", "", "UTC fetch timestamp in RFC3339; defaults to controlled current UTC")
	basis := flags.String("basis", "", "FX basis, e.g. close")
	provenance := flags.String("provenance", "", "provenance note or reference")
	if err := flags.Parse(args); err != nil {
		return err
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	fx, err := db.AddFXRate(ctx, store.FXInput{BaseCurrency: *base, QuoteCurrency: *quote, Rate: *rate, Source: *source, MarketAt: *marketAt, FetchedAt: *fetchedAt, Basis: *basis, Provenance: *provenance})
	if err != nil {
		return err
	}
	return writeJSON(fx)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid -as-of: %w", err)
	}
	return parsed.UTC(), nil
}

func runSnapshotCreate(args []string) error {
	flags := flag.NewFlagSet("snapshot-create", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	portfolioID := flags.String("portfolio", "portfolio-default", "portfolio ID")
	reportingCurrency := flags.String("reporting-currency", "MYR", "reporting currency")
	version := flags.String("calculation-version", "", "calculation version; defaults to current reviewed version")
	asOf := flags.String("as-of", "", "UTC valuation timestamp in RFC3339; defaults to current UTC")
	maxAge := flags.Duration("max-age", 36*time.Hour, "maximum quote/FX age")
	if err := flags.Parse(args); err != nil {
		return err
	}
	when, err := parseOptionalTime(*asOf)
	if err != nil {
		return err
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	status, err := db.CreateSnapshot(ctx, store.SnapshotRequest{PortfolioID: *portfolioID, ReportingCurrency: *reportingCurrency, CalculationVersion: *version, AsOf: when, MaxAge: *maxAge})
	if err != nil {
		return err
	}
	return writeJSON(status)
}

func runSnapshotStatus(args []string) error {
	flags := flag.NewFlagSet("snapshot-status", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	snapshotID := flags.String("snapshot", "", "snapshot ID; defaults to latest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	status, err := db.SnapshotStatus(ctx, *snapshotID)
	if err != nil {
		return err
	}
	return writeJSON(status)
}

func runSnapshotShow(args []string) error {
	flags := flag.NewFlagSet("snapshot-show", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	snapshotID := flags.String("snapshot", "", "snapshot ID")
	includeLots := flags.Bool("include-lots", false, "explicitly include private lot values")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *snapshotID == "" {
		return errors.New("-snapshot is required")
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.SnapshotShow(ctx, *snapshotID, *includeLots)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func runMappingAdd(args []string) error {
	flags := flag.NewFlagSet("mapping-add", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	instrument := flags.String("instrument", "", "instrument ID")
	providerName := flags.String("provider", "", "provider: finnhub, alpha_vantage, or coingecko")
	providerSymbol := flags.String("provider-symbol", "", "exact provider symbol or coin ID")
	currency := flags.String("quote-currency", "", "quote currency")
	activeFrom := flags.String("active-from", "", "UTC RFC3339 activation time; defaults to now")
	provenance := flags.String("provenance", "", "mapping provenance note")
	if err := flags.Parse(args); err != nil {
		return err
	}
	provider, err := marketdata.ParseProvider(*providerName)
	if err != nil {
		return err
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	mapping, err := db.AddProviderMapping(ctx, store.ProviderMappingInput{InstrumentID: *instrument, Provider: provider, ProviderSymbol: *providerSymbol, QuoteCurrency: *currency, ActiveFrom: *activeFrom, Provenance: *provenance})
	if err != nil {
		return err
	}
	return writeJSON(mapping)
}

func runMappingList(args []string) error {
	flags := flag.NewFlagSet("mapping-list", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	instrument := flags.String("instrument", "", "optional instrument ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	mappings, err := db.ListProviderMappings(ctx, *instrument)
	if err != nil {
		return err
	}
	return writeJSON(mappings)
}

func runMarketRefresh(args []string) error {
	flags := flag.NewFlagSet("market-refresh", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	portfolioID := flags.String("portfolio", "portfolio-default", "portfolio ID")
	reportingCurrency := flags.String("reporting-currency", "", "reporting currency; defaults to portfolio setting")
	providerList := flags.String("providers", "", "optional comma-separated provider subset")
	asOf := flags.String("as-of", "", "UTC refresh timestamp in RFC3339; defaults to now")
	if err := flags.Parse(args); err != nil {
		return err
	}
	when, err := parseOptionalTime(*asOf)
	if err != nil {
		return err
	}
	providers, err := parseProviderList(*providerList)
	if err != nil {
		return err
	}
	cfg := config.FromEnv()
	clients := map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub:           NewFinnhubFromConfig(cfg),
		marketdata.ProviderAlphaVantage:      NewAlphaFromConfig(cfg),
		marketdata.ProviderCoinGecko:         marketdata.NewCoinGeckoClient(marketdata.ClientOptions{BaseURL: os.Getenv("PORTFOLIO_COINGECKO_BASE_URL")}),
		marketdata.ProviderOpenExchangeRates: NewOXRFromConfig(cfg),
	}
	db, ctx, err := openDatabase(*database)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Refresh(ctx, store.RefreshOptions{PortfolioID: *portfolioID, ReportingCurrency: *reportingCurrency, Providers: providers, Clients: clients, AsOf: when})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func NewFinnhubFromConfig(cfg config.Config) marketdata.ProviderClient {
	return marketdata.NewFinnhubClient(cfg.ProviderKeys.Finnhub, marketdata.ClientOptions{BaseURL: os.Getenv("PORTFOLIO_FINNHUB_BASE_URL")})
}
func NewAlphaFromConfig(cfg config.Config) marketdata.ProviderClient {
	return marketdata.NewAlphaVantageClient(cfg.ProviderKeys.AlphaVantage, marketdata.ClientOptions{BaseURL: os.Getenv("PORTFOLIO_ALPHA_VANTAGE_BASE_URL")})
}
func NewOXRFromConfig(cfg config.Config) marketdata.ProviderClient {
	return marketdata.NewOpenExchangeRatesClient(cfg.ProviderKeys.OpenExchangeRates, marketdata.ClientOptions{BaseURL: os.Getenv("PORTFOLIO_OPEN_EXCHANGE_RATES_BASE_URL")})
}

func parseProviderList(value string) ([]marketdata.Provider, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var providers []marketdata.Provider
	seen := map[marketdata.Provider]bool{}
	for _, part := range strings.Split(value, ",") {
		provider, err := marketdata.ParseProvider(strings.TrimSpace(part))
		if err != nil || provider == marketdata.ProviderManual {
			return nil, fmt.Errorf("invalid refresh provider %q", part)
		}
		if !seen[provider] {
			providers = append(providers, provider)
			seen[provider] = true
		}
	}
	return providers, nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func fatal(err error) {
	if err != nil {
		panic(err)
	}
}
