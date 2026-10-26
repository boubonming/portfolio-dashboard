package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestMarketRefreshCLIUsesDurableCanonicalWorkflow(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "cli-refresh.sqlite")
	database, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"cli-portfolio", "CLI", "USD", base, base}},
		{`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, []any{"cli-instrument", "CLI", "USD", base}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"cli-portfolio", "cli-instrument", base}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"cli-lot", "cli-portfolio", "cli-instrument", "1", "USD", "active", base, base}},
	} {
		if _, err := database.DB.ExecContext(ctx, statement.query, statement.args...); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	if _, err := database.AddProviderMapping(ctx, store.ProviderMappingInput{
		InstrumentID: "cli-instrument", Provider: marketdata.ProviderCoinGecko, ProviderSymbol: "cli-coin", QuoteCurrency: "USD", ActiveFrom: base, Provenance: "cli-test",
	}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cli-coin":{"usd":123.45,"last_updated_at":` + strconv.FormatInt(time.Now().UTC().Add(-time.Minute).Unix(), 10) + `}}`))
	}))
	defer server.Close()
	t.Setenv("PORTFOLIO_COINGECKO_BASE_URL", server.URL)

	asOf := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano)
	args := []string{"-db", databasePath, "-portfolio", "cli-portfolio", "-providers", "coingecko", "-as-of", asOf}
	if err := runMarketRefresh(args); err != nil {
		t.Fatal(err)
	}
	if err := runMarketRefresh(args); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("replayed CLI refresh called provider %d times", providerCalls.Load())
	}

	database, err = store.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	checks := []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM market_refresh_idempotency WHERE actor_category='cli' AND actor_id='market-refresh-cli' AND state='committed'`, 1},
		{`SELECT COUNT(*) FROM snapshots WHERE portfolio_id='cli-portfolio'`, 1},
		{`SELECT COUNT(*) FROM activity_events WHERE portfolio_id='cli-portfolio' AND actor_category='cli' AND action='market.refresh.completed'`, 1},
		{`SELECT COUNT(*) FROM audit_events WHERE actor='market-refresh-cli' AND action='market.refresh.completed'`, 1},
	}
	for _, check := range checks {
		var got int
		if err := database.DB.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("query %q returned %d, want %d", check.query, got, check.want)
		}
	}
}
