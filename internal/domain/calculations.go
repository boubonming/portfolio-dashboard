package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const DefaultFreshness = 36 * time.Hour
const DefaultFutureTolerance = 5 * time.Minute
const CalculationVersion = "phase-b.decimal.v1"

// ClassifyFreshness uses UTC timestamps. Presentation layers may render these
// timestamps in SGT; the accounting decision is always made in UTC.
func ClassifyFreshness(marketAt string, asOf time.Time, maxAge time.Duration) Freshness {
	at, err := time.Parse(time.RFC3339Nano, marketAt)
	if err != nil {
		return Freshness{Status: Invalid, MarketAt: marketAt}
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	// Ingestion allows a small clock-skew window for observations received just
	// ahead of the requested as-of instant. Keep classification consistent with
	// that rule: within the window the observation is fresh with zero age, while
	// anything beyond it is a future dependency.
	if at.After(asOf.Add(DefaultFutureTolerance)) {
		return Freshness{Status: Future, MarketAt: marketAt}
	}
	if maxAge <= 0 {
		maxAge = DefaultFreshness
	}
	age := asOf.Sub(at)
	if age < 0 {
		age = 0
	}
	if age > maxAge {
		return Freshness{Status: Stale, MarketAt: marketAt, Age: age.String()}
	}
	return Freshness{Status: Fresh, MarketAt: marketAt, Age: age.String()}
}

func Calculate(input ValuationInput) (Valuation, error) {
	if input.ReportingCurrency == "" {
		input.ReportingCurrency = "MYR"
	}
	input.ReportingCurrency = strings.ToUpper(strings.TrimSpace(input.ReportingCurrency))
	if input.AsOf.IsZero() {
		input.AsOf = time.Now().UTC()
	}
	out := Valuation{Complete: true, ReportingCurrency: input.ReportingCurrency, Lots: []LotValuation{}, Subtotals: []CurrencySubtotal{}, QuoteIDs: []string{}, FXIDs: []string{}, MissingDependencies: []string{}, StaleDependencies: []string{}, InvalidDependencies: []string{}, SourceTimestamps: []string{}, PresentationTimezone: "Asia/Singapore"}
	quotes := latestQuotes(input.Quotes)
	seenQuote, seenFX := map[string]bool{}, map[string]bool{}
	type subtotal struct{ market, cost, pnl Decimal }
	subtotals := map[string]*subtotal{}
	total := ZeroDecimal()
	for _, lot := range input.Lots {
		lv := LotValuation{LotID: lot.ID, Symbol: lot.Symbol, InstrumentID: lot.InstrumentID, Currency: lot.Currency, Quantity: lot.Quantity}
		qty, err := ParseDecimal(lot.Quantity)
		if err != nil || qty.Sign() < 0 {
			out.Complete = false
			out.InvalidDependencies = append(out.InvalidDependencies, "lot:"+lot.ID+":quantity")
			out.Lots = append(out.Lots, lv)
			continue
		}
		quote, ok := quotes[lot.InstrumentID]
		if !ok {
			out.Complete = false
			out.MissingDependencies = append(out.MissingDependencies, "quote:"+lot.InstrumentID)
			lv.Freshness.Status = Missing
			out.Lots = append(out.Lots, lv)
			continue
		}
		fresh := ClassifyFreshness(quote.MarketAt, input.AsOf, input.MaxAge)
		lv.Freshness = fresh
		if fresh.Status != Fresh {
			out.Complete = false
			switch fresh.Status {
			case Stale:
				out.StaleDependencies = append(out.StaleDependencies, "quote:"+quote.ID)
			case Future, Invalid:
				out.InvalidDependencies = append(out.InvalidDependencies, "quote:"+quote.ID)
			}
			out.Lots = append(out.Lots, lv)
			continue
		}
		price, err := ParseDecimal(quote.Price)
		if err != nil || price.Sign() <= 0 || strings.ToUpper(quote.Currency) != strings.ToUpper(lot.Currency) {
			out.Complete = false
			out.InvalidDependencies = append(out.InvalidDependencies, "quote:"+quote.ID)
			out.Lots = append(out.Lots, lv)
			continue
		}
		market := qty.Mul(price)
		lv.Price, lv.MarketValue, lv.QuoteID = price.String(), market.String(), quote.ID
		if !seenQuote[quote.ID] {
			out.QuoteIDs = append(out.QuoteIDs, quote.ID)
			seenQuote[quote.ID] = true
		}
		st := subtotals[lot.Currency]
		if st == nil {
			st = &subtotal{market: ZeroDecimal(), cost: ZeroDecimal(), pnl: ZeroDecimal()}
			subtotals[lot.Currency] = st
		}
		st.market = st.market.Add(market)
		var cost Decimal
		costKnown := false
		if lot.CostBasis != nil {
			cost, err = ParseDecimal(*lot.CostBasis)
			costKnown = err == nil && cost.Sign() >= 0
		}
		if lot.CostBasis == nil && lot.UnitCost != nil {
			unit, e := ParseDecimal(*lot.UnitCost)
			if e == nil && unit.Sign() >= 0 {
				cost = qty.Mul(unit)
				costKnown = true
			}
		}
		if lot.CostBasis != nil || lot.UnitCost != nil {
			if !costKnown {
				out.Complete = false
				out.InvalidDependencies = append(out.InvalidDependencies, "cost:"+lot.ID)
			} else {
				costText := cost.String()
				pnl := market.Sub(cost)
				lv.CostBasis, lv.UnrealisedPnL = &costText, stringPtr(pnl.String())
				st.cost = st.cost.Add(cost)
				st.pnl = st.pnl.Add(pnl)
			}
		}
		converted, fxIDs, convErr := Convert(market, lot.Currency, input.ReportingCurrency, input.FXRates, input.AsOf, input.MaxAge)
		if convErr != nil {
			out.Complete = false
			if strings.Contains(convErr.Error(), "missing") {
				out.MissingDependencies = append(out.MissingDependencies, convErr.Error())
			} else if strings.Contains(convErr.Error(), "stale") {
				out.StaleDependencies = append(out.StaleDependencies, convErr.Error())
			} else {
				out.InvalidDependencies = append(out.InvalidDependencies, convErr.Error())
			}
		} else {
			value := converted.String()
			lv.ReportingValue, lv.FXIDs = &value, fxIDs
			total = total.Add(converted)
			for _, id := range fxIDs {
				if !seenFX[id] {
					out.FXIDs = append(out.FXIDs, id)
					seenFX[id] = true
				}
			}
		}
		out.Lots = append(out.Lots, lv)
	}
	keys := make([]string, 0, len(subtotals))
	for key := range subtotals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, currency := range keys {
		st := subtotals[currency]
		out.Subtotals = append(out.Subtotals, CurrencySubtotal{Currency: currency, MarketValue: st.market.String(), CostBasis: st.cost.String(), UnrealisedPnL: st.pnl.String()})
	}
	seenTimestamps := map[string]bool{}
	for _, quote := range quotes {
		if seenQuote[quote.ID] {
			if !seenTimestamps[quote.MarketAt] {
				out.SourceTimestamps = append(out.SourceTimestamps, quote.MarketAt)
				seenTimestamps[quote.MarketAt] = true
			}
		}
	}
	for _, fx := range input.FXRates {
		if seenFX[fx.ID] {
			if !seenTimestamps[fx.MarketAt] {
				out.SourceTimestamps = append(out.SourceTimestamps, fx.MarketAt)
				seenTimestamps[fx.MarketAt] = true
			}
		}
	}
	sort.Strings(out.SourceTimestamps)
	out.PresentationTimezone = "Asia/Singapore"
	if out.Complete {
		out.ReportingTotal = total.String()
	}
	return out, nil
}

func stringPtr(value string) *string { return &value }

func latestQuotes(values []Quote) map[string]Quote {
	out := map[string]Quote{}
	for _, quote := range values {
		old, exists := out[quote.InstrumentID]
		if !exists || observationLater(quote.MarketAt, old.MarketAt) {
			out[quote.InstrumentID] = quote
		}
	}
	return out
}

func observationLater(candidate, current string) bool {
	candidateAt, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	currentAt, currentErr := time.Parse(time.RFC3339Nano, current)
	if candidateErr == nil && currentErr == nil {
		return candidateAt.After(currentAt)
	}
	return candidate > current
}

type fxEdge struct {
	to, id string
	rate   Decimal
}

// Convert finds one explicit direct/inverted FX path. Multiple possible paths
// are rejected rather than silently choosing a provider.
func Convert(value Decimal, from, to string, rates []FXRate, asOf time.Time, maxAge time.Duration) (Decimal, []string, error) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return value, nil, nil
	}
	graph := map[string][]fxEdge{}
	latest := map[string][]FXRate{}
	latestAt := map[string]string{}
	for _, fx := range rates {
		key := fx.BaseCurrency + "\x00" + fx.QuoteCurrency
		if previous, exists := latestAt[key]; exists {
			if observationLater(fx.MarketAt, previous) {
				latest[key] = []FXRate{fx}
				latestAt[key] = fx.MarketAt
			} else if fx.MarketAt == previous {
				latest[key] = append(latest[key], fx)
			}
		} else {
			latestAt[key] = fx.MarketAt
			latest[key] = []FXRate{fx}
		}
	}
	for _, observations := range latest {
		for _, fx := range observations {
			fresh := ClassifyFreshness(fx.MarketAt, asOf, maxAge)
			rate, err := ParseDecimal(fx.Rate)
			if err != nil || rate.Sign() <= 0 || fx.BaseCurrency == fx.QuoteCurrency {
				continue
			}
			if fresh.Status == Fresh {
				graph[fx.BaseCurrency] = append(graph[fx.BaseCurrency], fxEdge{to: fx.QuoteCurrency, id: fx.ID, rate: rate})
				inverse, err := MustInverse(rate)
				if err != nil {
					continue
				}
				graph[fx.QuoteCurrency] = append(graph[fx.QuoteCurrency], fxEdge{to: fx.BaseCurrency, id: fx.ID, rate: inverse})
			}
		}
	}
	type path struct {
		currencies []string
		ids        []string
		factor     Decimal
	}
	paths := []path{{currencies: []string{from}, factor: MustDecimal("1")}}
	var found []path
	for len(paths) > 0 {
		current := paths[0]
		paths = paths[1:]
		at := current.currencies[len(current.currencies)-1]
		if at == to {
			found = append(found, current)
			continue
		}
		if len(current.currencies) > len(graph)+1 {
			continue
		}
		for _, edge := range graph[at] {
			visited := false
			for _, c := range current.currencies {
				if c == edge.to {
					visited = true
					break
				}
			}
			if visited {
				continue
			}
			next := path{currencies: append(append([]string{}, current.currencies...), edge.to), ids: append(append([]string{}, current.ids...), edge.id), factor: current.factor.Mul(edge.rate)}
			paths = append(paths, next)
		}
	}
	if len(found) == 0 {
		for key, observations := range latest {
			parts := strings.Split(key, "\x00")
			if len(parts) != 2 || !((parts[0] == from && parts[1] == to) || (parts[0] == to && parts[1] == from)) {
				continue
			}
			for _, fx := range observations {
				fresh := ClassifyFreshness(fx.MarketAt, asOf, maxAge)
				switch fresh.Status {
				case Stale:
					return ZeroDecimal(), nil, fmt.Errorf("stale fx:%s", fx.ID)
				case Future, Invalid:
					return ZeroDecimal(), nil, fmt.Errorf("invalid fx:%s", fx.ID)
				}
			}
		}
		return ZeroDecimal(), nil, fmt.Errorf("missing fx:%s/%s", from, to)
	}
	if len(found) != 1 {
		return ZeroDecimal(), nil, fmt.Errorf("ambiguous fx:%s/%s", from, to)
	}
	converted := value.Mul(found[0].factor)
	return converted, found[0].ids, nil
}

func MustInverse(value Decimal) (Decimal, error) { return MustDecimal("1").Div(value, 18) }
