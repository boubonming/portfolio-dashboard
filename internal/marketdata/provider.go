// Package marketdata contains credential-safe, provider-neutral market data
// boundaries and the free-tier HTTP adapters. Every adapter is injectable with
// a local HTTP server for tests; no adapter retries automatically.
package marketdata

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

type Provider string

const (
	ProviderFinnhub           Provider = "finnhub"
	ProviderAlphaVantage      Provider = "alpha_vantage"
	ProviderCoinGecko         Provider = "coingecko"
	ProviderOpenExchangeRates Provider = "open_exchange_rates"
	ProviderManual            Provider = "manual"
)

func (p Provider) String() string { return string(p) }

func ParseProvider(value string) (Provider, error) {
	p := Provider(value)
	switch p {
	case ProviderFinnhub, ProviderAlphaVantage, ProviderCoinGecko, ProviderOpenExchangeRates, ProviderManual:
		return p, nil
	default:
		return "", errors.New("unsupported provider")
	}
}

type QuoteRequest struct {
	InstrumentID   string
	Symbol         string // Original symbol, retained for identity validation.
	ProviderSymbol string // Explicit provider mapping; never inferred.
	Currency       string
	AsOf           time.Time
}
type FXRequest struct {
	BaseCurrency  string
	QuoteCurrency string
	AsOf          time.Time
}

type ProviderClient interface {
	Name() Provider
	Quote(context.Context, QuoteRequest) (domain.Quote, error)
	FX(context.Context, FXRequest) (domain.FXRate, error)
}

// BatchQuoteClient permits providers with a batch endpoint to make one bounded
// request. The returned errors are keyed by instrument ID.
type BatchQuoteClient interface {
	ProviderClient
	QuoteBatch(context.Context, []QuoteRequest) (map[string]domain.Quote, map[string]error)
}

// BatchQuoteRequestCounter reports the number of HTTP requests made by a
// provider for a batch. Providers with one request per batch need not
// implement it; providers that split a batch by currency can report their
// actual request count.
type BatchQuoteRequestCounter interface {
	QuoteBatchRequestCount([]QuoteRequest) int
}

type BatchFXClient interface {
	ProviderClient
	FXBatch(context.Context, []FXRequest) (map[string]domain.FXRate, map[string]error)
}

type ClientOptions struct {
	BaseURL    string
	HTTPClient HTTPDoer
	Timeout    time.Duration
	Now        func() time.Time
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// NoLiveClients remains useful to callers that intentionally disable refresh.
type NoLiveClients struct{}

func (NoLiveClients) Name() Provider { return ProviderManual }
func (NoLiveClients) Quote(context.Context, QuoteRequest) (domain.Quote, error) {
	return domain.Quote{}, ErrLiveProviderDisabled
}
func (NoLiveClients) FX(context.Context, FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, ErrLiveProviderDisabled
}

type disabledError string

func (e disabledError) Error() string { return string(e) }

var ErrLiveProviderDisabled error = disabledError("live market-data providers are disabled")
var ErrUnsupportedOperation error = disabledError("provider does not support this observation type")
