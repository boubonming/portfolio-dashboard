package marketdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureNow() time.Time { return time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC) }

func TestFinnhubHappyAndSecretRedaction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("token") != "fixture-secret" {
			t.Errorf("token was not sent to provider request")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("symbol") != "NYSE:ABC" {
			t.Errorf("symbol = %q", r.URL.Query().Get("symbol"))
		}
		_, _ = w.Write([]byte(`{"c":12.50,"t":1767355200}`))
	}))
	defer server.Close()
	client := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: server.URL, Now: fixtureNow})
	quote, err := client.Quote(context.Background(), QuoteRequest{InstrumentID: "instrument-1", Symbol: "LOCAL", ProviderSymbol: "NYSE:ABC", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price != "12.5" || quote.Currency != "USD" || quote.Source != "finnhub" {
		t.Fatalf("quote = %+v", quote)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}

	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	failure := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: unauthorized.URL, Now: fixtureNow})
	_, err = failure.Quote(context.Background(), QuoteRequest{InstrumentID: "instrument-1", Symbol: "ABC", Currency: "USD"})
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func TestAlphaVantageHappyAndRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query()
		if query.Get("apikey") != "fixture-secret" || query.Get("symbol") != "LSE:ABC" {
			t.Errorf("query = %v", query)
		}
		_, _ = w.Write([]byte(`{"Global Quote":{"01. symbol":"LSE:ABC","05. price":"4.2500","07. latest trading day":"2026-01-02"}}`))
	}))
	defer server.Close()
	client := NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: server.URL, Now: fixtureNow})
	quote, err := client.Quote(context.Background(), QuoteRequest{InstrumentID: "instrument-2", Symbol: "LOCAL", ProviderSymbol: "LSE:ABC", Currency: "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price != "4.25" || quote.Basis != "end_of_day" {
		t.Fatalf("quote = %+v", quote)
	}

	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Note":"rate limit"}`))
	}))
	defer limited.Close()
	client = NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: limited.URL, Now: fixtureNow})
	_, err = client.Quote(context.Background(), QuoteRequest{InstrumentID: "instrument-2", Symbol: "LOCAL", ProviderSymbol: "LSE:ABC", Currency: "GBP"})
	if err == nil || !strings.Contains(err.Error(), "rate_limit") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("rate-limit error = %v", err)
	}
}

func TestCoinGeckoBatchesMappedIDs(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query := r.URL.Query()
		if query.Get("vs_currencies") != "usd" {
			t.Errorf("currency query = %q", query.Get("vs_currencies"))
		}
		if query.Get("include_last_updated_at") != "true" {
			t.Errorf("include_last_updated_at = %q", query.Get("include_last_updated_at"))
		}
		ids, _ := url.QueryUnescape(query.Get("ids"))
		if ids != "bitcoin,ethereum,xrp" {
			t.Errorf("ids query = %q", ids)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bitcoin":{"usd":"100.00","last_updated_at":1767351600},"ethereum":{"usd":200,"last_updated_at":1767351600},"xrp":{"usd":0.50,"last_updated_at":1767351600}}`))
	}))
	defer server.Close()
	client := NewCoinGeckoClient(ClientOptions{BaseURL: server.URL, Now: fixtureNow})
	requests := []QuoteRequest{
		{InstrumentID: "btc", Symbol: "BTC", ProviderSymbol: "bitcoin", Currency: "USD"},
		{InstrumentID: "eth", Symbol: "ETH", ProviderSymbol: "ethereum", Currency: "USD"},
		{InstrumentID: "xrp", Symbol: "XRP", ProviderSymbol: "xrp", Currency: "USD"},
	}
	quotes, failures := client.QuoteBatch(context.Background(), requests)
	if len(failures) != 0 || len(quotes) != 3 || calls.Load() != 1 {
		t.Fatalf("quotes=%d failures=%d calls=%d", len(quotes), len(failures), calls.Load())
	}
	if quotes["xrp"].Price != "0.5" {
		t.Fatalf("xrp quote = %+v", quotes["xrp"])
	}
	if quotes["xrp"].MarketAt != "2026-01-02T11:00:00Z" {
		t.Fatalf("xrp market time = %q", quotes["xrp"].MarketAt)
	}
}

