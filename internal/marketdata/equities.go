package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

const defaultFinnhubURL = "https://finnhub.io/api/v1"

type FinnhubClient struct {
	apiKey  string
	baseURL string
	http    HTTPDoer
	now     func() time.Time
}

func NewFinnhubClient(apiKey string, options ClientOptions) *FinnhubClient {
	base, client, now := optionsOrDefault(options, defaultFinnhubURL)
	return &FinnhubClient{apiKey: strings.TrimSpace(apiKey), baseURL: base, http: client, now: now}
}

func NewFinnhub(apiKey, baseURL string) *FinnhubClient {
	return NewFinnhubClient(apiKey, ClientOptions{BaseURL: baseURL})
}

func (c *FinnhubClient) Name() Provider { return ProviderFinnhub }
func (c *FinnhubClient) FX(context.Context, FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, ErrUnsupportedOperation
}

func (c *FinnhubClient) Quote(ctx context.Context, request QuoteRequest) (domain.Quote, error) {
	if err := validateQuoteRequest(request); err != nil {
		return domain.Quote{}, providerErr(c.Name(), "invalid_request", err.Error())
	}
	if c.apiKey == "" {
		return domain.Quote{}, providerErr(c.Name(), "configuration", "FINNHUB_API_KEY is not configured")
	}
	query := url.Values{"symbol": {normalizedSymbol(request)}, "token": {c.apiKey}}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL(c.baseURL, "/quote", query), nil)
	if err != nil {
		return domain.Quote{}, providerErr(c.Name(), "request", "could not construct provider request")
	}
	body, err := requestJSON(ctx, c.http, c.Name(), httpRequest)
	if err != nil {
		return domain.Quote{}, err
	}
	var payload struct {
		Current json.RawMessage `json:"c"`
		Epoch   json.RawMessage `json:"t"`
		Error   string          `json:"error"`
	}
	if err := decodeObject(body, c.Name(), &payload); err != nil {
		return domain.Quote{}, err
	}
	if strings.TrimSpace(payload.Error) != "" {
		return domain.Quote{}, providerErr(c.Name(), "provider_error", "provider returned an error payload")
	}
	price, err := decimalRaw(payload.Current, c.Name(), "current price")
	if err != nil {
		return domain.Quote{}, err
	}
	var epoch int64
	if err := json.Unmarshal(payload.Epoch, &epoch); err != nil || epoch <= 0 {
		return domain.Quote{}, providerErr(c.Name(), "invalid_timestamp", "provider timestamp is invalid")
	}
	marketAt, err := parseProviderTimestamp(c.Name(), time.Unix(epoch, 0), c.now())
	if err != nil {
		return domain.Quote{}, err
	}
	return domain.Quote{ID: quoteID(c.Name().String(), request, price.String(), marketAt), InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: price.String(), Currency: strings.ToUpper(request.Currency), Source: c.Name().String(), MarketAt: marketAt, FetchedAt: c.now().UTC().Format(time.RFC3339Nano), Basis: "intraday", Provenance: "Finnhub quote endpoint; explicit provider mapping"}, nil
}

const defaultAlphaVantageURL = "https://www.alphavantage.co/query"

type AlphaVantageClient struct {
	apiKey  string
	baseURL string
	http    HTTPDoer
	now     func() time.Time
}

func NewAlphaVantageClient(apiKey string, options ClientOptions) *AlphaVantageClient {
	base, client, now := optionsOrDefault(options, defaultAlphaVantageURL)
	return &AlphaVantageClient{apiKey: strings.TrimSpace(apiKey), baseURL: base, http: client, now: now}
}
func NewAlphaVantage(apiKey, baseURL string) *AlphaVantageClient {
	return NewAlphaVantageClient(apiKey, ClientOptions{BaseURL: baseURL})
}
func (c *AlphaVantageClient) Name() Provider { return ProviderAlphaVantage }
func (c *AlphaVantageClient) FX(context.Context, FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, ErrUnsupportedOperation
}

