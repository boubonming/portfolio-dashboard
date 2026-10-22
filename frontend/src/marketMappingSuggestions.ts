import type { MarketInstrument, MarketMapping, ProviderReadiness } from './api'

export type MappingDraftSeed = {
  provider: string
  providerSymbol: string
  quoteCurrency: string
  provenance: string
  confirmed: boolean
  suggested: boolean
  guidance: string
}

function committedSeed(mapping: MarketMapping): MappingDraftSeed {
  return {
    provider: mapping.provider,
    providerSymbol: mapping.provider_symbol,
    quoteCurrency: mapping.quote_currency,
    provenance: mapping.provenance || 'Committed mapping; exact provider listing was previously confirmed.',
    confirmed: true,
    suggested: false,
    guidance: 'Current / confirmed mapping. This committed mapping takes precedence over discovery.',
  }
}

// Discovery is deliberately server-backed. This function only seeds existing
// authoritative mappings; it never copies a holding symbol or invents a
// provider identifier.
export function prefillMappingSuggestion(instrument: MarketInstrument, _providers: ProviderReadiness[]): MappingDraftSeed {
  const committed = instrument.mappings[0]
  if (committed) return committedSeed(committed)
  return {
    provider: '',
    providerSymbol: '',
    quoteCurrency: instrument.currency,
    provenance: '',
    confirmed: false,
    suggested: false,
    guidance: 'Searching configured providers for an exact candidate…',
  }
}
