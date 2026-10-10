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

const defaultOpenExchangeRatesURL = "https://openexchangerates.org/api"

type OpenExchangeRatesClient struct {
	appID   string
	baseURL string
	http    HTTPDoer
	now     func() time.Time
}

func NewOpenExchangeRatesClient(appID string, options ClientOptions) *OpenExchangeRatesClient {
	base, client, now := optionsOrDefault(options, defaultOpenExchangeRatesURL)
	return &OpenExchangeRatesClient{appID: strings.TrimSpace(appID), baseURL: base, http: client, now: now}
}
func NewOpenExchangeRates(appID, baseURL string) *OpenExchangeRatesClient {
	return NewOpenExchangeRatesClient(appID, ClientOptions{BaseURL: baseURL})
}
func (c *OpenExchangeRatesClient) Name() Provider { return ProviderOpenExchangeRates }
func (c *OpenExchangeRatesClient) Quote(context.Context, QuoteRequest) (domain.Quote, error) {
	return domain.Quote{}, ErrUnsupportedOperation
}

func (c *OpenExchangeRatesClient) FX(ctx context.Context, request FXRequest) (domain.FXRate, error) {
	rates, failures := c.FXBatch(ctx, []FXRequest{request})
	key := strings.ToUpper(request.BaseCurrency) + "/" + strings.ToUpper(request.QuoteCurrency)
	if rate, ok := rates[key]; ok {
		return rate, nil
	}
	if err, ok := failures[key]; ok {
		return domain.FXRate{}, err
	}
	return domain.FXRate{}, providerErr(c.Name(), "provider_error", "provider returned no mapped FX rate")
}

// FXBatch makes one latest-rates call for all required pairs. OXR's free API
// has a USD base, so non-USD cross-rates are derived exactly from USD rates.
func (c *OpenExchangeRatesClient) FXBatch(ctx context.Context, requests []FXRequest) (map[string]domain.FXRate, map[string]error) {
	result := map[string]domain.FXRate{}
	failures := map[string]error{}
	if c.appID == "" {
		for _, request := range requests {
			failures[fxKey(request)] = providerErr(c.Name(), "configuration", "OPEN_EXCHANGE_RATES_APP_ID is not configured")
		}
		return result, failures
	}
	unique := map[string]FXRequest{}
	for _, request := range requests {
		key := fxKey(request)
		base, quote := strings.ToUpper(strings.TrimSpace(request.BaseCurrency)), strings.ToUpper(strings.TrimSpace(request.QuoteCurrency))
		if base == "" || quote == "" || base == quote {
			failures[key] = providerErr(c.Name(), "invalid_request", "FX currencies must be distinct")
			continue
		}
		unique[key] = FXRequest{BaseCurrency: base, QuoteCurrency: quote, AsOf: request.AsOf}
	}
	if len(unique) == 0 {
		return result, failures
	}
	query := url.Values{"app_id": {c.appID}, "base": {"USD"}}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL(c.baseURL, "/latest.json", query), nil)
	if err != nil {
		for key := range unique {
			failures[key] = providerErr(c.Name(), "request", "could not construct provider request")
		}
		return result, failures
	}
	body, err := requestJSON(ctx, c.http, c.Name(), httpRequest)
	if err != nil {
		for key := range unique {
			failures[key] = err
		}
		return result, failures
	}
	var payload struct {
		Timestamp int64                      `json:"timestamp"`
		Base      string                     `json:"base"`
		Rates     map[string]json.RawMessage `json:"rates"`
		Error     bool                       `json:"error"`
		Message   string                     `json:"description"`
	}
	if err := decodeObject(body, c.Name(), &payload); err != nil {
		for key := range unique {
			failures[key] = err
		}
		return result, failures
	}
	if payload.Error {
		error := providerErr(c.Name(), "provider_error", "provider returned an error payload")
		for key := range unique {
			failures[key] = error
		}
		return result, failures
	}
	if !strings.EqualFold(payload.Base, "USD") {
		error := providerErr(c.Name(), "currency_mismatch", "provider base currency was not USD")
		for key := range unique {
			failures[key] = error
		}
		return result, failures
	}
	marketAt, timestampErr := parseProviderTimestamp(c.Name(), time.Unix(payload.Timestamp, 0), c.now())
	if timestampErr != nil {
		for key := range unique {
			failures[key] = timestampErr
		}
		return result, failures
	}
	usd := domain.MustDecimal("1")
	usdRates := map[string]domain.Decimal{"USD": usd}
	for currency, raw := range payload.Rates {
		parsed, parseErr := decimalRaw(raw, c.Name(), "USD rate")
		if parseErr != nil {
			for key, request := range unique {
				if strings.EqualFold(currency, request.BaseCurrency) || strings.EqualFold(currency, request.QuoteCurrency) {
					failures[key] = parseErr
				}
			}
			continue
		}
		usdRates[strings.ToUpper(currency)] = parsed
	}
	fetched := c.now().UTC().Format(time.RFC3339Nano)
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		request := unique[key]
		baseRate, baseOK := usdRates[request.BaseCurrency]
		quoteRate, quoteOK := usdRates[request.QuoteCurrency]
		if !baseOK || !quoteOK {
			failures[key] = providerErr(c.Name(), "currency_mismatch", "requested currency was absent from USD rate response")
			continue
		}
		rate, divErr := quoteRate.Div(baseRate, 18)
		if divErr != nil || rate.Sign() <= 0 {
			failures[key] = providerErr(c.Name(), "invalid_value", "derived cross-rate was invalid")
			continue
		}
		result[key] = domain.FXRate{ID: fxID(c.Name().String(), request, rate.String(), marketAt), BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: rate.String(), Source: c.Name().String(), MarketAt: marketAt, FetchedAt: fetched, Basis: "USD-base cross-rate", Provenance: "Open Exchange Rates latest endpoint; exact decimal USD-base derivation"}
	}
	return result, failures
}

func fxKey(request FXRequest) string {
	return strings.ToUpper(strings.TrimSpace(request.BaseCurrency)) + "/" + strings.ToUpper(strings.TrimSpace(request.QuoteCurrency))
}
