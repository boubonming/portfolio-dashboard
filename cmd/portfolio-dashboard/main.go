package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/httpserver"

	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/marketrefresh"
	"github.com/boubonming/portfolio-dashboard/internal/scheduler"
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
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	database, err := store.Open(ctx, cfg.DataPath)
	if err != nil {
		fatal(err)
	}
	defer database.Close()
	clients := map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub:           NewFinnhubFromConfig(cfg),
		marketdata.ProviderAlphaVantage:      NewAlphaFromConfig(cfg),
		marketdata.ProviderCoinGecko:         marketdata.NewCoinGeckoClient(marketdata.ClientOptions{BaseURL: cfg.ProviderURLs.CoinGecko}),
		marketdata.ProviderOpenExchangeRates: NewOXRFromConfig(cfg),
	}
	handler, err := httpserver.NewHandlerWithConfigAndMarketClients(database, cfg, clients)
	if err != nil {
		fatal(err)
	}
	refreshService := marketrefresh.New(database, clients, store.MarketProviders(map[marketdata.Provider]bool{
		marketdata.ProviderFinnhub:           cfg.ProviderKeys.Finnhub != "",
		marketdata.ProviderAlphaVantage:      cfg.ProviderKeys.AlphaVantage != "",
		marketdata.ProviderOpenExchangeRates: cfg.ProviderKeys.OpenExchangeRates != "",
	}))
	refreshScheduler := scheduler.New(refreshService, cfg.MarketRefresh)
	serverContext := context.Background()
	var stopSignals context.CancelFunc
	if cfg.MarketRefresh.Enabled() {
		serverContext, stopSignals = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stopSignals()
		if err := refreshScheduler.Start(serverContext); err != nil {
			fatal(err)
		}
	}
	fmt.Fprintf(os.Stderr, "portfolio dashboard listening on %s\n", cfg.BindAddress)
	server := newHTTPServer(cfg.BindAddress, handler)
	if cfg.MarketRefresh.Enabled() {
		go func() {
			<-serverContext.Done()
			shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownContext)
		}()
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_ = refreshScheduler.Stop(context.Background())
		fatal(err)
	}
	_ = refreshScheduler.Stop(context.Background())
}

// newHTTPServer centralizes the production timeout policy so it can be
// exercised without starting a listener in tests.
func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func runCommand(command string, args []string) error {
	switch command {

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
	case "machine-grant-holdings":
		return runMachineGrantHoldings(args)
	case "machine-revoke-holdings":
		return runMachineRevokeHoldings(args)
	case "machine-grant-market-refresh", "machine-grant-market-data-refresh":
		return runMachineGrantMarketRefresh(args)
	case "machine-revoke-market-refresh", "machine-revoke-market-data-refresh":
		return runMachineRevokeMarketRefresh(args)
	case "machine-inspect":
		return runMachineInspect(args)
	case "healthcheck":
		return runHealthcheck(args)
	default:
		return fmt.Errorf("unknown command %q (use healthcheck, mapping-add, mapping-list, market-refresh, machine-grant-holdings, machine-revoke-holdings, machine-grant-market-refresh, machine-revoke-market-refresh, machine-inspect, quote-add, fx-add, snapshot-create, snapshot-status, or snapshot-show)", command)
	}
}

func runMachineGrantMarketRefresh(args []string) error {
	flags := flag.NewFlagSet("machine-grant-market-refresh", flag.ContinueOnError)
	databasePath := flags.String("db", "", "SQLite database path")
	principalID := flags.String("principal", "", "existing machine principal ID")
	portfolioID := flags.String("portfolio", "", "exact portfolio binding")
	confirmation := flags.String("confirm", "", "must be exactly GRANT MARKET DATA REFRESH")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *confirmation != "GRANT MARKET DATA REFRESH" {
		return errors.New("-confirm must be exactly GRANT MARKET DATA REFRESH")
	}
	if err := requireConfiguredMarketRefreshBinding(*principalID, *portfolioID); err != nil {
		return err
	}
	database, ctx, err := openDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	changed, err := database.GrantMachineMarketRefresh(ctx, *principalID, *portfolioID)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{"principal_id": *principalID, "portfolio_id": *portfolioID, "market_data_refresh": true, "changed": changed, "result": map[bool]string{true: "granted", false: "already_granted"}[changed]})
}

