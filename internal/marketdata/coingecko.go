package marketdata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

const defaultCoinGeckoURL = "https://api.coingecko.com"

type CoinGeckoClient struct {
	baseURL string
	http    HTTPDoer
	now     func() time.Time
}

func NewCoinGeckoClient(options ClientOptions) *CoinGeckoClient {
	base, client, now := optionsOrDefault(options, defaultCoinGeckoURL)
	return &CoinGeckoClient{baseURL: base, http: client, now: now}
}
func NewCoinGecko(baseURL string) *CoinGeckoClient {
	return NewCoinGeckoClient(ClientOptions{BaseURL: baseURL})
}
func (c *CoinGeckoClient) Name() Provider { return ProviderCoinGecko }
func (c *CoinGeckoClient) FX(context.Context, FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, ErrUnsupportedOperation
}

func (c *CoinGeckoClient) Quote(ctx context.Context, request QuoteRequest) (domain.Quote, error) {
	quotes, failures := c.QuoteBatch(ctx, []QuoteRequest{request})
	if quote, ok := quotes[request.InstrumentID]; ok {
		return quote, nil
	}
	if err, ok := failures[request.InstrumentID]; ok {
		return domain.Quote{}, err
	}
	return domain.Quote{}, providerErr(c.Name(), "provider_error", "provider returned no mapped quote")
}

// QuoteBatch groups requests by quote currency and performs one request for
// each currency. BTC/ETH/XRP mappings with USD therefore use exactly one
// request while preserving one normalized observation per instrument.
func (c *CoinGeckoClient) QuoteBatch(ctx context.Context, requests []QuoteRequest) (map[string]domain.Quote, map[string]error) {
	quotes := map[string]domain.Quote{}
	failures := map[string]error{}
	groups := map[string][]QuoteRequest{}
	for _, request := range requests {
		if err := validateQuoteRequest(request); err != nil {
			failures[request.InstrumentID] = providerErr(c.Name(), "invalid_request", err.Error())
			continue
		}
		currency := strings.ToLower(strings.TrimSpace(request.Currency))
		groups[currency] = append(groups[currency], request)
	}
	currencies := make([]string, 0, len(groups))
	for currency := range groups {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	for _, currency := range currencies {
		group := groups[currency]
		ids := make([]string, 0, len(group))
		byID := map[string]QuoteRequest{}
		for _, request := range group {
			id := normalizedSymbol(request)
			ids = append(ids, id)
			byID[id] = request
		}
		sort.Strings(ids)
		query := url.Values{"ids": {strings.Join(ids, ",")}, "vs_currencies": {currency}}
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL(c.baseURL, "/api/v3/simple/price", query), nil)
		if err != nil {
			for _, request := range group {
				failures[request.InstrumentID] = providerErr(c.Name(), "request", "could not construct provider request")
			}
			continue
		}
		body, err := requestJSON(ctx, c.http, c.Name(), httpRequest)
		if err != nil {
			for _, request := range group {
				failures[request.InstrumentID] = err
			}
			continue
		}
		var payload map[string]map[string]json.RawMessage
		if err := decodeObject(body, c.Name(), &payload); err != nil {
			for _, request := range group {
				failures[request.InstrumentID] = err
			}
			continue
		}
		fetched := c.now().UTC()
		marketAt, timestampErr := parseProviderTimestamp(c.Name(), fetched, fetched)
		if timestampErr != nil {
			for _, request := range group {
				failures[request.InstrumentID] = timestampErr
			}
			continue
		}
		for _, id := range ids {
			request := byID[id]
			values, ok := payload[id]
			if !ok {
				failures[request.InstrumentID] = providerErr(c.Name(), "symbol_mismatch", "mapped coin ID was absent from response")
				continue
			}
			price, ok := values[currency]
			if !ok {
				failures[request.InstrumentID] = providerErr(c.Name(), "currency_mismatch", "mapped quote currency was absent from response")
				continue
			}
			parsed, parseErr := decimalRaw(price, c.Name(), "coin price")
			if parseErr != nil {
				failures[request.InstrumentID] = parseErr
				continue
			}
			quotes[request.InstrumentID] = domain.Quote{ID: quoteID(c.Name().String(), request, parsed.String(), marketAt), InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: parsed.String(), Currency: strings.ToUpper(request.Currency), Source: c.Name().String(), MarketAt: marketAt, FetchedAt: fetched.Format(time.RFC3339Nano), Basis: "spot", Provenance: "CoinGecko simple price endpoint; explicit coin ID mapping"}
		}
	}
	return quotes, failures
}
