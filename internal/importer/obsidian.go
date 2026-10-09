package importer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var moneyPattern = regexp.MustCompile(`(?i)\b(USD|SGD|MYR|JPY)\s+([0-9][0-9,]*(?:\.[0-9]+)?)`)
var datePattern = regexp.MustCompile(`\b(20[0-9]{2}-[0-9]{2}-[0-9]{2})\b`)
var decimalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

// PreviewFile reads a markdown source and never opens or writes a database.
func PreviewFile(filename string) (domain.Preview, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return domain.Preview{}, fmt.Errorf("read source: %w", err)
	}
	preview, err := PreviewMarkdown(string(data), filename)
	if err != nil {
		return domain.Preview{}, err
	}
	digest := sha256.Sum256(data)
	preview.SourceSHA256 = hex.EncodeToString(digest[:])
	return preview, nil
}

func PreviewMarkdown(markdown, source string) (domain.Preview, error) {
	preview := domain.Preview{Version: "phase-a.v1", Source: source, Lots: []domain.Lot{}, Unsupported: []domain.UnsupportedRow{}}
	scanner := bufio.NewScanner(strings.NewReader(markdown))
	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return preview, err
	}
	seenCurrencies := map[string]bool{}
	seenSymbols := map[string]bool{}
	anomalySet := map[string]bool{}
	currency := ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "### ") && strings.HasSuffix(line, "holdings") {
			currency = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "### ")), ""), " holdings")
			if strings.HasSuffix(currency, "-denominated") {
				currency = strings.TrimSuffix(currency, "-denominated")
			}
			currency = strings.TrimSpace(currency)
			if currency != "" && currency != "Notes" {
				seenCurrencies[currency] = true
			}
			continue
		}
		if !strings.HasPrefix(line, "|") || i+1 >= len(lines) || !isSeparator(lines[i+1]) {
			continue
		}
		headers := tableCells(line)
		preview.Summary.SourceTableRows += countTableRows(lines, i+2)
		i++
		for i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if !strings.HasPrefix(next, "|") {
				break
			}
			i++
			cells := tableCells(next)
			if len(cells) == 0 {
				continue
			}
			for len(cells) < len(headers) {
				cells = append(cells, "")
			}
			if len(headers) == 0 {
				continue
			}
			row := parseRow(headers, cells, currency, i+1, next)
			if row == nil {
				preview.Unsupported = append(preview.Unsupported, domain.UnsupportedRow{SourceRow: i + 1, SourceText: next, Reason: "row has no source symbol"})
				anomalySet["unsupported row"] = true
				continue
			}
			if row.Classification == "aggregate" {
				preview.Summary.AggregateRows++
				row.Included = false
			} else {
				preview.Summary.HoldingRows++
				row.Included = true
				preview.Summary.LotsIncluded++
				seenSymbols[row.Symbol] = true
			}
			for _, issue := range row.Issues {
				anomalySet[issue] = true
			}
			preview.Lots = append(preview.Lots, *row)
		}
	}
	for c := range seenCurrencies {
		preview.CurrencyHeaders = append(preview.CurrencyHeaders, c)
	}
	sort.Strings(preview.CurrencyHeaders)
	preview.Summary.DistinctSymbols = len(seenSymbols)
	for anomaly := range anomalySet {
		preview.Summary.Anomalies = append(preview.Summary.Anomalies, anomaly)
	}
	sort.Strings(preview.Summary.Anomalies)
	preview.Summary.AnomalyCount = len(preview.Summary.Anomalies)
	return preview, nil
}

func isSeparator(line string) bool {
	for _, cell := range tableCells(line) {
		if !strings.Contains(cell, "-") {
			return false
		}
	}
	return true
}

func countTableRows(lines []string, start int) int {
	count := 0
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
			count++
		} else {
			break
		}
	}
	return count
}

func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseRow(headers, cells []string, currency string, sourceRow int, sourceText string) *domain.Lot {
	index := func(needles ...string) int {
		for i, h := range headers {
			low := strings.ToLower(h)
			for _, needle := range needles {
				if strings.Contains(low, needle) {
					return i
				}
			}
		}
		return -1
	}
	value := func(i int) string {
		if i >= 0 && i < len(cells) {
			return strings.TrimSpace(cells[i])
		}
		return ""
	}
	symbolCell := value(index("symbol"))
	symbol := strings.TrimSpace(strings.Trim(symbolCell, "*"))
	if symbol == "" {
		return nil
	}
	classification := "lot"
	if strings.HasSuffix(strings.ToLower(symbol), " total") {
		classification = "aggregate"
		symbol = strings.TrimSpace(symbol[:len(symbol)-len(" total")])
	}
	quantityRaw := value(index("units", "quantity", "shares"))
	quantity := normalizeDecimal(quantityRaw)
	lot := &domain.Lot{
		Symbol: symbol, Description: value(index("description", "name")), Currency: currency,
		Quantity: quantity, QuantityRaw: quantityRaw, SourceRow: sourceRow, SourceText: sourceText,
		Classification: classification, Included: classification != "aggregate", Issues: []string{},
	}
	if classification != "aggregate" {
		if currency == "" {
			lot.Issues = append(lot.Issues, "missing currency")
		}
		if quantity == "" {
			lot.Issues = append(lot.Issues, "missing or invalid quantity")
		}
		if date := datePattern.FindString(sourceText); date != "" {
			lot.AcquisitionDate = &date
		} else {
			lot.Issues = append(lot.Issues, "missing acquisition/opening date")
		}
		if account := value(index("account", "broker")); account != "" {
			lot.AccountBroker = &account
		} else {
			lot.Issues = append(lot.Issues, "missing account/broker")
		}
	}
	entry := value(index("entry price / cost basis", "average cost", "unit cost"))
	cost := costColumn(headers, cells)
	if parsed := firstMoney(entry); parsed != "" {
		lot.UnitCost = &parsed
	} else if strings.Contains(strings.ToLower(entry), "price") {
		lot.Issues = append(lot.Issues, "missing or invalid unit cost")
	}
	if parsed := firstMoney(cost); parsed != "" {
		lot.CostBasis = &parsed
	} else if cost != "" {
		lot.Issues = append(lot.Issues, "missing or invalid cost basis")
	}
	if lot.CostBasis == nil && lot.UnitCost == nil {
		lot.Issues = append(lot.Issues, "missing cost basis")
	}
	return lot
}

func costColumn(headers, cells []string) string {
	for i, header := range headers {
		low := strings.ToLower(header)
		if strings.Contains(low, "approx. cost") || strings.Contains(low, "total cost") {
			if i < len(cells) {
				return strings.TrimSpace(cells[i])
			}
		}
	}
	for i, header := range headers {
		if strings.Contains(strings.ToLower(header), "cost basis") && i < len(cells) {
			return strings.TrimSpace(cells[i])
		}
	}
	return ""
}

func firstMoney(value string) string {
	match := moneyPattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return ""
	}
	return normalizeDecimal(match[2])
}

func normalizeDecimal(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.ToLower(value), "approx.")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, ",", "")
	if !decimalPattern.MatchString(value) {
		return ""
	}
	// Validation only; return the source decimal text unchanged.
	return value
}
