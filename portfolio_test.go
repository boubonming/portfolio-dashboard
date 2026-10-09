package main

import (
	"math"
	"testing"
)

func TestCalculateSummary(t *testing.T) {
	tests := []struct {
		name           string
		holdings       []Holding
		marketValue    float64
		costBasis      float64
		gainLoss       float64
		gainLossPct    float64
		holdingGainPct float64
		allocationPct  float64
	}{
		{
			name:        "positive performance",
			holdings:    []Holding{{Symbol: "UP", Name: "Up", Category: "Equities", Quantity: 10, AverageCost: 10, CurrentPrice: 12}},
			marketValue: 120, costBasis: 100, gainLoss: 20, gainLossPct: 20, holdingGainPct: 20, allocationPct: 100,
		},
		{
			name:        "negative performance",
			holdings:    []Holding{{Symbol: "DOWN", Name: "Down", Category: "Equities", Quantity: 10, AverageCost: 10, CurrentPrice: 8}},
			marketValue: 80, costBasis: 100, gainLoss: -20, gainLossPct: -20, holdingGainPct: -20, allocationPct: 100,
		},
		{
			name:        "zero cost basis guards percentages",
			holdings:    []Holding{{Symbol: "FREE", Name: "Free", Category: "Other", Quantity: 10, AverageCost: 0, CurrentPrice: 5}},
			marketValue: 50, costBasis: 0, gainLoss: 50, gainLossPct: 0, holdingGainPct: 0, allocationPct: 100,
		},
		{
			name:           "empty portfolio",
			holdings:       []Holding{},
			marketValue:    0,
			costBasis:      0,
			gainLoss:       0,
			gainLossPct:    0,
			holdingGainPct: 0,
			allocationPct:  0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := Calculate(PortfolioSnapshot{AsOf: "2026-10-01", Currency: "sgd", Holdings: test.holdings})
			if err != nil {
				t.Fatalf("Calculate() error = %v", err)
			}
			assertClose(t, "market value", response.Summary.TotalMarketValue, test.marketValue)
			assertClose(t, "cost basis", response.Summary.TotalCostBasis, test.costBasis)
			assertClose(t, "gain/loss", response.Summary.GainLoss, test.gainLoss)
			assertClose(t, "gain/loss percentage", response.Summary.GainLossPct, test.gainLossPct)
			if len(response.Holdings) > 0 {
				assertClose(t, "holding gain/loss percentage", response.Holdings[0].GainLossPct, test.holdingGainPct)
				assertClose(t, "allocation percentage", response.Holdings[0].AllocationPct, test.allocationPct)
			}
			if response.Currency != "SGD" {
				t.Errorf("Currency = %q, want SGD", response.Currency)
			}
		})
	}
}

func TestCalculateAggregatesAllocations(t *testing.T) {
	response, err := Calculate(PortfolioSnapshot{
		AsOf: "2026-10-01", Currency: "USD",
		Holdings: []Holding{
			{Symbol: "A", Name: "A", Category: "Equities", Quantity: 1, AverageCost: 10, CurrentPrice: 10},
			{Symbol: "B", Name: "B", Category: "Cash", Quantity: 2, AverageCost: 25, CurrentPrice: 25},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Allocations) != 2 || response.Allocations[0].Category != "Cash" {
		t.Fatalf("Allocations = %#v, want Cash then Equities", response.Allocations)
	}
	assertClose(t, "cash allocation", response.Allocations[0].Percentage, 83.3333333333)
	assertClose(t, "equities allocation", response.Allocations[1].Percentage, 16.6666666667)
}

func TestCalculateRejectsInvalidInput(t *testing.T) {
	cases := []Holding{
		{Symbol: "", Name: "Name", Category: "Equities", Quantity: 1, AverageCost: 1, CurrentPrice: 1},
		{Symbol: "A", Name: "Name", Category: "", Quantity: 1, AverageCost: 1, CurrentPrice: 1},
		{Symbol: "A", Name: "Name", Category: "Equities", Quantity: -1, AverageCost: 1, CurrentPrice: 1},
	}
	for i, holding := range cases {
		if _, err := Calculate(PortfolioSnapshot{AsOf: "2026-10-01", Currency: "USD", Holdings: []Holding{holding}}); err == nil {
			t.Errorf("case %d: Calculate() error = nil, want validation error", i)
		}
	}
}

func assertClose(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}
