package httpserver

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

var supportedExportCurrencies = map[string]struct{}{
	"MYR": {},
	"USD": {},
	"SGD": {},
	"JPY": {},
}

var exportHeaders = []string{
	"portfolio_id", "portfolio_name", "reporting_currency",
	"snapshot_state", "snapshot_id", "calculation_version", "snapshot_as_of", "snapshot_created_at", "snapshot_complete",
	"lot_id", "symbol", "name", "description", "quantity", "source_currency", "unit_cost", "cost_basis",
	"latest_price", "latest_value", "reporting_value", "quote_source", "market_timestamp", "freshness_state", "freshness_age",
	"broker", "acquisition_date", "missing_broker", "missing_acquisition_date", "data_quality_flags",
}

func portfolioExport(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		canonicalPath := ""
		if len(parts) == 5 {
			canonicalPath = "/api/v1/portfolios/" + parts[3] + "/export.csv"
		}
		validPath := len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[3] != "" && len(parts[3]) <= 128 && parts[4] == "export.csv" && r.URL.Path == canonicalPath && r.URL.EscapedPath() == canonicalPath
		if !validPath {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if database == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		currency, err := parseExportCurrency(r.URL.RawQuery)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_export_query"})
			return
		}
		view, err := database.PortfolioOverview(r.Context(), parts[3], currency)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrPortfolioNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
			case errors.Is(err, store.ErrInvalidReportingCurrency):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_reporting_currency"})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			}
			return
		}
		body, err := renderPortfolioCSV(r.Context(), view)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"portfolio-export-"+currency+".csv\"")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func parseExportCurrency(rawQuery string) (string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query) != 1 {
		return "", store.ErrInvalidReportingCurrency
	}
	values, ok := query["currency"]
	if !ok || len(values) != 1 || values[0] == "" || values[0] != strings.ToUpper(values[0]) || rawQuery != "currency="+values[0] {
		return "", store.ErrInvalidReportingCurrency
	}
	if _, ok := supportedExportCurrencies[values[0]]; !ok {
		return "", store.ErrInvalidReportingCurrency
	}
	return values[0], nil
}

func renderPortfolioCSV(ctx context.Context, view domain.PortfolioOverview) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	writer.UseCRLF = true
	if err := writer.Write(exportHeaders); err != nil {
		return nil, err
	}
	for _, holding := range view.Holdings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := exportHoldingRow(view, holding)
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func exportHoldingRow(view domain.PortfolioOverview, holding domain.HoldingView) []string {
	return []string{
		exportText(view.Portfolio.ID), exportText(view.Portfolio.Name), exportText(view.ReportingCurrency),
		exportText(view.Snapshot.State), exportText(view.Snapshot.SnapshotID), exportText(view.Snapshot.CalculationVersion),
		exportTimestamp(view.Snapshot.AsOf), exportTimestamp(view.Snapshot.CreatedAt), fmt.Sprintf("%t", view.Snapshot.Complete),
		exportText(holding.LotID), exportText(holding.Symbol), exportText(holding.Name), exportText(holding.Description),
		holding.Quantity, exportText(holding.Currency), optionalDecimal(holding.UnitCost), optionalDecimal(holding.CostBasis),
		optionalDecimal(holding.LatestPrice), optionalDecimal(holding.LatestValue), optionalDecimal(holding.ReportingValue),
		exportText(holding.QuoteSource), exportTimestamp(holding.Freshness.MarketAt), exportText(holding.Freshness.Status), exportText(holding.Freshness.Age),
		optionalText(holding.Broker), optionalDate(holding.AcquisitionDate),
		fmt.Sprintf("%t", holding.MissingBroker), fmt.Sprintf("%t", holding.MissingAcquisitionDate), exportText(strings.Join(holding.DataQuality, "|")),
	}
}

func optionalDecimal(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return exportText(*value)
}

// exportText is the one policy for arbitrary textual cells. A leading
// apostrophe makes spreadsheet applications treat dangerous values as text;
// encoding/csv still owns all RFC 4180 quoting and escaping.
func exportText(value string) string {
	if value == "" {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(value)
	// Unicode format controls such as BOM and zero-width space are treated as
	// dangerous prefixes too: preserving them before a formula marker could
	// hide the marker from this text policy while leaving spreadsheet parsing
	// behavior implementation-dependent.
	if strings.ContainsRune("=+-@", first) || unicode.IsSpace(first) || unicode.IsControl(first) || unicode.Is(unicode.Cf, first) {
		return "'" + value
	}
	return value
}

func exportTimestamp(value string) string {
	if value == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && parsed.Format(time.RFC3339Nano) == value {
		return value
	}
	return exportText(value)
}

func optionalDate(value *string) string {
	if value == nil {
		return ""
	}
	if parsed, err := time.Parse("2006-01-02", *value); err == nil && parsed.Format("2006-01-02") == *value {
		return *value
	}
	return exportText(*value)
}
