package marketdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const maxDiscoveryCandidates = 8

func validateSearchQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 128 || !utf8.ValidString(query) {
		return "", errors.New("search query is invalid")
	}
	for _, r := range query {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("search query contains control characters")
		}
	}
	return query, nil
}

func searchRequest(ctx context.Context, client HTTPDoer, provider Provider, endpoint string, query url.Values, target any) error {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL(endpoint, "", query), nil)
	if err != nil {
		return providerErr(provider, "request", "could not construct provider request")
	}
	body, err := requestJSON(ctx, client, provider, httpRequest)
	if err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return providerErr(provider, "malformed_json", "provider search response was not an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return providerErr(provider, "malformed_json", "provider search response was malformed")
	}
	var extra any
	if trailingErr := decoder.Decode(&extra); trailingErr != io.EOF {
		return providerErr(provider, "malformed_json", "provider search response had trailing data")
	}
	return nil
}

func boundedText(value, field string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", errors.New(field + " is missing")
	}
	if len(value) > 256 || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", errors.New(field + " is invalid")
	}
	return value, nil
}

func appendCandidate(candidates *[]DiscoveryCandidate, candidate DiscoveryCandidate) error {
	var err error
	candidate.Identifier, err = boundedText(candidate.Identifier, "identifier", true)
	if err != nil {
		return err
	}
	for _, value := range []*string{&candidate.Symbol, &candidate.Name, &candidate.Description, &candidate.Type, &candidate.Region, &candidate.Exchange, &candidate.Currency} {
		*value, err = boundedText(*value, "candidate field", false)
		if err != nil {
			return err
		}
	}
	for _, existing := range *candidates {
		if strings.EqualFold(existing.Identifier, candidate.Identifier) {
			return nil
		}
	}
	if len(*candidates) < maxDiscoveryCandidates {
		*candidates = append(*candidates, candidate)
	}
	return nil
}

// Search is implemented by each provider adapter next to its quote boundary.
// The shared helpers above keep request size, response size, and redaction
// behavior identical across providers.
