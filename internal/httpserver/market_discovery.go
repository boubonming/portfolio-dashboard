package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

const marketDiscoveryTotalTimeout = 10 * time.Second

func (h *marketWorkflowEndpoint) discover(w http.ResponseWriter, r *http.Request, portfolioID string) {
	body, err := readJSONBody(w, r, 8<<10)
	if err == nil && hasDuplicateJSONKeys(body) {
		err = errors.New("duplicate JSON key")
	}
	if err == nil {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			err = errors.New("discovery request must be an object")
		}
		var request struct{}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err == nil {
			err = decoder.Decode(&request)
		}
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing JSON")
			}
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_discovery_request"})
		return
	}
	setup, err := h.database.MarketDataSetup(r.Context(), portfolioID, h.readiness)
	if err != nil {
		if errors.Is(err, store.ErrPortfolioNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
		}
		return
	}
	if expected := r.Header.Get("If-Match"); expected == "" || expected != setup.ETag {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "stale_mapping_setup"})
		return
	}

	discoveryContext, cancel := context.WithTimeout(r.Context(), marketDiscoveryTotalTimeout)
	defer cancel()
	response := domain.MarketDiscoveryResponse{PortfolioID: portfolioID, ETag: setup.ETag, Results: make([]domain.MarketDiscoveryResult, 0, len(setup.Instruments))}
	cache := make(map[string]discoveryLookup)
	for _, instrument := range setup.Instruments {
		result := domain.MarketDiscoveryResult{InstrumentID: instrument.InstrumentID, AttemptedProviders: []string{}, Candidates: []domain.MarketDiscoveryCandidate{}}
		if len(instrument.Mappings) != 0 {
			result.Status = "committed"
			result.Diagnostic = "A committed mapping already exists; discovery was not used."
			response.Results = append(response.Results, result)
			continue
		}
		providers := discoveryProviders(instrument.Currency)
		for _, provider := range providers {
			result.AttemptedProviders = append(result.AttemptedProviders, provider.String())
			if discoveryContext.Err() != nil {
				if result.Diagnostic == "" {
					result.Diagnostic = discoveryDiagnostic(provider, contextDeadlineError{})
				}
				continue
			}
			key := provider.String() + "\x00" + strings.ToLower(strings.TrimSpace(instrument.Symbol))
			lookup, ok := cache[key]
			if !ok {
				lookup = h.lookupDiscovery(discoveryContext, provider, instrument.Symbol)
				cache[key] = lookup
			}
			if lookup.err == nil {
				for _, candidate := range lookup.candidates {
					result.Candidates = append(result.Candidates, discoveryCandidateView(provider, candidate))
				}
				if len(lookup.candidates) > 0 && result.Provider == "" {
					result.Provider = provider.String()
				}
			} else if result.Diagnostic == "" {
				result.Diagnostic = discoveryDiagnostic(provider, lookup.err)
			}
		}
		if len(result.Candidates) > 0 {
			result.Status = "candidates"
			if len(result.Candidates) > 1 {
				result.Diagnostic = "Multiple provider candidates were returned; choose and verify one explicitly."
			} else {
				result.Diagnostic = "Provider candidates are unverified until explicitly confirmed."
			}
		} else if result.Diagnostic == "" {
			result.Status = "no_result"
			result.Diagnostic = "Configured providers returned no candidate for this symbol. Enter and verify the exact provider identifier manually."
		} else if strings.Contains(result.Diagnostic, "not configured") || strings.Contains(result.Diagnostic, "unavailable") {
			result.Status = "unavailable"
		} else {
			result.Status = "error"
		}
		response.Results = append(response.Results, result)
	}
	writeJSON(w, http.StatusOK, response)
}

type discoveryLookup struct {
	candidates []marketdata.DiscoveryCandidate
	err        error
}

func discoveryProviders(currency string) []marketdata.Provider {
	providers := []marketdata.Provider{marketdata.ProviderCoinGecko}
	if strings.EqualFold(strings.TrimSpace(currency), "USD") {
		return append(providers, marketdata.ProviderFinnhub)
	}
	return append(providers, marketdata.ProviderAlphaVantage)
}

func (h *marketWorkflowEndpoint) lookupDiscovery(ctx context.Context, provider marketdata.Provider, symbol string) discoveryLookup {
	if ctx.Err() != nil {
		return discoveryLookup{err: contextDeadlineError{}}
	}
	client, ok := h.clients[provider].(marketdata.DiscoveryClient)
	if !ok || client == nil {
		return discoveryLookup{err: fmt.Errorf("%s provider is not configured or unavailable", provider)}
	}
	candidates, err := client.Search(ctx, strings.TrimSpace(symbol))
	if err != nil {
		if ctx.Err() != nil {
			return discoveryLookup{err: contextDeadlineError{}}
		}
		return discoveryLookup{err: err}
	}
	return discoveryLookup{candidates: candidates}
}

type contextDeadlineError struct{}

func (contextDeadlineError) Error() string { return "discovery deadline exceeded" }

func discoveryCandidateView(provider marketdata.Provider, candidate marketdata.DiscoveryCandidate) domain.MarketDiscoveryCandidate {
	return domain.MarketDiscoveryCandidate{Provider: provider.String(), Identifier: candidate.Identifier, Symbol: candidate.Symbol, Name: candidate.Name, Description: candidate.Description, Type: candidate.Type, Region: candidate.Region, Exchange: candidate.Exchange, Currency: candidate.Currency}
}

func discoveryDiagnostic(provider marketdata.Provider, err error) string {
	if _, ok := err.(contextDeadlineError); ok {
		return fmt.Sprintf("%s search timed out; no suggestion was selected. Enter the identifier manually.", provider)
	}
	var providerError *marketdata.ProviderError
	if errors.As(err, &providerError) {
		switch providerError.Classification {
		case "configuration":
			return fmt.Sprintf("%s is not configured; enter and verify the exact provider identifier manually.", provider)
		case "rate_limit":
			return fmt.Sprintf("%s rate limit reached; no suggestion was selected. Try again later or enter the identifier manually.", provider)
		case "timeout":
			return fmt.Sprintf("%s search timed out; no suggestion was selected. Enter the identifier manually.", provider)
		case "auth", "transport", "provider_error", "malformed_json", "content_type":
			return fmt.Sprintf("%s search is unavailable (%s); no suggestion was selected.", provider, providerError.Classification)
		}
	}
	return fmt.Sprintf("%s search is unavailable; no suggestion was selected.", provider)
}
