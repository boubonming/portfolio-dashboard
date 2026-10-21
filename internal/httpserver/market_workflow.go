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

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

type marketWorkflowEndpoint struct {
	database  *store.Store
	clients   map[marketdata.Provider]marketdata.ProviderClient
	readiness []domain.ProviderReadiness
	auth      *auth.Authenticator
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
	return &marketWorkflowEndpoint{
		database:  database,
		clients:   clients,
		readiness: store.MarketProviders(keys),
		auth:      authenticator,
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
	if len(key) < 8 || len(key) > 128 || !canonicalPathID.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "idempotency_key_required"})
		return
	}
	var request struct {
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
		err = decoder.Decode(&request)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing JSON")
			}
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_refresh_request"})
		return
	}
	setup, setupErr := h.database.MarketDataSetup(r.Context(), portfolioID, h.readiness)
	if setupErr != nil {
		if errors.Is(setupErr, store.ErrPortfolioNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
		}
		return
	}
	reportingCurrency := strings.TrimSpace(request.ReportingCurrency)
	if reportingCurrency == "" {
		if err := h.database.DB.QueryRowContext(r.Context(), "SELECT reporting_currency FROM portfolios WHERE id=?", portfolioID).Scan(&reportingCurrency); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			return
		}
		reportingCurrency = strings.TrimSpace(reportingCurrency)
	}
	if len(reportingCurrency) != 3 || reportingCurrency != strings.ToUpper(reportingCurrency) || strings.Trim(reportingCurrency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_reporting_currency"})
		return
	}
	fingerprintInput, _ := json.Marshal(struct {
		PortfolioID       string `json:"portfolio_id"`
		Operation         string `json:"operation"`
		ReportingCurrency string `json:"reporting_currency"`
	}{portfolioID, "refresh", reportingCurrency})
	fingerprintBytes := sha256.Sum256(fingerprintInput)
	fingerprint := hex.EncodeToString(fingerprintBytes[:])
	missing := make([]string, 0)
	for _, instrument := range setup.Instruments {
		if len(instrument.Mappings) == 0 {
			missing = append(missing, fmt.Sprintf("%s (%s)", instrument.Symbol, instrument.Currency))
		}
	}
	actorID, actorOK := h.auth.BrowserActorID(r)
	if !actorOK {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	response, _, err := h.database.RefreshMarketDataIdempotent(r.Context(), store.MarketRefreshIdempotencyOptions{
		ActorCategory: "browser_user", ActorID: actorID, PortfolioID: portfolioID, IdempotencyKey: key, RequestFingerprint: fingerprint,
		Refresh:  store.RefreshOptions{PortfolioID: portfolioID, ReportingCurrency: reportingCurrency, Clients: h.clients},
		Snapshot: store.SnapshotRequest{PortfolioID: portfolioID, ReportingCurrency: reportingCurrency, MaxAge: domain.DefaultFreshness}, Missing: missing,
	})
	if err != nil {
		switch {
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
