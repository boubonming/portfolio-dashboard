// Package marketdata defines provider-neutral boundaries for dated market
// observations. This phase intentionally has no network clients.
package marketdata

import (
	"context"
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

type QuoteRequest struct {
	InstrumentID, Symbol, Currency string
	AsOf                           time.Time
}
type FXRequest struct {
	BaseCurrency, QuoteCurrency string
	AsOf                        time.Time
}

type ProviderClient interface {
	Name() Provider
	Quote(context.Context, QuoteRequest) (domain.Quote, error)
	FX(context.Context, FXRequest) (domain.FXRate, error)
}

// NoLiveClients documents that ingestion is administrative/manual until a
// credential-gated connector task adds implementations.
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

var ErrLiveProviderDisabled error = disabledError("live market-data providers are disabled in this phase")
