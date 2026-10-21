package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

const marketRefreshFreshnessPolicy = "quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero"

type marketWorkflowEndpoint struct {
	database  *store.Store
	clients   map[marketdata.Provider]marketdata.ProviderClient
	readiness []domain.ProviderReadiness
	mu        sync.Mutex
	requests  map[string]marketRefreshRequest
}

type marketRefreshRequest struct {
	fingerprint string
	result      domain.MarketRefreshResult
}

func (h *marketWorkflowEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serveHTTP(w, r)
}

func newMarketWorkflowEndpoint(database *store.Store, cfg config.Config) *marketWorkflowEndpoint {
	keys := map[marketdata.Provider]bool{
		marketdata.ProviderFinnhub:           cfg.ProviderKeys.Finnhub != "",
		marketdata.ProviderAlphaVantage:      cfg.ProviderKeys.AlphaVantage != "",
		marketdata.ProviderOpenExchangeRates: cfg.ProviderKeys.OpenExchangeRates != "",
	}
	return &marketWorkflowEndpoint{
		database: database,
		clients: map[marketdata.Provider]marketdata.ProviderClient{
			marketdata.ProviderFinnhub:           marketdata.NewFinnhubClient(cfg.ProviderKeys.Finnhub, marketdata.ClientOptions{}),
			marketdata.ProviderAlphaVantage:      marketdata.NewAlphaVantageClient(cfg.ProviderKeys.AlphaVantage, marketdata.ClientOptions{}),
			marketdata.ProviderCoinGecko:         marketdata.NewCoinGeckoClient(marketdata.ClientOptions{}),
			marketdata.ProviderOpenExchangeRates: marketdata.NewOpenExchangeRatesClient(cfg.ProviderKeys.OpenExchangeRates, marketdata.ClientOptions{}),
		},
		readiness: store.MarketProviders(keys),
		requests:  map[string]marketRefreshRequest{},
	}
}

func (h *marketWorkflowEndpoint) serveHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || parts[3] == "" || parts[4] != "market-data" || (len(parts) != 5 && len(parts) != 6) {
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
	result, err := h.database.AddProviderMappingsBatch(r.Context(), portfolioID, r.Header.Get("If-Match"), request.Mappings)
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
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "idempotency_key_required"})
		return
	}
	var request struct {
		ReportingCurrency string `json:"reporting_currency"`
	}
	body, err := readJSONBody(w, r, 8<<10)
	if err == nil && len(body) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&request)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_refresh_request"})
		return
	}
	fingerprintBytes := sha256.Sum256([]byte(strings.TrimSpace(request.ReportingCurrency)))
	fingerprint := hex.EncodeToString(fingerprintBytes[:])
	cacheKey := portfolioID + "\x00" + key
	h.mu.Lock()
	if cached, ok := h.requests[cacheKey]; ok {
		if cached.fingerprint != fingerprint {
			h.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency_key_conflict"})
			return
		}
		h.mu.Unlock()
		writeJSON(w, http.StatusOK, cached.result)
		return
	}
	// Hold the lock over the bounded operation so a double-submit cannot issue
	// a second provider request before the first response is cached.
	defer h.mu.Unlock()
	result, err := h.database.Refresh(r.Context(), store.RefreshOptions{PortfolioID: portfolioID, ReportingCurrency: request.ReportingCurrency, Clients: h.clients})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "refresh_failed"})
		return
	}
	snapshot, err := h.database.CreateSnapshot(r.Context(), store.SnapshotRequest{PortfolioID: portfolioID, ReportingCurrency: request.ReportingCurrency, AsOf: time.Now().UTC(), MaxAge: domain.DefaultFreshness})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "snapshot_failed"})
		return
	}
	setup, _ := h.database.MarketDataSetup(r.Context(), portfolioID, h.readiness)
	missing := make([]string, 0)
	for _, instrument := range setup.Instruments {
		if len(instrument.Mappings) == 0 {
			missing = append(missing, fmt.Sprintf("%s (%s)", instrument.Symbol, instrument.Currency))
		}
	}
	response := domain.MarketRefreshResult{Refresh: result, Snapshot: snapshot, Complete: result.Status == "complete" && snapshot.Complete, Freshness: marketRefreshFreshnessPolicy, Missing: missing}
	h.requests[cacheKey] = marketRefreshRequest{fingerprint: fingerprint, result: response}
	writeJSON(w, http.StatusOK, response)
}

func ioReadAll(reader io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, limit))
}
