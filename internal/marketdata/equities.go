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

func (c *FinnhubClient) Search(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	query, err := validateSearchQuery(query)
	if err != nil {
		return nil, providerErr(c.Name(), "invalid_request", "search query is invalid")
	}
	if c.apiKey == "" {
		return nil, providerErr(c.Name(), "configuration", "FINNHUB_API_KEY is not configured")
	}
	var payload struct {
		Result []struct {
			Description   string `json:"description"`
			DisplaySymbol string `json:"displaySymbol"`
			Symbol        string `json:"symbol"`
			Type          string `json:"type"`
		} `json:"result"`
	}
	if err := searchRequest(ctx, c.http, c.Name(), c.baseURL+"/search", url.Values{"symbol": {query}, "token": {c.apiKey}}, &payload); err != nil {
		return nil, err
	}
	if len(payload.Result) > maxDiscoveryCandidates {
		return nil, providerErr(c.Name(), "malformed_json", "provider returned too many search candidates")
	}
	candidates := make([]DiscoveryCandidate, 0, maxDiscoveryCandidates)
	for _, item := range payload.Result {
		identifier := item.Symbol
		if strings.TrimSpace(identifier) == "" {
			identifier = item.DisplaySymbol
		}
		if err := appendCandidate(&candidates, DiscoveryCandidate{Identifier: identifier, Symbol: item.DisplaySymbol, Name: item.Description, Description: item.Description, Type: item.Type}); err != nil {
			return nil, providerErr(c.Name(), "malformed_json", "provider search candidate was malformed")
		}
	}
	return candidates, nil
}

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
	if err := json.Unmarshal(payload.Epoch, &epoch); err != nil || epoch < 0 {
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

func (c *AlphaVantageClient) Search(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	query, err := validateSearchQuery(query)
	if err != nil {
		return nil, providerErr(c.Name(), "invalid_request", "search query is invalid")
	}
	if c.apiKey == "" {
		return nil, providerErr(c.Name(), "configuration", "ALPHA_VANTAGE_API_KEY is not configured")
	}
	var payload struct {
		Matches []struct {
			Symbol      string `json:"1. symbol"`
			Name        string `json:"2. name"`
			Type        string `json:"3. type"`
			Region      string `json:"4. region"`
			MarketOpen  string `json:"5. marketOpen"`
			MarketClose string `json:"6. marketClose"`
			Timezone    string `json:"7. timezone"`
			Currency    string `json:"8. currency"`
			MatchScore  string `json:"9. matchScore"`
		} `json:"bestMatches"`
		Note        string `json:"Note"`
		Information string `json:"Information"`
		Error       string `json:"Error Message"`
	}
	if err := searchRequest(ctx, c.http, c.Name(), c.baseURL, url.Values{"function": {"SYMBOL_SEARCH"}, "keywords": {query}, "apikey": {c.apiKey}}, &payload); err != nil {
		return nil, err
	}
	if payload.Note != "" || payload.Information != "" {
		return nil, providerErr(c.Name(), "rate_limit", "provider returned a rate-limit notice")
	}
	if payload.Error != "" {
		return nil, providerErr(c.Name(), "provider_error", "provider returned an error payload")
	}
	if len(payload.Matches) > maxDiscoveryCandidates {
		return nil, providerErr(c.Name(), "malformed_json", "provider returned too many search candidates")
	}
	candidates := make([]DiscoveryCandidate, 0, maxDiscoveryCandidates)
	for _, item := range payload.Matches {
		if err := appendCandidate(&candidates, DiscoveryCandidate{Identifier: item.Symbol, Symbol: item.Symbol, Name: item.Name, Description: item.Name, Type: item.Type, Region: item.Region, Currency: item.Currency}); err != nil {
			return nil, providerErr(c.Name(), "malformed_json", "provider search candidate was malformed")
		}
	}
	return candidates, nil
}

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
