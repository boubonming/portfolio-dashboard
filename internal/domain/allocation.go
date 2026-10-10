package domain

import "sort"

const allocationPercentageScale = 2

// BuildAllocation creates reporting-currency distributions only for a
// complete valuation. Values are aggregated from lot-level reporting values,
// so duplicate lots are counted once each and never duplicated by a join.
func BuildAllocation(input ValuationInput, valuation Valuation) AllocationView {
	coverage := AllocationCoverage{
		TotalLots:           len(input.Lots),
		MissingDependencies: len(valuation.MissingDependencies),
		StaleDependencies:   len(valuation.StaleDependencies),
		InvalidDependencies: len(valuation.InvalidDependencies),
	}
	for _, lot := range valuation.Lots {
		if lot.ReportingValue != nil {
			coverage.ValuedLots++
		}
	}
	state := "partial"
	if coverage.TotalLots == 0 && valuation.Complete {
		state = "complete"
	} else if valuation.Complete {
		state = "complete"
	}
	allocation := AllocationView{State: state, Coverage: coverage, ByInstrument: []InstrumentAllocation{}, BySourceCurrency: []CurrencyAllocation{}}
	if !valuation.Complete {
		if len(input.Lots) == 0 {
			allocation.State = "unavailable"
		}
		return allocation
	}

	type instrumentTotal struct {
		instrumentID string
		symbol       string
		value        Decimal
	}
	instruments := map[string]instrumentTotal{}
	currencies := map[string]Decimal{}
	for _, lot := range valuation.Lots {
		if lot.ReportingValue == nil {
			continue
		}
		value, err := ParseDecimal(*lot.ReportingValue)
		if err != nil {
			continue
		}
		instrumentID := lot.InstrumentID
		if instrumentID == "" {
			instrumentID = lot.Symbol
		}
		current, exists := instruments[instrumentID]
		if !exists {
			current = instrumentTotal{instrumentID: instrumentID, symbol: lot.Symbol, value: ZeroDecimal()}
		}
		current.value = current.value.Add(value)
		instruments[instrumentID] = current
		currencies[lot.Currency] = currencies[lot.Currency].Add(value)
	}

	instrumentKeys := make([]string, 0, len(instruments))
	for key := range instruments {
		instrumentKeys = append(instrumentKeys, key)
	}
	sort.Strings(instrumentKeys)
	instrumentValues := make([]Decimal, 0, len(instrumentKeys))
	for _, key := range instrumentKeys {
		instrumentValues = append(instrumentValues, instruments[key].value)
	}
	instrumentPercentages := allocationPercentages(instrumentValues, valuation.ReportingTotal)
	for index, key := range instrumentKeys {
		item := instruments[key]
		allocation.ByInstrument = append(allocation.ByInstrument, InstrumentAllocation{InstrumentID: item.instrumentID, Symbol: item.symbol, Value: item.value.String(), Percentage: instrumentPercentages[index]})
	}

	currencyKeys := make([]string, 0, len(currencies))
	for key := range currencies {
		currencyKeys = append(currencyKeys, key)
	}
	sort.Strings(currencyKeys)
	currencyValues := make([]Decimal, 0, len(currencyKeys))
	for _, key := range currencyKeys {
		currencyValues = append(currencyValues, currencies[key])
	}
	currencyPercentages := allocationPercentages(currencyValues, valuation.ReportingTotal)
	for index, key := range currencyKeys {
		allocation.BySourceCurrency = append(allocation.BySourceCurrency, CurrencyAllocation{Currency: key, Value: currencies[key].String(), Percentage: currencyPercentages[index]})
	}
	return allocation
}

func allocationPercentages(values []Decimal, totalText string) []string {
	percentages := make([]string, len(values))
	if len(values) == 0 {
		return percentages
	}
	total, err := ParseDecimal(totalText)
	if err != nil || total.Sign() == 0 {
		for index := range percentages {
			percentages[index] = "0"
		}
		return percentages
	}
	sum := ZeroDecimal()
	for index, value := range values {
		if index == len(values)-1 {
			percentages[index] = MustDecimal("100").Sub(sum).String()
			continue
		}
		percentage, err := value.Mul(MustDecimal("100")).Div(total, allocationPercentageScale)
		if err != nil {
			percentages[index] = "0"
			continue
		}
		percentages[index] = percentage.String()
		sum = sum.Add(percentage)
	}
	return percentages
}
