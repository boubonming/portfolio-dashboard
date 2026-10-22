package marketdata

import (
	"context"
	"fmt"
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
		}, path: "/search", body: `{"count":1,"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock"}]}`, wantID: "MSFT"},
		{name: "alpha_vantage", provider: ProviderAlphaVantage, client: func(base string) DiscoveryClient {
			return NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: base + "/query"})
		}, path: "/query", body: `{"bestMatches":[{"1. symbol":"7203.T","2. name":"Toyota","3. type":"Equity","4. region":"Japan","8. currency":"JPY"}]}`, wantID: "7203.T"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path || r.URL.Query().Get("query") == "" && r.URL.Query().Get("q") == "" && r.URL.Query().Get("symbol") == "" && r.URL.Query().Get("keywords") == "" {
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

func TestDiscoveryAdaptersRejectUntrustedSearchShapes(t *testing.T) {
	tests := []struct {
		name        string
		provider    Provider
		client      func(string) DiscoveryClient
		path        string
		valid       string
		unknown     string
		duplicateID string
	}{
		{name: "coingecko", provider: ProviderCoinGecko, client: func(base string) DiscoveryClient { return NewCoinGeckoClient(ClientOptions{BaseURL: base}) }, path: "/api/v3/search", valid: `{"coins":[{"id":"bitcoin","symbol":"btc","name":"Bitcoin"}]}`, unknown: `{"coins":[{"id":"bitcoin","symbol":"btc","name":"Bitcoin","unexpected":"x"}]}`, duplicateID: `{"coins":[{"id":"bitcoin","symbol":"btc","name":"Bitcoin"},{"id":"bitcoin","symbol":"btc","name":"Other"}]}`},
		{name: "finnhub", provider: ProviderFinnhub, client: func(base string) DiscoveryClient {
			return NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: base})
		}, path: "/search", valid: `{"count":1,"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock"}]}`, unknown: `{"count":1,"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock","unexpected":"x"}]}`, duplicateID: `{"count":2,"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock"},{"symbol":"MSFT","displaySymbol":"MSFT","description":"Other","type":"Common Stock"}]}`},
		{name: "alpha_vantage", provider: ProviderAlphaVantage, client: func(base string) DiscoveryClient {
			return NewAlphaVantageClient("fixture-secret", ClientOptions{BaseURL: base + "/query"})
		}, path: "/query", valid: `{"bestMatches":[{"1. symbol":"7203.T","2. name":"Toyota","3. type":"Equity","4. region":"Japan","8. currency":"JPY"}]}`, unknown: `{"bestMatches":[{"1. symbol":"7203.T","2. name":"Toyota","3. type":"Equity","4. region":"Japan","8. currency":"JPY","unexpected":"x"}]}`, duplicateID: `{"bestMatches":[{"1. symbol":"7203.T","2. name":"Toyota","3. type":"Equity","4. region":"Japan","8. currency":"JPY"},{"1. symbol":"7203.T","2. name":"Other","3. type":"Equity","4. region":"Japan","8. currency":"JPY"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			duplicateKey := test.valid
			switch test.provider {
			case ProviderCoinGecko:
				duplicateKey = strings.Replace(test.valid, `"id":"bitcoin"`, `"id":"bitcoin","id":"other"`, 1)
			case ProviderFinnhub:
				duplicateKey = strings.Replace(test.valid, `"symbol":"MSFT"`, `"symbol":"MSFT","symbol":"OTHER"`, 1)
			case ProviderAlphaVantage:
				duplicateKey = strings.Replace(test.valid, `"1. symbol":"7203.T"`, `"1. symbol":"7203.T","1. symbol":"OTHER"`, 1)
			}
			bodies := []struct {
				name string
				body string
			}{
				{name: "unknown field", body: test.unknown},
				{name: "duplicate key", body: duplicateKey},
				{name: "duplicate candidate identifier", body: test.duplicateID},
				{name: "trailing JSON", body: test.valid + ` {}`},
			}
			for _, body := range bodies {
				t.Run(body.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != test.path {
							t.Fatalf("unexpected request path: %s", r.URL.Path)
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(body.body))
					}))
					defer server.Close()
					_, err := test.client(server.URL).Search(context.Background(), "MSFT")
					if err == nil || !strings.Contains(err.Error(), "malformed_json") {
						t.Fatalf("expected bounded malformed response error, got %v", err)
					}
				})
			}
			tooMany := strings.Builder{}
			if test.provider == ProviderCoinGecko {
				tooMany.WriteString(`{"coins":[`)
			} else if test.provider == ProviderFinnhub {
				fmt.Fprintf(&tooMany, `{"count":%d,"result":[`, maxDiscoveryCandidates+1)
			} else {
				tooMany.WriteString(`{"bestMatches":[`)
			}
			for i := 0; i < maxDiscoveryCandidates+1; i++ {
				if i > 0 {
					tooMany.WriteByte(',')
				}
				if test.provider == ProviderCoinGecko {
					fmt.Fprintf(&tooMany, `{"id":"coin-%d","symbol":"coin-%d","name":"Coin"}`, i, i)
				} else if test.provider == ProviderFinnhub {
					fmt.Fprintf(&tooMany, `{"symbol":"SYM%d","displaySymbol":"SYM%d","description":"Company","type":"Common Stock"}`, i, i)
				} else {
					fmt.Fprintf(&tooMany, `{"1. symbol":"SYM%d","2. name":"Company","3. type":"Equity","4. region":"US","8. currency":"USD"}`, i)
				}
			}
			tooMany.WriteString(`]}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tooMany.String()))
			}))
			defer server.Close()
			_, err := test.client(server.URL).Search(context.Background(), "MSFT")
			if err == nil || !strings.Contains(err.Error(), "malformed_json") {
				t.Fatalf("expected oversized record set error, got %v", err)
			}
		})
	}
}

