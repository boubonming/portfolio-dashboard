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

const cryptoAliases: Record<string, string> = {
  BTC: 'bitcoin',
  ETH: 'ethereum',
  XRP: 'ripple',
}

function providerAvailable(providers: ProviderReadiness[], name: string): boolean {
  const provider = providers.find((candidate) => candidate.name === name)
  return Boolean(provider?.configured || provider?.public)
}

function unavailableGuidance(provider: string): string {
  return `No suggestion — ${provider} is not configured or available. Enter and verify an exact provider identifier before confirming.`
}

function suggestionGuidance(provider: string, providerSymbol: string, basis: string): string {
  return `Suggested — verify the exact exchange/listing before confirming. ${basis} Candidate provider identifier: ${providerSymbol} (${provider}); no exchange suffix, ADR, or local-listing identity was inferred.`
}

function committedSeed(instrument: MarketInstrument, mapping: MarketMapping): MappingDraftSeed {
  return {
    provider: mapping.provider,
    providerSymbol: mapping.provider_symbol,
    quoteCurrency: mapping.quote_currency,
    provenance: mapping.provenance || 'Committed mapping; exact provider listing was previously confirmed.',
    confirmed: true,
    suggested: false,
    guidance: 'Current / confirmed mapping. This committed mapping takes precedence over any suggestion.',
  }
}

export function prefillMappingSuggestion(instrument: MarketInstrument, providers: ProviderReadiness[]): MappingDraftSeed {
  const committed = instrument.mappings[0]
  if (committed) return committedSeed(instrument, committed)

  const symbol = instrument.symbol.trim()
  const alias = cryptoAliases[symbol.toUpperCase()]
  if (alias) {
    if (!providerAvailable(providers, 'coingecko')) {
      return { provider: '', providerSymbol: '', quoteCurrency: instrument.currency, provenance: unavailableGuidance('CoinGecko'), confirmed: false, suggested: true, guidance: unavailableGuidance('CoinGecko') }
    }
    return {
      provider: 'coingecko',
      providerSymbol: alias,
      quoteCurrency: instrument.currency,
      provenance: suggestionGuidance('CoinGecko', alias, `The exact crypto alias ${symbol} has a conservative CoinGecko ID candidate.`),
      confirmed: false,
      suggested: true,
      guidance: suggestionGuidance('CoinGecko', alias, `The exact crypto alias ${symbol} has a conservative CoinGecko ID candidate.`),
    }
  }

  const provider = instrument.currency === 'USD' ? 'finnhub' : 'alpha_vantage'
  const providerLabel = provider === 'finnhub' ? 'Finnhub' : 'Alpha Vantage'
  if (!providerAvailable(providers, provider)) {
    return { provider: '', providerSymbol: '', quoteCurrency: instrument.currency, provenance: unavailableGuidance(providerLabel), confirmed: false, suggested: true, guidance: unavailableGuidance(providerLabel) }
  }
  const basis = instrument.currency === 'USD' ? `The instrument appears USD-listed, so its holding symbol ${symbol} is offered as an unverified candidate for the configured Finnhub provider.` : `The non-USD/international instrument uses its holding symbol ${symbol} as an unverified candidate for the configured Alpha Vantage provider.`
  return {
    provider,
    providerSymbol: symbol,
    quoteCurrency: instrument.currency,
    provenance: suggestionGuidance(providerLabel, symbol, basis),
    confirmed: false,
    suggested: true,
    guidance: suggestionGuidance(providerLabel, symbol, basis),
  }
}