func TestCoinGeckoBatchesEachQuoteCurrencyAndRejectsMissingTimestamp(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("include_last_updated_at") != "true" {
			t.Errorf("timestamp query = %q", r.URL.Query().Get("include_last_updated_at"))
		}
		if r.URL.Query().Get("vs_currencies") == "eur" {
			_, _ = w.Write([]byte(`{"bitcoin":{"eur":90,"last_updated_at":1767351600}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ethereum":{"usd":200}}`))
	}))
	defer server.Close()
	client := NewCoinGeckoClient(ClientOptions{BaseURL: server.URL, Now: fixtureNow})
	quotes, failures := client.QuoteBatch(context.Background(), []QuoteRequest{
		{InstrumentID: "btc", Symbol: "BTC", ProviderSymbol: "bitcoin", Currency: "EUR"},
		{InstrumentID: "eth", Symbol: "ETH", ProviderSymbol: "ethereum", Currency: "USD"},
	})
	if calls.Load() != 2 || len(quotes) != 1 || len(failures) != 1 {
		t.Fatalf("quotes=%d failures=%d calls=%d", len(quotes), len(failures), calls.Load())
	}
	if !strings.Contains(failures["eth"].Error(), "invalid_timestamp") {
		t.Fatalf("missing timestamp error = %v", failures["eth"])
	}
}

func TestOpenExchangeRatesOneCallAndExactCrossRate(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("app_id") != "fixture-secret" || r.URL.Query().Get("base") != "USD" {
			t.Errorf("query = %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		stamp := fixtureNow().Add(-time.Hour).Unix()
		_, _ = fmt.Fprintf(w, `{"timestamp":%d,"base":"USD","rates":{"EUR":"2","MYR":"5"}}`, stamp)
	}))
	defer server.Close()
	client := NewOpenExchangeRatesClient("fixture-secret", ClientOptions{BaseURL: server.URL, Now: fixtureNow})
	rates, failures := client.FXBatch(context.Background(), []FXRequest{{BaseCurrency: "EUR", QuoteCurrency: "MYR"}, {BaseCurrency: "USD", QuoteCurrency: "MYR"}})
	if len(failures) != 0 || len(rates) != 2 || calls.Load() != 1 {
		t.Fatalf("rates=%d failures=%d calls=%d", len(rates), len(failures), calls.Load())
	}
	if rates["EUR/MYR"].Rate != "2.5" || rates["USD/MYR"].Rate != "5" {
		t.Fatalf("rates = %+v", rates)
	}
}