func runMachineRevokeMarketRefresh(args []string) error {
	flags := flag.NewFlagSet("machine-revoke-market-refresh", flag.ContinueOnError)
	databasePath := flags.String("db", "", "SQLite database path")
	principalID := flags.String("principal", "", "existing machine principal ID")
	portfolioID := flags.String("portfolio", "", "exact portfolio binding")
	confirmation := flags.String("confirm", "", "must be exactly REVOKE MARKET DATA REFRESH")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *confirmation != "REVOKE MARKET DATA REFRESH" {
		return errors.New("-confirm must be exactly REVOKE MARKET DATA REFRESH")
	}
	if err := requireConfiguredMarketRefreshBinding(*principalID, *portfolioID); err != nil {
		return err
	}
	database, ctx, err := openDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	changed, err := database.RevokeMachineMarketRefresh(ctx, *principalID, *portfolioID)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{"principal_id": *principalID, "portfolio_id": *portfolioID, "market_data_refresh": false, "changed": changed, "result": map[bool]string{true: "revoked", false: "already_revoked"}[changed]})
}

func runMachineGrantHoldings(args []string) error {
	flags := flag.NewFlagSet("machine-grant-holdings", flag.ContinueOnError)
	databasePath := flags.String("db", "", "SQLite database path")
	principalID := flags.String("principal", "", "existing machine principal ID")
	portfolioID := flags.String("portfolio", "", "exact portfolio binding")
	confirmation := flags.String("confirm", "", "must be exactly GRANT HOLDINGS")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *confirmation != "GRANT HOLDINGS" {
		return errors.New("-confirm must be exactly GRANT HOLDINGS")
	}
	if err := requireConfiguredMachineBinding(*principalID, *portfolioID, true); err != nil {
		return err
	}
	database, ctx, err := openDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	changed, err := database.GrantMachineHoldings(ctx, *principalID, *portfolioID)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{"principal_id": *principalID, "portfolio_id": *portfolioID, "holdings_write": true, "changed": changed, "result": map[bool]string{true: "granted", false: "already_granted"}[changed]})
}

func runMachineRevokeHoldings(args []string) error {
	flags := flag.NewFlagSet("machine-revoke-holdings", flag.ContinueOnError)
	databasePath := flags.String("db", "", "SQLite database path")
	principalID := flags.String("principal", "", "existing machine principal ID")
	portfolioID := flags.String("portfolio", "", "exact portfolio binding")
	confirmation := flags.String("confirm", "", "must be exactly REVOKE HOLDINGS")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *confirmation != "REVOKE HOLDINGS" {
		return errors.New("-confirm must be exactly REVOKE HOLDINGS")
	}
	if err := requireConfiguredMachineBinding(*principalID, *portfolioID, false); err != nil {
		return err
	}
	database, ctx, err := openDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	changed, err := database.RevokeMachineHoldings(ctx, *principalID, *portfolioID)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{"principal_id": *principalID, "portfolio_id": *portfolioID, "holdings_write": false, "changed": changed, "result": map[bool]string{true: "revoked", false: "already_revoked"}[changed]})
}

func runMachineInspect(args []string) error {
	flags := flag.NewFlagSet("machine-inspect", flag.ContinueOnError)
	databasePath := flags.String("db", "", "SQLite database path")
	principalID := flags.String("principal", "", "existing machine principal ID")
	portfolioID := flags.String("portfolio", "", "exact portfolio binding")
	if err := flags.Parse(args); err != nil {
		return err
	}
	database, ctx, err := openDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	grant, err := database.InspectMachinePrincipal(ctx, *principalID, *portfolioID)
	if err != nil {
		return err
	}
	return writeJSON(grant)
}

func requireConfiguredMachineBinding(principalID, portfolioID string, requireWrite bool) error {
	if principalID == "" || portfolioID == "" || os.Getenv("PORTFOLIO_MACHINE_PRINCIPAL_ID") != principalID || os.Getenv("PORTFOLIO_MACHINE_PORTFOLIO_ID") != portfolioID {
		return errors.New("principal and portfolio must exactly match the configured machine identity")
	}
	if os.Getenv("PORTFOLIO_MACHINE_PORTFOLIO_READ") != "true" {
		return errors.New("PORTFOLIO_MACHINE_PORTFOLIO_READ must be true")
	}
	if requireWrite && os.Getenv("PORTFOLIO_MACHINE_HOLDINGS_WRITE") != "true" {
		return errors.New("PORTFOLIO_MACHINE_HOLDINGS_WRITE must be true before granting")
	}
	return nil
}

