import { describe, expect, it } from 'vitest'
import type { MarketInstrument, ProviderReadiness } from './api'
import { prefillMappingSuggestion } from './marketMappingSuggestions'

const ready: ProviderReadiness[] = [
  { name: 'coingecko', configured: true, public: true },
  { name: 'finnhub', configured: true, public: false },
  { name: 'alpha_vantage', configured: true, public: false },
]

function instrument(overrides: Partial<MarketInstrument> = {}): MarketInstrument {
  return { instrument_id: 'internal-only', symbol: 'ABC', description: 'Example', currency: 'USD', mappings: [], ...overrides }
}

describe('prefillMappingSuggestion', () => {
  it.each([
    ['BTC', 'bitcoin'],
    ['ETH', 'ethereum'],
    ['XRP', 'ripple'],
  ])('uses the exact CoinGecko alias for %s', (symbol, providerSymbol) => {
    const result = prefillMappingSuggestion(instrument({ symbol }), ready)
    expect(result).toMatchObject({ provider: 'coingecko', providerSymbol, quoteCurrency: 'USD', confirmed: false, suggested: true })
    expect(result.guidance).toMatch(/Suggested — verify the exact exchange\/listing before confirming/)
    expect(result.provenance).toContain('CoinGecko')
  })

  it('uses Finnhub and the visible holding symbol for an available USD candidate', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: 'MSFT' }), ready)
    expect(result).toMatchObject({ provider: 'finnhub', providerSymbol: 'MSFT', confirmed: false, suggested: true })
    expect(result.provenance).toMatch(/no exchange suffix, ADR, or local-listing identity was inferred/)
  })

  it('uses Alpha Vantage and the visible holding symbol for an available non-USD candidate', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: '7203', currency: 'JPY' }), ready)
    expect(result).toMatchObject({ provider: 'alpha_vantage', providerSymbol: '7203', quoteCurrency: 'JPY', confirmed: false, suggested: true })
  })

  it('leaves an unavailable provider and identifier blank with an explanation', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: 'MSFT' }), [{ name: 'finnhub', configured: false, public: false }])
    expect(result).toMatchObject({ provider: '', providerSymbol: '', confirmed: false, suggested: true })
    expect(result.provenance).toMatch(/Finnhub is not configured or available/)
  })

  it('does not fall back from an unavailable CoinGecko crypto alias', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: 'BTC' }), [{ name: 'finnhub', configured: true, public: false }])
    expect(result).toMatchObject({ provider: '', providerSymbol: '', confirmed: false, suggested: true })
    expect(result.guidance).toMatch(/CoinGecko is not configured or available/)
  })

  it('always gives a committed mapping precedence and marks it confirmed', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: 'BTC', mappings: [{ provider: 'finnhub', provider_symbol: 'NASDAQ:BTC', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'Owner verified listing' }] }), ready)
    expect(result).toMatchObject({ provider: 'finnhub', providerSymbol: 'NASDAQ:BTC', quoteCurrency: 'USD', provenance: 'Owner verified listing', confirmed: true, suggested: false })
    expect(result.guidance).toMatch(/Current \/ confirmed mapping/)
  })

  it('keeps duplicate symbols independent because each call uses its own instrument data', () => {
    const first = prefillMappingSuggestion(instrument({ instrument_id: 'first', symbol: 'SAME', description: 'First company' }), ready)
    const second = prefillMappingSuggestion(instrument({ instrument_id: 'second', symbol: 'SAME', description: 'Second company' }), ready)
    expect(first.providerSymbol).toBe('SAME')
    expect(second.providerSymbol).toBe('SAME')
    expect(first.guidance).toEqual(second.guidance)
    expect(first).not.toBe(second)
  })
})
