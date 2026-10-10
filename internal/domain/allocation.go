package domain

import (
	"math/big"
	"sort"
	"strings"
)

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
			value, err := ParseDecimal(*lot.ReportingValue)
			if err == nil && value.Sign() >= 0 {
				coverage.ValuedLots++
			}
		}
	}
	allocation := AllocationView{State: "partial", Coverage: coverage}
	if !valuation.Complete {
		if len(input.Lots) == 0 {
			allocation.State = "unavailable"
		}
		return allocation
	}
	if len(valuation.MissingDependencies) != 0 || len(valuation.StaleDependencies) != 0 || len(valuation.InvalidDependencies) != 0 {
		return allocation
	}

	type instrumentTotal struct {
		instrumentID string
		symbol       string
		currencies   map[string]bool
		value        Decimal
	}
	inputLots := make(map[string]ValuationLot, len(input.Lots))
	for _, lot := range input.Lots {
		if lot.ID == "" {
			return allocation
		}
		if _, exists := inputLots[lot.ID]; exists {
			return allocation
		}
		inputLots[lot.ID] = lot
	}
	if len(valuation.Lots) != len(input.Lots) {
		return allocation
	}
	if len(input.Lots) == 0 {
		if valuation.ReportingTotal != "" {
			total, err := ParseDecimal(valuation.ReportingTotal)
			if err != nil || total.Sign() < 0 || total.Sign() != 0 {
				return allocation
			}
		}
		allocation.State = "complete"
		allocation.ByInstrument = []InstrumentAllocation{}
		allocation.BySourceCurrency = []CurrencyAllocation{}
		return allocation
	}

	instruments := map[string]instrumentTotal{}
	currencies := map[string]Decimal{}
	seenLots := make(map[string]bool, len(valuation.Lots))
	total, err := ParseDecimal(valuation.ReportingTotal)
	if err != nil || total.Sign() < 0 {
		return allocation
	}
	calculatedTotal := ZeroDecimal()
	for _, lot := range valuation.Lots {
		inputLot, exists := inputLots[lot.LotID]
		if !exists || seenLots[lot.LotID] || lot.ReportingValue == nil {
			return allocation
		}
		seenLots[lot.LotID] = true
		value, err := ParseDecimal(*lot.ReportingValue)
		if err != nil || value.Sign() < 0 {
			return allocation
		}
		if lot.InstrumentID != "" && lot.InstrumentID != inputLot.InstrumentID {
			return allocation
		}
		if lot.Symbol != "" && inputLot.Symbol != "" && lot.Symbol != inputLot.Symbol {
			return allocation
		}
		if lot.Currency != "" && inputLot.Currency != "" && !strings.EqualFold(lot.Currency, inputLot.Currency) {
			return allocation
		}
		instrumentID := lot.InstrumentID
		if instrumentID == "" {
			instrumentID = inputLot.InstrumentID
		}
		if instrumentID == "" {
			return allocation
		}
		symbol := lot.Symbol
		if symbol == "" {
			symbol = inputLot.Symbol
		}
		currency := lot.Currency
		if currency == "" {
			currency = inputLot.Currency
		}
		if currency == "" {
			return allocation
		}
		calculatedTotal = calculatedTotal.Add(value)
		current, exists := instruments[instrumentID]
		if !exists {
			current = instrumentTotal{instrumentID: instrumentID, symbol: symbol, currencies: map[string]bool{}, value: ZeroDecimal()}
		}
		current.value = current.value.Add(value)
		current.currencies[currency] = true
		instruments[instrumentID] = current
		currencies[currency] = currencies[currency].Add(value)
	}
	if len(seenLots) != len(input.Lots) || calculatedTotal.Rat().Cmp(total.Rat()) != 0 {
		return allocation
	}
	allocation.State = "complete"

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
		currencies := make([]string, 0, len(item.currencies))
		for currency := range item.currencies {
			currencies = append(currencies, currency)
		}
		sort.Strings(currencies)
		allocation.ByInstrument = append(allocation.ByInstrument, InstrumentAllocation{InstrumentID: item.instrumentID, Symbol: item.symbol, SourceCurrency: strings.Join(currencies, " / "), Value: item.value.String(), Percentage: instrumentPercentages[index]})
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
	if err != nil || total.Sign() <= 0 {
		for index := range percentages {
			percentages[index] = "0"
		}
		return percentages
	}

	// Apportion the 10,000 hundredths of a percent by largest remainder. The
	// exact rationals keep the result deterministic even for tiny values and
	// avoid a residual being assigned to an arbitrary final slice.
	type remainder struct {
		index int
		num   *big.Int
		den   *big.Int
	}
	units := make([]*big.Int, len(values))
	remainders := make([]remainder, 0, len(values))
	used := big.NewInt(0)
	for index, value := range values {
		if value.Sign() < 0 {
			return make([]string, len(values))
		}
		quota := new(big.Rat).Quo(value.Rat(), total.Rat())
		quota.Mul(quota, new(big.Rat).SetInt64(10000))
		whole, fraction := new(big.Int).QuoRem(quota.Num(), quota.Denom(), new(big.Int))
		units[index] = whole
		used.Add(used, whole)
		remainders = append(remainders, remainder{index: index, num: fraction, den: new(big.Int).Set(quota.Denom())})
	}
	sort.SliceStable(remainders, func(left, right int) bool {
		l, r := remainders[left], remainders[right]
		leftCross := new(big.Int).Mul(l.num, r.den)
		rightCross := new(big.Int).Mul(r.num, l.den)
		comparison := leftCross.Cmp(rightCross)
		if comparison != 0 {
			return comparison > 0
		}
		return l.index < r.index
	})
	remaining := new(big.Int).Sub(big.NewInt(10000), used)
	for index := int64(0); index < remaining.Int64(); index++ {
		units[remainders[index].index].Add(units[remainders[index].index], big.NewInt(1))
	}
	for index, value := range units {
		percentages[index] = percentageUnitsString(value)
	}
	return percentages
}

func percentageUnitsString(units *big.Int) string {
	if units.Sign() == 0 {
		return "0"
	}
	whole, fraction := new(big.Int).QuoRem(new(big.Int).Set(units), big.NewInt(100), new(big.Int))
	if fraction.Sign() == 0 {
		return whole.String()
	}
	fractionText := fraction.Text(10)
	if len(fractionText) == 1 {
		fractionText = "0" + fractionText
	}
	return whole.String() + "." + fractionText
}