func (c *AlphaVantageClient) Quote(ctx context.Context, request QuoteRequest) (domain.Quote, error) {
	if err := validateQuoteRequest(request); err != nil {
		return domain.Quote{}, providerErr(c.Name(), "invalid_request", err.Error())
	}
	if c.apiKey == "" {
		return domain.Quote{}, providerErr(c.Name(), "configuration", "ALPHA_VANTAGE_API_KEY is not configured")
	}
	query := url.Values{"function": {"GLOBAL_QUOTE"}, "symbol": {normalizedSymbol(request)}, "apikey": {c.apiKey}}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL(c.baseURL, "", query), nil)
	if err != nil {
		return domain.Quote{}, providerErr(c.Name(), "request", "could not construct provider request")
	}
	body, err := requestJSON(ctx, c.http, c.Name(), httpRequest)
	if err != nil {
		return domain.Quote{}, err
	}
	var payload struct {
		GlobalQuote map[string]json.RawMessage `json:"Global Quote"`
		Note        string                     `json:"Note"`
		Information string                     `json:"Information"`
		Error       string                     `json:"Error Message"`
	}
	if err := decodeObject(body, c.Name(), &payload); err != nil {
		return domain.Quote{}, err
	}
	if payload.Note != "" || payload.Information != "" {
		return domain.Quote{}, providerErr(c.Name(), "rate_limit", "provider returned a rate-limit notice")
	}
	if payload.Error != "" {
		return domain.Quote{}, providerErr(c.Name(), "provider_error", "provider returned an error payload")
	}
	if len(payload.GlobalQuote) == 0 {
		return domain.Quote{}, providerErr(c.Name(), "provider_error", "provider returned no global quote")
	}
	var responseSymbol, responsePrice, latestDay string
	if err := rawString(payload.GlobalQuote, "01. symbol", &responseSymbol); err != nil {
		return domain.Quote{}, providerErr(c.Name(), "malformed_json", "global quote symbol is missing")
	}
	if !strings.EqualFold(strings.TrimSpace(responseSymbol), normalizedSymbol(request)) {
		return domain.Quote{}, providerErr(c.Name(), "symbol_mismatch", "provider symbol did not match explicit mapping")
	}
	if err := rawString(payload.GlobalQuote, "05. price", &responsePrice); err != nil {
		return domain.Quote{}, providerErr(c.Name(), "malformed_json", "global quote price is missing")
	}
	price, err := decimalRaw(json.RawMessage(strconv.Quote(responsePrice)), c.Name(), "global quote price")
	if err != nil {
		return domain.Quote{}, err
	}
	if err := rawString(payload.GlobalQuote, "07. latest trading day", &latestDay); err != nil {
		return domain.Quote{}, providerErr(c.Name(), "invalid_timestamp", "latest trading day is missing")
	}
	market, err := time.Parse("2006-01-02", latestDay)
	if err != nil {
		return domain.Quote{}, providerErr(c.Name(), "invalid_timestamp", "latest trading day is invalid")
	}
	marketAt, err := parseProviderTimestamp(c.Name(), market.UTC(), c.now())
	if err != nil {
		return domain.Quote{}, err
	}
	return domain.Quote{ID: quoteID(c.Name().String(), request, price.String(), marketAt), InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: price.String(), Currency: strings.ToUpper(request.Currency), Source: c.Name().String(), MarketAt: marketAt, FetchedAt: c.now().UTC().Format(time.RFC3339Nano), Basis: "end_of_day", Provenance: "Alpha Vantage GLOBAL_QUOTE endpoint; explicit provider mapping"}, nil
}

func rawString(values map[string]json.RawMessage, key string, target *string) error {
	raw, ok := values[key]
	if !ok {
		return errors.New("missing field")
	}
	return json.Unmarshal(raw, target)
}
