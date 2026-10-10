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
		ids, _ := url.QueryUnescape(query.Get("ids"))
		if ids != "bitcoin,ethereum,xrp" {
			t.Errorf("ids query = %q", ids)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bitcoin":{"usd":"100.00"},"ethereum":{"usd":200},"xrp":{"usd":0.50}}`))
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