func TestProviderHTTPFailuresAreBoundedAndRedacted(t *testing.T) {
	type adapter struct {
		name string
		call func(context.Context, string) error
	}
	adapters := []adapter{
		{name: "finnhub", call: func(ctx context.Context, baseURL string) error {
			_, err := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: baseURL, Now: fixtureNow}).Quote(ctx, QuoteRequest{InstrumentID: "fixture", ProviderSymbol: "NYSE:FIX", Currency: "USD"})
			return err
		}},
		{name: "alpha_vantage", call: func(ctx context.Context, baseURL string) error {
			_, err := NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: baseURL, Now: fixtureNow}).Quote(ctx, QuoteRequest{InstrumentID: "fixture", ProviderSymbol: "LSE:FIX", Currency: "GBP"})
			return err
		}},
		{name: "coingecko", call: func(ctx context.Context, baseURL string) error {
			_, err := NewCoinGeckoClient(ClientOptions{BaseURL: baseURL, Now: fixtureNow}).Quote(ctx, QuoteRequest{InstrumentID: "fixture", ProviderSymbol: "bitcoin", Currency: "USD"})
			return err
		}},
		{name: "open_exchange_rates", call: func(ctx context.Context, baseURL string) error {
			_, failures := NewOpenExchangeRatesClient("fixture-secret", ClientOptions{BaseURL: baseURL, Now: fixtureNow}).FXBatch(ctx, []FXRequest{{BaseCurrency: "EUR", QuoteCurrency: "MYR"}})
			return failures["EUR/MYR"]
		}},
	}
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		class       string
	}{
		{name: "malformed_json", status: http.StatusOK, contentType: "application/json", body: "{", class: "malformed_json"},
		{name: "non_json", status: http.StatusOK, contentType: "text/plain", body: "provider failure", class: "content_type"},
		{name: "unauthorized", status: http.StatusUnauthorized, contentType: "application/json", body: `{}`, class: "auth"},
		{name: "forbidden", status: http.StatusForbidden, contentType: "application/json", body: `{}`, class: "auth"},
		{name: "rate_limited", status: http.StatusTooManyRequests, contentType: "application/json", body: `{}`, class: "rate_limit"},
		{name: "oversized_body", status: http.StatusOK, contentType: "application/json", body: strings.Repeat("x", maxProviderBody+1), class: "body_too_large"},
	}
	for _, provider := range adapters {
		for _, testCase := range cases {
			t.Run(provider.name+"/"+testCase.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", testCase.contentType)
					w.WriteHeader(testCase.status)
					_, _ = w.Write([]byte(testCase.body))
				}))
				defer server.Close()
				err := provider.call(context.Background(), server.URL)
				if err == nil || !strings.Contains(err.Error(), testCase.class) || strings.Contains(err.Error(), "fixture-secret") {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
}

func TestProviderPayloadValidation(t *testing.T) {
	cases := []struct {
		name  string
		body  func() string
		call  func(context.Context, string) error
		class string
	}{
		{name: "finnhub_timestamp", body: func() string { return `{"c":12,"t":0}` }, call: func(ctx context.Context, baseURL string) error {
			_, err := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: baseURL, Now: fixtureNow}).Quote(ctx, QuoteRequest{InstrumentID: "fixture", ProviderSymbol: "NYSE:FIX", Currency: "USD"})
			return err
		}, class: "invalid_timestamp"},
		{name: "alpha_symbol", body: func() string {
			return `{"Global Quote":{"01. symbol":"LSE:OTHER","05. price":"4.2","07. latest trading day":"2026-01-02"}}`
		}, call: func(ctx context.Context, baseURL string) error {
			_, err := NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: baseURL, Now: fixtureNow}).Quote(ctx, QuoteRequest{InstrumentID: "fixture", ProviderSymbol: "LSE:FIX", Currency: "GBP"})
			return err
		}, class: "symbol_mismatch"},
		{name: "coingecko_future_timestamp", body: func() string {
			return fmt.Sprintf(`{"bitcoin":{"usd":1,"last_updated_at":%d}}`, fixtureNow().Add(10*time.Minute).Unix())
		}, call: func(ctx context.Context, baseURL string) error {
			_, err := NewCoinGeckoClient(ClientOptions{BaseURL: baseURL, Now: fixtureNow}).Quote(ctx, QuoteRequest{InstrumentID: "fixture", ProviderSymbol: "bitcoin", Currency: "USD"})
			return err
		}, class: "invalid_timestamp"},
		{name: "oxr_currency", body: func() string {
			return fmt.Sprintf(`{"timestamp":%d,"base":"EUR","rates":{"MYR":5}}`, fixtureNow().Add(-time.Hour).Unix())
		}, call: func(ctx context.Context, baseURL string) error {
			_, failures := NewOpenExchangeRatesClient("fixture-secret", ClientOptions{BaseURL: baseURL, Now: fixtureNow}).FXBatch(ctx, []FXRequest{{BaseCurrency: "EUR", QuoteCurrency: "MYR"}})
			return failures["EUR/MYR"]
		}, class: "currency_mismatch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body()))
			}))
			defer server.Close()
			err := testCase.call(context.Background(), server.URL)
			if err == nil || !strings.Contains(err.Error(), testCase.class) || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
