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
  it('does not copy symbols or invent provider identifiers for unmapped instruments', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: 'BTC' }), ready)
    expect(result).toMatchObject({ provider: '', providerSymbol: '', quoteCurrency: 'USD', confirmed: false, suggested: false })
    expect(result.provenance).toBe('')
    expect(result.guidance).toMatch(/Searching configured providers/)
  })

  it('always gives a committed mapping precedence and marks it confirmed', () => {
    const result = prefillMappingSuggestion(instrument({ symbol: 'BTC', mappings: [{ provider: 'finnhub', provider_symbol: 'NASDAQ:BTC', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'Owner verified listing' }] }), ready)
    expect(result).toMatchObject({ provider: 'finnhub', providerSymbol: 'NASDAQ:BTC', quoteCurrency: 'USD', provenance: 'Owner verified listing', confirmed: true, suggested: false })
    expect(result.guidance).toMatch(/Current \/ confirmed mapping/)
  })

  it('keeps duplicate symbols independent and blank until provider discovery returns metadata', () => {
    const first = prefillMappingSuggestion(instrument({ instrument_id: 'first', symbol: 'SAME', description: 'First company' }), ready)
    const second = prefillMappingSuggestion(instrument({ instrument_id: 'second', symbol: 'SAME', description: 'Second company' }), ready)
    expect(first.providerSymbol).toBe('')
    expect(second.providerSymbol).toBe('')
    expect(first.guidance).toEqual(second.guidance)
    expect(first).not.toBe(second)
  })
})
