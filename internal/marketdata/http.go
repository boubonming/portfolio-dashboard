package marketdata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

const maxProviderBody = 1 << 20

var numericJSON = regexp.MustCompile(`^[+-]?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
var secretQuery = regexp.MustCompile(`(?i)(token|apikey|app_id)=[^&\s]+`)

type ProviderError struct {
	Provider       Provider
	Classification string
	Message        string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s provider error (%s)", e.Provider, e.Classification)
	}
	return fmt.Sprintf("%s provider error (%s): %s", e.Provider, e.Classification, e.Message)
}

func providerErr(provider Provider, classification, message string) error {
	return &ProviderError{Provider: provider, Classification: classification, Message: safeMessage(message)}
}

func safeMessage(message string) string {
	return secretQuery.ReplaceAllString(message, "$1=[REDACTED]")
}

func optionsOrDefault(options ClientOptions, defaultURL string) (string, HTTPDoer, func() time.Time) {
	base := strings.TrimRight(options.BaseURL, "/")
	if base == "" {
		base = defaultURL
	}
	client := options.HTTPClient
	if client == nil {
		timeout := options.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		client = &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return base, client, now
}

func requestJSON(ctx context.Context, client HTTPDoer, provider Provider, request *http.Request) ([]byte, error) {
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, providerErr(provider, "timeout", "request timed out")
		}
		return nil, providerErr(provider, "transport", err.Error())
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxProviderBody+1))
	if readErr != nil {
		return nil, providerErr(provider, "read", "response could not be read")
	}
	if len(body) > maxProviderBody {
		return nil, providerErr(provider, "body_too_large", "response exceeded bounded body limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		classification := "http_status"
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			classification = "auth"
		} else if response.StatusCode == http.StatusTooManyRequests {
			classification = "rate_limit"
		}
		return nil, providerErr(provider, classification, fmt.Sprintf("HTTP status %d", response.StatusCode))
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/json") && !strings.Contains(contentType, "+json") {
		return nil, providerErr(provider, "content_type", "provider did not return JSON")
	}
	if !json.Valid(body) {
		return nil, providerErr(provider, "malformed_json", "provider returned malformed JSON")
	}
	return body, nil
}

func decimalRaw(raw json.RawMessage, provider Provider, field string) (domain.Decimal, error) {
	value := strings.TrimSpace(string(bytes.TrimSpace(raw)))
	if len(value) >= 2 && value[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return domain.Decimal{}, providerErr(provider, "invalid_value", field+" is not a decimal")
		}
		value = strings.TrimSpace(text)
	}
	if !numericJSON.MatchString(value) {
		return domain.Decimal{}, providerErr(provider, "invalid_value", field+" is not a finite decimal")
	}
	parsed, err := domain.ParseDecimal(value)
	if err != nil || parsed.Sign() <= 0 {
		return domain.Decimal{}, providerErr(provider, "invalid_value", field+" must be positive")
	}
	return parsed, nil
}

func normalizedSymbol(request QuoteRequest) string {
	if strings.TrimSpace(request.ProviderSymbol) != "" {
		return strings.TrimSpace(request.ProviderSymbol)
	}
	return strings.TrimSpace(request.Symbol)
}

func validateQuoteRequest(request QuoteRequest) error {
	if strings.TrimSpace(request.InstrumentID) == "" || normalizedSymbol(request) == "" || strings.TrimSpace(request.Currency) == "" {
		return errors.New("instrument, explicit provider symbol, and currency are required")
	}
	return nil
}

func quoteID(source string, request QuoteRequest, price, marketAt string) string {
	return "provider-quote-" + stableDigest(source, request.InstrumentID, normalizedSymbol(request), request.Currency, price, marketAt)
}

func fxID(source string, request FXRequest, rate, marketAt string) string {
	return "provider-fx-" + stableDigest(source, request.BaseCurrency, request.QuoteCurrency, rate, marketAt)
}

func stableDigest(parts ...string) string {
	// Deterministic IDs are kept in a helper to make adapters idempotent across
	// fetches. It intentionally contains no credentials.
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part)
		b.WriteByte(0)
	}
	return fmt.Sprintf("%x", sha256Bytes([]byte(b.String())))
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func buildURL(base, path string, query url.Values) string {
	base = strings.TrimRight(base, "/")
	parsed, err := url.Parse(base + path)
	if err != nil {
		return base + path
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func parseProviderTimestamp(provider Provider, timestamp time.Time, now time.Time) (string, error) {
	if timestamp.IsZero() || timestamp.After(now.Add(5*time.Minute)) {
		return "", providerErr(provider, "invalid_timestamp", "provider timestamp is missing or in the future")
	}
	return timestamp.UTC().Format(time.RFC3339Nano), nil
}

func decodeObject(body []byte, provider Provider, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return providerErr(provider, "malformed_json", "provider JSON schema was invalid")
	}
	return nil
}
