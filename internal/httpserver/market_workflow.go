package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/marketrefresh"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

type marketWorkflowEndpoint struct {
	database  *store.Store
	clients   map[marketdata.Provider]marketdata.ProviderClient
	readiness []domain.ProviderReadiness
	auth      *auth.Authenticator
	workflow  *marketrefresh.Service
}

func (h *marketWorkflowEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serveHTTP(w, r)
}

func newMarketWorkflowEndpoint(database *store.Store, cfg config.Config, authenticator *auth.Authenticator, injected map[marketdata.Provider]marketdata.ProviderClient) *marketWorkflowEndpoint {
	keys := map[marketdata.Provider]bool{
		marketdata.ProviderFinnhub:           cfg.ProviderKeys.Finnhub != "",
		marketdata.ProviderAlphaVantage:      cfg.ProviderKeys.AlphaVantage != "",
		marketdata.ProviderOpenExchangeRates: cfg.ProviderKeys.OpenExchangeRates != "",
	}
	clients := injected
	if clients == nil {
		clients = map[marketdata.Provider]marketdata.ProviderClient{
			marketdata.ProviderFinnhub:           marketdata.NewFinnhubClient(cfg.ProviderKeys.Finnhub, marketdata.ClientOptions{BaseURL: cfg.ProviderURLs.Finnhub}),
			marketdata.ProviderAlphaVantage:      marketdata.NewAlphaVantageClient(cfg.ProviderKeys.AlphaVantage, marketdata.ClientOptions{BaseURL: cfg.ProviderURLs.AlphaVantage}),
			marketdata.ProviderCoinGecko:         marketdata.NewCoinGeckoClient(marketdata.ClientOptions{BaseURL: cfg.ProviderURLs.CoinGecko}),
			marketdata.ProviderOpenExchangeRates: marketdata.NewOpenExchangeRatesClient(cfg.ProviderKeys.OpenExchangeRates, marketdata.ClientOptions{BaseURL: cfg.ProviderURLs.OpenExchangeRates}),
		}
	}
	readiness := store.MarketProviders(keys)
	return &marketWorkflowEndpoint{
		database:  database,
		clients:   clients,
		readiness: readiness,
		auth:      authenticator,
		workflow:  marketrefresh.New(database, clients, readiness),
	}
}

func (h *marketWorkflowEndpoint) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 5 || len(parts) > 6 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || parts[3] == "" || len(parts[3]) > 128 || !canonicalPathID.MatchString(parts[3]) || strings.Contains(r.URL.EscapedPath(), "%") || parts[4] != "market-data" || (len(parts) != 5 && len(parts) != 6) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	portfolioID := parts[3]
	if len(parts) == 5 {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		h.setup(w, r, portfolioID)
		return
	}
	switch parts[5] {
	case "discover":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		h.discover(w, r, portfolioID)
	case "mappings":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		h.saveMappings(w, r, portfolioID)
	case "refresh":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		h.refresh(w, r, portfolioID)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func (h *marketWorkflowEndpoint) setup(w http.ResponseWriter, r *http.Request, portfolioID string) {
	setup, err := h.database.MarketDataSetup(r.Context(), portfolioID, h.readiness)
	if err != nil {
		if errors.Is(err, store.ErrPortfolioNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
		}
		return
	}
	w.Header().Set("ETag", setup.ETag)
	writeJSON(w, http.StatusOK, setup)
}

func (h *marketWorkflowEndpoint) saveMappings(w http.ResponseWriter, r *http.Request, portfolioID string) {
	var request struct {
		Mappings []domain.MappingBatchItem `json:"mappings"`
	}
	body, err := readJSONBody(w, r, 512<<10)
	if err == nil {
		if hasDuplicateJSONKeys(body) {
			err = errors.New("duplicate JSON key")
		}
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&request)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing JSON")
			}
		}
	}
	if err != nil || len(request.Mappings) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mapping_batch"})
		return
	}
	requestAuth, ok := marketWorkflowAuthFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var result domain.MappingBatchResult
	if requestAuth.actor.Category == "machine" {
		result, err = h.database.AddProviderMappingsBatchAs(r.Context(), portfolioID, r.Header.Get("If-Match"), request.Mappings, requestAuth.actor)
	} else {
		result, err = h.database.AddProviderMappingsBatch(r.Context(), portfolioID, r.Header.Get("If-Match"), request.Mappings)
	}
	if err != nil {
		if strings.Contains(err.Error(), "stale") {
			writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "stale_mapping_setup"})
		} else if errors.Is(err, store.ErrPortfolioNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mapping_batch"})
		}
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusOK, result)
}

func readJSONBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	if !strings.EqualFold(strings.Split(r.Header.Get("Content-Type"), ";")[0], "application/json") {
		return nil, errors.New("invalid content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	body, err := ioReadAll(r.Body, max+1)
	if err != nil || int64(len(body)) > max {
		return nil, errors.New("invalid body")
	}
	return body, nil
}

func (h *marketWorkflowEndpoint) refresh(w http.ResponseWriter, r *http.Request, portfolioID string) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 || !canonicalPathID.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "idempotency_key_required"})
		return
	}
	var request struct {
		ReportingCurrency string `json:"reporting_currency"`
	}
	var decoded *struct {
		ReportingCurrency string `json:"reporting_currency"`
	}
	body, err := readJSONBody(w, r, 8<<10)
	if err == nil {
		if hasDuplicateJSONKeys(body) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_refresh_request"})
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&decoded)
		if err == nil && decoded != nil {
			request = *decoded
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing JSON")
			}
		} else if err == nil {
			err = errors.New("refresh request must be an object")
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_refresh_request"})
		return
	}
	requestAuth, actorOK := marketWorkflowAuthFromContext(r.Context())
	if !actorOK {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	response, _, err := h.workflow.Refresh(r.Context(), marketrefresh.Request{
		PortfolioID:       portfolioID,
		ReportingCurrency: request.ReportingCurrency,
		IdempotencyKey:    key,
		Actor:             requestAuth.actor,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPortfolioNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
		case strings.Contains(err.Error(), "invalid reporting currency"):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_reporting_currency"})
		case errors.Is(err, store.ErrMarketRefreshIdempotencyConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency_key_conflict"})
		case errors.Is(err, store.ErrMarketRefreshInProgress):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "refresh_in_progress"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "refresh_failed"})
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func ioReadAll(reader io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, limit))
}