func TestFinnhubSearchUsesStrictDocumentedContract(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "valid nonempty", body: `{"count":1,"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock"}]}`, want: 1},
		{name: "valid zero", body: `{"count":0,"result":[]}`, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/search" {
					t.Fatalf("unexpected request path: %s", r.URL.Path)
				}
				if r.URL.Query().Get("q") != "MSFT" || r.URL.Query().Get("symbol") != "" {
					t.Fatalf("unexpected lookup query: %s", r.URL.RawQuery)
				}
				if got := r.URL.Query().Get("token"); got != "fixture-secret" {
					t.Fatalf("unexpected token query: %q", got)
				}
				if len(r.URL.Query()) != 2 {
					t.Fatalf("unexpected public query fields: %s", r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			candidates, err := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: server.URL + "/api/v1"}).Search(context.Background(), "MSFT")
			if err != nil || len(candidates) != test.want {
				t.Fatalf("candidates=%+v err=%v", candidates, err)
			}
		})
	}
}

func TestFinnhubSearchRejectsInvalidCountAndStrictFields(t *testing.T) {
	valid := `{"count":1,"result":[{"symbol":"MSFT","displaySymbol":"MSFT","description":"Microsoft","type":"Common Stock"}]}`
	tests := []struct {
		name string
		body string
	}{
		{name: "count mismatch", body: strings.Replace(valid, `"count":1`, `"count":0`, 1)},
		{name: "missing count", body: strings.Replace(valid, `"count":1,`, "", 1)},
		{name: "negative count", body: strings.Replace(valid, `"count":1`, `"count":-1`, 1)},
		{name: "non-integer count", body: strings.Replace(valid, `"count":1`, `"count":1.0`, 1)},
		{name: "duplicate top-level field", body: strings.Replace(valid, `"count":1,`, `"count":1,"count":1,`, 1)},
		{name: "unknown top-level field", body: strings.Replace(valid, `"count":1,`, `"count":1,"unexpected":"x",`, 1)},
		{name: "duplicate result field", body: strings.Replace(valid, `"symbol":"MSFT"`, `"symbol":"MSFT","symbol":"OTHER"`, 1)},
		{name: "unknown result field", body: strings.Replace(valid, `"type":"Common Stock"`, `"type":"Common Stock","unexpected":"x"`, 1)},
		{name: "trailing JSON", body: valid + ` {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/search" || r.URL.Query().Get("q") != "MSFT" || r.URL.Query().Get("symbol") != "" {
					t.Fatalf("unexpected request: %s", r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			candidates, err := NewFinnhubClient("fixture-secret", ClientOptions{BaseURL: server.URL + "/api/v1"}).Search(context.Background(), "MSFT")
			if err == nil || !strings.Contains(err.Error(), "malformed_json") || candidates != nil || strings.Contains(err.Error(), "fixture-secret") || len(err.Error()) > 256 {
				t.Fatalf("expected bounded redacted rejection, candidates=%+v err=%v", candidates, err)
			}
		})
	}
}
