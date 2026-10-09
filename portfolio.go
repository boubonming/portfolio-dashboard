package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

// PortfolioSnapshot is the editable, point-in-time portfolio input format.
type PortfolioSnapshot struct {
	AsOf     string    `json:"as_of"`
	Currency string    `json:"currency"`
	Holdings []Holding `json:"holdings"`
}

// Holding contains the user-supplied fields and the values calculated by the server.
type Holding struct {
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	Quantity     float64 `json:"quantity"`
	AverageCost  float64 `json:"average_cost"`
	CurrentPrice float64 `json:"current_price"`
	Category     string  `json:"category"`

	MarketValue   float64 `json:"market_value"`
	CostBasis     float64 `json:"cost_basis"`
	GainLoss      float64 `json:"gain_loss"`
	GainLossPct   float64 `json:"gain_loss_pct"`
	AllocationPct float64 `json:"allocation_pct"`
}

type Summary struct {
	TotalMarketValue float64 `json:"total_market_value"`
	TotalCostBasis   float64 `json:"total_cost_basis"`
	GainLoss         float64 `json:"gain_loss"`
	GainLossPct      float64 `json:"gain_loss_pct"`
}

type Allocation struct {
	Category    string  `json:"category"`
	MarketValue float64 `json:"market_value"`
	Percentage  float64 `json:"percentage"`
}

// PortfolioResponse is the normalized API shape returned to the dashboard.
type PortfolioResponse struct {
	AsOf        string       `json:"as_of"`
	Currency    string       `json:"currency"`
	Holdings    []Holding    `json:"holdings"`
	Summary     Summary      `json:"summary"`
	Allocations []Allocation `json:"allocations"`
}

// LoadPortfolio reads and validates a JSON snapshot from disk.
func LoadPortfolio(path string) (PortfolioSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return PortfolioSnapshot{}, fmt.Errorf("open portfolio: %w", err)
	}
	defer file.Close()

	var snapshot PortfolioSnapshot
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return PortfolioSnapshot{}, fmt.Errorf("decode portfolio: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return PortfolioSnapshot{}, fmt.Errorf("decode portfolio: %w", err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return PortfolioSnapshot{}, err
	}
	return normalizeSnapshot(snapshot), nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateSnapshot(snapshot PortfolioSnapshot) error {
	if strings.TrimSpace(snapshot.AsOf) == "" {
		return errors.New("portfolio as_of is required")
	}
	currency := strings.TrimSpace(snapshot.Currency)
	if len(currency) != 3 {
		return errors.New("portfolio currency must be a three-letter code")
	}
	for i, holding := range snapshot.Holdings {
		if strings.TrimSpace(holding.Symbol) == "" || strings.TrimSpace(holding.Name) == "" {
			return fmt.Errorf("holding %d symbol and name are required", i)
		}
		if strings.TrimSpace(holding.Category) == "" {
			return fmt.Errorf("holding %d category is required", i)
		}
		if !finiteNonNegative(holding.Quantity) || !finiteNonNegative(holding.AverageCost) || !finiteNonNegative(holding.CurrentPrice) {
			return fmt.Errorf("holding %d quantity, average_cost, and current_price must be finite and non-negative", i)
		}
	}
	return nil
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func normalizeSnapshot(snapshot PortfolioSnapshot) PortfolioSnapshot {
	snapshot.AsOf = strings.TrimSpace(snapshot.AsOf)
	snapshot.Currency = strings.ToUpper(strings.TrimSpace(snapshot.Currency))
	for i := range snapshot.Holdings {
		snapshot.Holdings[i].Symbol = strings.TrimSpace(snapshot.Holdings[i].Symbol)
		snapshot.Holdings[i].Name = strings.TrimSpace(snapshot.Holdings[i].Name)
		snapshot.Holdings[i].Category = strings.TrimSpace(snapshot.Holdings[i].Category)
	}
	return snapshot
}

// Calculate produces deterministic summary, holding, and category allocation values.
func Calculate(snapshot PortfolioSnapshot) (PortfolioResponse, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return PortfolioResponse{}, err
	}
	snapshot = normalizeSnapshot(snapshot)
	response := PortfolioResponse{
		AsOf:        snapshot.AsOf,
		Currency:    snapshot.Currency,
		Holdings:    make([]Holding, len(snapshot.Holdings)),
		Allocations: make([]Allocation, 0),
	}
	copy(response.Holdings, snapshot.Holdings)

	categoryValues := make(map[string]float64)
	categoryOrder := make([]string, 0)
	for i := range response.Holdings {
		holding := &response.Holdings[i]
		holding.MarketValue = holding.Quantity * holding.CurrentPrice
		holding.CostBasis = holding.Quantity * holding.AverageCost
		if math.IsInf(holding.MarketValue, 0) || math.IsInf(holding.CostBasis, 0) {
			return PortfolioResponse{}, fmt.Errorf("holding %d calculation overflow", i)
		}
		holding.GainLoss = holding.MarketValue - holding.CostBasis
		holding.GainLossPct = percentage(holding.GainLoss, holding.CostBasis)
		response.Summary.TotalMarketValue += holding.MarketValue
		response.Summary.TotalCostBasis += holding.CostBasis
		if _, exists := categoryValues[holding.Category]; !exists {
			categoryOrder = append(categoryOrder, holding.Category)
		}
		categoryValues[holding.Category] += holding.MarketValue
	}
	response.Summary.GainLoss = response.Summary.TotalMarketValue - response.Summary.TotalCostBasis
	response.Summary.GainLossPct = percentage(response.Summary.GainLoss, response.Summary.TotalCostBasis)
	if math.IsInf(response.Summary.TotalMarketValue, 0) || math.IsInf(response.Summary.TotalCostBasis, 0) {
		return PortfolioResponse{}, errors.New("portfolio calculation overflow")
	}
	for i := range response.Holdings {
		response.Holdings[i].AllocationPct = percentage(response.Holdings[i].MarketValue, response.Summary.TotalMarketValue)
	}
	for _, category := range categoryOrder {
		value := categoryValues[category]
		response.Allocations = append(response.Allocations, Allocation{
			Category:    category,
			MarketValue: value,
			Percentage:  percentage(value, response.Summary.TotalMarketValue),
		})
	}
	// Keep the legend stable even if a future input source changes map construction.
	sort.SliceStable(response.Allocations, func(i, j int) bool {
		return response.Allocations[i].MarketValue > response.Allocations[j].MarketValue
	})
	return response, nil
}

func percentage(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator * 100
}
