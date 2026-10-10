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

func TestBuildAllocationAggregatesDuplicateLotsAndReconcilesPercentages(t *testing.T) {
	input := ValuationInput{Lots: []ValuationLot{
		{ID: "a-1", InstrumentID: "instrument-a", Symbol: "AAA", Currency: "USD"},
		{ID: "a-2", InstrumentID: "instrument-a", Symbol: "AAA", Currency: "USD"},
		{ID: "b-1", InstrumentID: "instrument-b", Symbol: "BBB", Currency: "SGD"},
	}}
	valuation := Valuation{Complete: true, ReportingCurrency: "MYR", ReportingTotal: "100", Lots: []LotValuation{
		{LotID: "a-1", InstrumentID: "instrument-a", Symbol: "AAA", Currency: "USD", ReportingValue: ptrTest("20")},
		{LotID: "a-2", InstrumentID: "instrument-a", Symbol: "AAA", Currency: "USD", ReportingValue: ptrTest("30")},
		{LotID: "b-1", InstrumentID: "instrument-b", Symbol: "BBB", Currency: "SGD", ReportingValue: ptrTest("50")},
	}}
	allocation := BuildAllocation(input, valuation)
	if allocation.State != "complete" || allocation.Coverage.TotalLots != 3 || allocation.Coverage.ValuedLots != 3 {
		t.Fatalf("coverage = %+v", allocation)
	}
	if len(allocation.ByInstrument) != 2 || allocation.ByInstrument[0].Symbol != "AAA" || allocation.ByInstrument[0].Value != "50" || allocation.ByInstrument[0].Percentage != "50" {
		t.Fatalf("instrument allocation = %+v", allocation.ByInstrument)
	}
	if len(allocation.BySourceCurrency) != 2 || allocation.BySourceCurrency[0].Currency != "SGD" || allocation.BySourceCurrency[0].Value != "50" || allocation.BySourceCurrency[1].Percentage != "50" {
		t.Fatalf("currency allocation = %+v", allocation.BySourceCurrency)
	}
}

func TestBuildAllocationWithholdsPartialDistribution(t *testing.T) {
	missing := BuildAllocation(ValuationInput{Lots: []ValuationLot{{ID: "missing"}, {ID: "stale"}}}, Valuation{
		Complete:            false,
		Lots:                []LotValuation{{LotID: "missing"}},
		MissingDependencies: []string{"quote:missing"},
		StaleDependencies:   []string{"quote:stale"},
	})
	if missing.State != "partial" || missing.Coverage.TotalLots != 2 || missing.Coverage.ValuedLots != 0 || missing.Coverage.MissingDependencies != 1 || missing.Coverage.StaleDependencies != 1 || len(missing.ByInstrument) != 0 || len(missing.BySourceCurrency) != 0 {
		t.Fatalf("partial allocation = %+v", missing)
	}
}

func TestAllocationPercentagesUseLargestRemainderWithZeroSlices(t *testing.T) {
	percentages := allocationPercentages([]Decimal{ZeroDecimal(), MustDecimal("1"), MustDecimal("1"), MustDecimal("1")}, "3")
	got := strings.Join(percentages, ",")
	if got != "0,33.34,33.33,33.33" {
		t.Fatalf("percentages = %s", got)
	}
	sum := ZeroDecimal()
	for _, percentage := range percentages {
		sum = sum.Add(MustDecimal(percentage))
		if MustDecimal(percentage).Sign() < 0 {
			t.Fatalf("negative percentage %q", percentage)
		}
	}
	if sum.String() != "100" {
		t.Fatalf("percentage sum = %s", sum.String())
	}

	// Each slice rounds to 0.99 without apportionment; the extra hundredth
	// must go to the largest remainder rather than an arbitrary final slice.
	values := make([]Decimal, 101)
	for index := range values {
		values[index] = MustDecimal("1")
	}
	percentages = allocationPercentages(values, "101")
	countOne, countNinetyNine := 0, 0
	for _, percentage := range percentages {
		switch percentage {
		case "1":
			countOne++
		case "0.99":
			countNinetyNine++
		default:
			t.Fatalf("unexpected rounded percentage %q", percentage)
		}
	}
	if countOne != 1 || countNinetyNine != 100 {
		t.Fatalf("largest remainder counts = %d one, %d ninety-nine", countOne, countNinetyNine)
	}
	if got := strings.Join(allocationPercentages([]Decimal{MustDecimal("1"), MustDecimal("9999")}, "10000"), ","); got != "0.01,99.99" {
		t.Fatalf("small slice percentages = %s", got)
	}
}

func TestBuildAllocationResolvesLegacyIdentityByLotID(t *testing.T) {
	input := ValuationInput{Lots: []ValuationLot{
		{ID: "usd-lot", InstrumentID: "instrument-usd", Symbol: "SAME", Currency: "USD"},
		{ID: "sgd-lot", InstrumentID: "instrument-sgd", Symbol: "SAME", Currency: "SGD"},
	}}
	allocation := BuildAllocation(input, Valuation{Complete: true, ReportingTotal: "30", Lots: []LotValuation{
		{LotID: "usd-lot", Symbol: "SAME", Currency: "USD", ReportingValue: ptrTest("10")},
		{LotID: "sgd-lot", Symbol: "SAME", Currency: "SGD", ReportingValue: ptrTest("20")},
	}})
	if allocation.State != "complete" || len(allocation.ByInstrument) != 2 {
		t.Fatalf("legacy allocation = %+v", allocation)
	}
	if allocation.ByInstrument[0].InstrumentID != "instrument-sgd" || allocation.ByInstrument[1].InstrumentID != "instrument-usd" {
		t.Fatalf("instrument identity = %+v", allocation.ByInstrument)
	}
	if allocation.ByInstrument[0].SourceCurrency != "SGD" || allocation.ByInstrument[1].SourceCurrency != "USD" {
		t.Fatalf("source currency identity = %+v", allocation.ByInstrument)
	}
}

func TestBuildAllocationFailsClosedForMalformedCompletePayload(t *testing.T) {
	input := ValuationInput{Lots: []ValuationLot{{ID: "one", InstrumentID: "instrument-one", Symbol: "ONE", Currency: "USD"}, {ID: "two", InstrumentID: "instrument-two", Symbol: "TWO", Currency: "USD"}}}
	cases := []struct {
		name      string
		valuation Valuation
	}{
		{name: "missing lot", valuation: Valuation{Complete: true, ReportingTotal: "3", Lots: []LotValuation{{LotID: "one", InstrumentID: "instrument-one", Currency: "USD", ReportingValue: ptrTest("1")}}}},
		{name: "malformed value", valuation: Valuation{Complete: true, ReportingTotal: "3", Lots: []LotValuation{{LotID: "one", InstrumentID: "instrument-one", Currency: "USD", ReportingValue: ptrTest("1.2.3")}, {LotID: "two", InstrumentID: "instrument-two", Currency: "USD", ReportingValue: ptrTest("2")}}}},
		{name: "inconsistent total", valuation: Valuation{Complete: true, ReportingTotal: "4", Lots: []LotValuation{{LotID: "one", InstrumentID: "instrument-one", Currency: "USD", ReportingValue: ptrTest("1")}, {LotID: "two", InstrumentID: "instrument-two", Currency: "USD", ReportingValue: ptrTest("2")}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			allocation := BuildAllocation(input, test.valuation)
			if allocation.State != "partial" || len(allocation.ByInstrument) != 0 || len(allocation.BySourceCurrency) != 0 {
				t.Fatalf("malformed allocation = %+v", allocation)
			}
		})
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
