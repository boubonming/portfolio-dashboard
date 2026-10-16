package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func reviewList(database *store.Store, w http.ResponseWriter, r *http.Request, portfolioID string) {
	if database == nil {
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		return
	}
	filter, err := parseReviewListQuery(r.URL.RawQuery)
	if err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	page, err := database.ListAdvisoryReviews(r.Context(), portfolioID, filter)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrReviewCursor):
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		case errors.Is(err, store.ErrPortfolioNotFound), errors.Is(err, store.ErrReviewNotFound):
			writePhaseDError(w, http.StatusNotFound, "not_found", "The requested portfolio was not found.")
		case errors.Is(err, store.ErrReviewCorrupt):
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		default:
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		}
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func reviewDetail(database *store.Store, w http.ResponseWriter, r *http.Request, portfolioID, reviewID string) {
	if database == nil {
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		return
	}
	result, err := database.GetAdvisoryReview(r.Context(), portfolioID, reviewID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrReviewNotFound):
			writePhaseDError(w, http.StatusNotFound, "not_found", "The requested review was not found.")
		case errors.Is(err, store.ErrReviewCorrupt):
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		default:
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parseReviewListQuery(raw string) (store.ReviewListFilter, error) {
	filter := store.ReviewListFilter{Limit: 20}
	if raw == "" {
		return filter, nil
	}
	seen := map[string]bool{}
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			return store.ReviewListFilter{}, errors.New("empty query component")
		}
		keyRaw, valueRaw, ok := strings.Cut(pair, "=")
		if !ok || keyRaw == "" || valueRaw == "" {
			return store.ReviewListFilter{}, errors.New("invalid query component")
		}
		key, err := url.QueryUnescape(keyRaw)
		if err != nil {
			return store.ReviewListFilter{}, err
		}
		if keyRaw != key {
			return store.ReviewListFilter{}, errors.New("non-canonical query key")
		}
		value, err := url.QueryUnescape(valueRaw)
		if err != nil {
			return store.ReviewListFilter{}, err
		}
		if url.QueryEscape(value) != valueRaw {
			return store.ReviewListFilter{}, errors.New("non-canonical query value")
		}
		if seen[key] {
			return store.ReviewListFilter{}, errors.New("duplicate query key")
		}
		seen[key] = true
		switch key {
		case "limit":
			limit, parseErr := strconv.Atoi(value)
			if parseErr != nil || strconv.Itoa(limit) != value || limit < 1 || limit > 50 {
				return store.ReviewListFilter{}, errors.New("invalid limit")
			}
			filter.Limit = limit
		case "cursor":
			if value == "" {
				return store.ReviewListFilter{}, errors.New("empty cursor")
			}
			filter.Cursor = value
		case "holding_id":
			if !validReviewPathID(value) {
				return store.ReviewListFilter{}, errors.New("invalid holding")
			}
			filter.HoldingID, filter.HasHolding = value, true
		case "status":
			if value != "complete" && value != "incomplete" {
				return store.ReviewListFilter{}, errors.New("invalid status")
			}
			filter.Status, filter.HasStatus = value, true
		case "outdated":
			if value != "true" && value != "false" {
				return store.ReviewListFilter{}, errors.New("invalid outdated")
			}
			filter.Outdated, filter.HasOutdated = value == "true", true
		default:
			return store.ReviewListFilter{}, errors.New("unknown query key")
		}
	}
	return filter, nil
}
