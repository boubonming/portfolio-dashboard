package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoveryAdaptersUseProviderSearchEndpointsAndBoundMetadata(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		client   func(string) DiscoveryClient
		path     string
		body     string
		wantID   string
	}{
		{name: "coingecko", provider: ProviderCoinGecko, client: func(base string) DiscoveryClient { return NewCoinGeckoClient(ClientOptions{BaseURL: base}) }, path: "/api/v3/search", body: `{"coins":[{"id":"bitcoin","symbol":"btc","name":"Bitcoin"}]}`, wantID: "bitcoin"},
		{name: "finnhub", provider: ProviderFinnhub, client: func(base string) DiscoveryClient {
			return NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: base})
		}, path: "/search", body: `{"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock"}]}`, wantID: "MSFT"},
		{name: "alpha_vantage", provider: ProviderAlphaVantage, client: func(base string) DiscoveryClient {
			return NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: base + "/query"})
		}, path: "/query", body: `{"bestMatches":[{"1. symbol":"7203.T","2. name":"Toyota","3. type":"Equity","4. region":"Japan","8. currency":"JPY"}]}`, wantID: "7203.T"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path || r.URL.Query().Get("query") == "" && r.URL.Query().Get("symbol") == "" && r.URL.Query().Get("keywords") == "" {
					t.Fatalf("unexpected request: %s", r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			candidates, err := test.client(server.URL).Search(context.Background(), "MSFT")
			if err != nil || len(candidates) != 1 || candidates[0].Identifier != test.wantID {
				t.Fatalf("candidates=%+v err=%v", candidates, err)
			}
		})
	}
}

func TestDiscoveryAdapterRedactsProviderSecretOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"fixture"}`))
	}))
	defer server.Close()
	_, err := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: server.URL}).Search(context.Background(), "MSFT")
	if err == nil || !strings.Contains(err.Error(), "rate_limit") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unexpected redaction/error: %v", err)
	}
}