func requireConfiguredMarketRefreshBinding(principalID, portfolioID string) error {
	if err := requireConfiguredMachineBinding(principalID, portfolioID, false); err != nil {
		return err
	}
	if os.Getenv("PORTFOLIO_MACHINE_MARKET_REFRESH") != "true" {
		return errors.New("PORTFOLIO_MACHINE_MARKET_REFRESH must be true before changing the market refresh grant")
	}
	return nil
}

// runHealthcheck is deliberately restricted to the public health endpoint so
// the distroless runtime can have a useful Docker HEALTHCHECK without adding a
// shell, curl, wget, or any access to private API routes.
func runHealthcheck(args []string) error {
	const maxHealthcheckTimeout = 30 * time.Second
	const maxHealthcheckBody = 4 << 10
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	target := flags.String("url", "http://127.0.0.1:8080/healthz", "health endpoint URL")
	timeout := flags.Duration("timeout", 2*time.Second, "maximum request duration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *timeout <= 0 || *timeout > maxHealthcheckTimeout {
		return fmt.Errorf("-timeout must be positive and no greater than %s", maxHealthcheckTimeout)
	}
	parsed, err := url.Parse(*target)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "/healthz" || parsed.EscapedPath() != "/healthz" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("-url must be a literal loopback HTTP(S) /healthz URL without query parameters")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("-url must use HTTP or HTTPS")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || (host != "127.0.0.1" && host != "::1") {
		return errors.New("-url must use an explicit port on literal 127.0.0.1 or ::1")
	}
	request, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}
	client := &http.Client{
		Timeout:   *timeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("healthcheck redirects are not allowed")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return fmt.Errorf("healthcheck request failed: %w", err)
	}
	defer response.Body.Close()
	read, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxHealthcheckBody+1))
	if readErr != nil {
		return errors.New("healthcheck response could not be read")
	}
	if read > maxHealthcheckBody {
		return errors.New("healthcheck response exceeded the maximum size")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
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
	idempotencyKey := flags.String("idempotency-key", "", "optional bounded retry key; defaults to the as-of instant")
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
	if strings.TrimSpace(*idempotencyKey) == "" {
		*idempotencyKey = cliMarketRefreshIdempotencyKey(*portfolioID, when)
	}
	if !cliMarketRefreshIdempotencyKeyPattern.MatchString(strings.TrimSpace(*idempotencyKey)) {
		return errors.New("-idempotency-key must be 8 to 128 bounded identifier characters")
	}
	service := marketrefresh.New(db, clients, store.MarketProviders(map[marketdata.Provider]bool{
		marketdata.ProviderFinnhub:           cfg.ProviderKeys.Finnhub != "",
		marketdata.ProviderAlphaVantage:      cfg.ProviderKeys.AlphaVantage != "",
		marketdata.ProviderOpenExchangeRates: cfg.ProviderKeys.OpenExchangeRates != "",
	}))
	result, _, err := service.Refresh(ctx, marketrefresh.Request{
		PortfolioID:       *portfolioID,
		ReportingCurrency: *reportingCurrency,
		IdempotencyKey:    strings.TrimSpace(*idempotencyKey),
		Actor:             store.MutationActor{Category: "cli", ID: "market-refresh-cli", Permissions: []string{"market_data:refresh"}},
		AsOf:              when,
		Providers:         providers,
		FingerprintScope:  cliMarketRefreshFingerprintScope(providers, when),
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

var cliMarketRefreshIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{7,127}$`)

func cliMarketRefreshIdempotencyKey(portfolioID string, asOf time.Time) string {
	witness := sha256.Sum256([]byte("cli\x00" + portfolioID + "\x00" + asOf.UTC().Format(time.RFC3339Nano)))
	return "cli-" + hex.EncodeToString(witness[:])
}

func cliMarketRefreshFingerprintScope(providers []marketdata.Provider, asOf time.Time) string {
	encoded, _ := json.Marshal(struct {
		Providers []marketdata.Provider `json:"providers"`
		AsOf      string                `json:"as_of"`
	}{providers, asOf.UTC().Format(time.RFC3339Nano)})
	return string(encoded)
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
