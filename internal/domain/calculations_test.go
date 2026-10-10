package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDecimalCalculationDoesNotUseBinaryFloat(t *testing.T) {
	quantity := MustDecimal("0.1")
	price := MustDecimal("0.2")
	if got := quantity.Mul(price).String(); got != "0.02" {
		t.Fatalf("market value = %s", got)
	}
	if got := MustDecimal("100.00").Sub(MustDecimal("99.99")).String(); got != "0.01" {
		t.Fatalf("pnl = %s", got)
	}
}

func TestClassifyFreshnessUsesUTCAndFutureTolerance(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	within := ClassifyFreshness(asOf.Add(DefaultFutureTolerance).Format(time.RFC3339Nano), asOf, time.Hour)
	if within.Status != Fresh || within.Age != "0s" {
		t.Fatalf("within-tolerance freshness = %+v", within)
	}
	beyond := ClassifyFreshness(asOf.Add(DefaultFutureTolerance+time.Nanosecond).Format(time.RFC3339Nano), asOf, time.Hour)
	if beyond.Status != Future {
		t.Fatalf("beyond-tolerance freshness = %+v", beyond)
	}
	singapore := asOf.Add(4 * time.Minute).In(time.FixedZone("SGT", 8*60*60)).Format(time.RFC3339Nano)
	if zoned := ClassifyFreshness(singapore, asOf, time.Hour); zoned.Status != Fresh || zoned.Age != "0s" {
		t.Fatalf("UTC-normalized freshness = %+v", zoned)
	}
}

func TestCalculateMultipleLotsAndOriginalCurrencySubtotals(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	lots := []ValuationLot{
		{ID: "usd-1", InstrumentID: "a", Symbol: "A", Currency: "USD", Quantity: "0.1", CostBasis: ptrTest("0.01")},
		{ID: "usd-2", InstrumentID: "a", Symbol: "A", Currency: "USD", Quantity: "2", CostBasis: ptrTest("10")},
		{ID: "sgd", InstrumentID: "b", Symbol: "B", Currency: "SGD", Quantity: "1", UnitCost: ptrTest("3")},
		{ID: "myr", InstrumentID: "c", Symbol: "C", Currency: "MYR", Quantity: "1", UnitCost: ptrTest("4")},
		{ID: "jpy", InstrumentID: "d", Symbol: "D", Currency: "JPY", Quantity: "1", UnitCost: ptrTest("5")},
	}
	quotes := []Quote{{ID: "q-a", InstrumentID: "a", Price: "0.2", Currency: "USD", MarketAt: asOf.Format(time.RFC3339), Source: "manual", Basis: "close", Provenance: "fixture"}, {ID: "q-b", InstrumentID: "b", Price: "4", Currency: "SGD", MarketAt: asOf.Format(time.RFC3339), Source: "manual", Basis: "close", Provenance: "fixture"}, {ID: "q-c", InstrumentID: "c", Price: "6", Currency: "MYR", MarketAt: asOf.Format(time.RFC3339), Source: "manual", Basis: "close", Provenance: "fixture"}, {ID: "q-d", InstrumentID: "d", Price: "7", Currency: "JPY", MarketAt: asOf.Format(time.RFC3339), Source: "manual", Basis: "close", Provenance: "fixture"}}
	fx := []FXRate{{ID: "usd-myr", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4", MarketAt: asOf.Format(time.RFC3339)}, {ID: "sgd-myr", BaseCurrency: "SGD", QuoteCurrency: "MYR", Rate: "3", MarketAt: asOf.Format(time.RFC3339)}, {ID: "jpy-myr", BaseCurrency: "JPY", QuoteCurrency: "MYR", Rate: "0.03", MarketAt: asOf.Format(time.RFC3339)}}
	result, err := Calculate(ValuationInput{Lots: lots, Quotes: quotes, FXRates: fx, ReportingCurrency: "MYR", AsOf: asOf, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.ReportingTotal != "19.89" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Subtotals) != 4 || result.Subtotals[0].Currency != "JPY" {
		t.Fatalf("subtotals = %+v", result.Subtotals)
	}
}

func TestCalculateMissingAndStaleDependenciesNeverBecomeZero(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	result, err := Calculate(ValuationInput{Lots: []ValuationLot{{ID: "missing", InstrumentID: "none", Currency: "USD", Quantity: "1"}, {ID: "stale", InstrumentID: "stale", Currency: "USD", Quantity: "1"}}, Quotes: []Quote{{ID: "old", InstrumentID: "stale", Price: "2", Currency: "USD", MarketAt: asOf.Add(-48 * time.Hour).Format(time.RFC3339)}}, ReportingCurrency: "MYR", AsOf: asOf, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.ReportingTotal != "" || len(result.MissingDependencies) != 1 || len(result.StaleDependencies) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCalculateFutureQuoteBeyondToleranceIsIncomplete(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	result, err := Calculate(ValuationInput{
		Lots:   []ValuationLot{{ID: "future", InstrumentID: "future", Currency: "USD", Quantity: "1"}},
		Quotes: []Quote{{ID: "future-quote", InstrumentID: "future", Price: "2", Currency: "USD", MarketAt: asOf.Add(DefaultFutureTolerance + time.Second).Format(time.RFC3339Nano)}},
		AsOf:   asOf, MaxAge: time.Hour, ReportingCurrency: "MYR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.ReportingTotal != "" || len(result.InvalidDependencies) != 1 || result.InvalidDependencies[0] != "quote:future-quote" {
		t.Fatalf("future quote result = %+v", result)
	}
}

func TestCalculateStaleFXIsIncomplete(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	result, err := Calculate(ValuationInput{
		Lots:    []ValuationLot{{ID: "stale-fx", InstrumentID: "stale-fx", Currency: "USD", Quantity: "1"}},
		Quotes:  []Quote{{ID: "fresh-quote", InstrumentID: "stale-fx", Price: "2", Currency: "USD", MarketAt: asOf.Format(time.RFC3339Nano)}},
		FXRates: []FXRate{{ID: "old-fx", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4", MarketAt: asOf.Add(-2 * time.Hour).Format(time.RFC3339Nano)}},
		AsOf:    asOf, MaxAge: time.Hour, ReportingCurrency: "MYR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.ReportingTotal != "" || len(result.StaleDependencies) != 1 || result.StaleDependencies[0] != "stale fx:old-fx" {
		t.Fatalf("stale FX result = %+v", result)
	}
}

func TestConvertDirectInvertedAndAmbiguous(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	converted, ids, err := Convert(MustDecimal("10"), "MYR", "USD", []FXRate{{ID: "u-m", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4", MarketAt: at}}, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), time.Hour)
	if err != nil || converted.String() != "2.5" || len(ids) != 1 {
		t.Fatalf("converted=%s ids=%v err=%v", converted.String(), ids, err)
	}
	_, _, err = Convert(MustDecimal("1"), "USD", "MYR", []FXRate{{ID: "a", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4", MarketAt: at}, {ID: "b", BaseCurrency: "USD", QuoteCurrency: "SGD", Rate: "2", MarketAt: at}, {ID: "c", BaseCurrency: "SGD", QuoteCurrency: "MYR", Rate: "2", MarketAt: at}}, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), time.Hour)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v", err)
	}
}

func ptrTest(value string) *string { return &value }
