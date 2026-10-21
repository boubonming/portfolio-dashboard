export class APIError extends Error {
  status: number

  constructor(status: number, message = `Request failed (${status})`) {
    super(message)
    this.name = 'APIError'
    this.status = status
  }
}

export type APIRequestInit = RequestInit & { csrfToken?: string }

export async function apiFetch(input: RequestInfo | URL, init: APIRequestInit = {}): Promise<Response> {
  const { csrfToken, headers: suppliedHeaders, ...requestInit } = init
  const headers = new Headers(suppliedHeaders)
  if (!headers.has('Accept')) headers.set('Accept', 'application/json')
  const method = (requestInit.method || 'GET').toUpperCase()
  if (csrfToken && method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(input, { ...requestInit, headers, credentials: 'same-origin' })
  if (response.status === 401) throw new APIError(401, 'Your session has expired. Please sign in again.')
  return response
}

function hasDuplicateJSONKeys(raw: string): boolean {
  let index = 0
  const whitespace = () => { while (/\s/.test(raw[index] || '')) index += 1 }
  const stringToken = (): string | null => {
    if (raw[index] !== '"') return null
    const start = index
    index += 1
    while (index < raw.length) {
      const char = raw[index++]
      if (char === '\\') index += 1
      else if (char === '"') {
        try { return JSON.parse(raw.slice(start, index)) as string } catch { return null }
      }
    }
    return null
  }
  const value = (): boolean => {
    whitespace()
    if (raw[index] === '{') {
      index += 1
      whitespace()
      const keys = new Set<string>()
      if (raw[index] === '}') { index += 1; return false }
      while (index < raw.length) {
        whitespace()
        const key = stringToken()
        if (key === null) return false
        if (keys.has(key)) return true
        keys.add(key)
        whitespace()
        if (raw[index++] !== ':') return false
        if (value()) return true
        whitespace()
        if (raw[index] === '}') { index += 1; return false }
        if (raw[index++] !== ',') return false
      }
      return false
    }
    if (raw[index] === '[') {
      index += 1
      whitespace()
      if (raw[index] === ']') { index += 1; return false }
      while (index < raw.length) {
        if (value()) return true
        whitespace()
        if (raw[index] === ']') { index += 1; return false }
        if (raw[index++] !== ',') return false
      }
      return false
    }
    if (raw[index] === '"') return stringToken() === null
    const start = index
    while (index < raw.length && !/[\s,\]}]/.test(raw[index])) index += 1
    return index === start
  }
  const duplicate = value()
  whitespace()
  return duplicate || index !== raw.length
}

export async function jsonResponse<T>(response: Response): Promise<T> {
  if (!response.ok) throw new APIError(response.status)
  // Consume the complete body before decoding so malformed success payloads
  // cannot reach authority-bearing callers. Test doubles may only implement json.
  if (typeof response.text === 'function') {
    const raw = await response.text()
    try {
      if (hasDuplicateJSONKeys(raw)) throw new Error('duplicate or malformed JSON')
      return JSON.parse(raw) as T
    } catch {
      throw new APIError(200, 'Invalid JSON response.')
    }
  }
  return response.json() as Promise<T>
}

export type ProviderReadiness = { name: string; configured: boolean; public: boolean }
export type MarketMapping = { provider: string; provider_symbol: string; quote_currency: string; active_from: string; provenance?: string }
export type MarketInstrument = { instrument_id: string; symbol: string; description?: string; currency: string; mappings: MarketMapping[] }
export type MarketDataSetup = { portfolio_id: string; etag: string; providers: ProviderReadiness[]; instruments: MarketInstrument[] }
export type MappingBatchItem = { instrument_id: string; provider: string; provider_symbol: string; quote_currency: string; provenance: string; confirm_unverified: boolean }
export type ProviderRefreshStatus = {
  provider: string; status: string; request_count: number; succeeded_count: number; failed_count: number; missing_mapping_count: number
  observation_ids: string[]; errors: string[]
}
export type MarketSnapshotStatus = {
  snapshot_id: string; portfolio_id: string; calculation_version: string; created_at: string; as_of: string; complete: boolean
  reporting_currency: string; reporting_total?: string; currencies: string[]; subtotals: Array<{ currency: string; market_value: string; cost_basis?: string; unrealised_pnl?: string }>
  missing_count: number; stale_count: number; invalid_count: number; missing_dependencies: string[]; stale_dependencies: string[]; invalid_dependencies: string[]; quote_ids: string[]; fx_ids: string[]; source_timestamps: string[]; presentation_timezone: string
}
export type MarketRefreshResult = {
  refresh: { refresh_id: string; portfolio_id: string; status: string; started_at: string; ended_at: string; observation_ids: string[]; providers: ProviderRefreshStatus[] }
  snapshot: MarketSnapshotStatus
  complete: boolean
  freshness_policy: string
  missing_instruments: string[]
}

export type MappingBatchOutcome = { instrument_id: string; status: string; mapping?: { id: string; instrument_id: string; provider: string; provider_symbol: string; quote_currency: string; active_from: string; provenance: string; created_at: string }; error?: string }
export type MappingBatchResult = { status: string; etag: string; outcomes: MappingBatchOutcome[] }

type ObjectValue = Record<string, unknown>

function objectResponse(value: unknown, label: string): ObjectValue {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new APIError(200, `Invalid ${label} response.`)
  return value as ObjectValue
}

function exactKeys(value: ObjectValue, required: readonly string[], optional: readonly string[], label: string) {
  const allowed = new Set([...required, ...optional])
  for (const key of Object.keys(value)) if (!allowed.has(key)) throw new APIError(200, `Invalid ${label} response: unexpected ${key}.`)
  for (const key of required) if (!(key in value)) throw new APIError(200, `Invalid ${label} response: missing ${key}.`)
}

function stringField(value: unknown, field: string, label: string, max = 256): string {
  if (typeof value !== 'string' || value.length === 0 || value.length > max || /[\u0000-\u001f\u007f]/.test(value)) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return value
}

function identifier(value: unknown, field: string, label: string): string {
  const result = stringField(value, field, label, 128)
  if (!/^[A-Za-z0-9][A-Za-z0-9._~:/-]*$/.test(result)) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return result
}

function canonicalID(value: unknown, field: string, label: string): string {
  const result = stringField(value, field, label, 128)
  if (!/^[A-Za-z0-9][A-Za-z0-9._~-]*$/.test(result)) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return result
}

function currencyCode(value: unknown, field: string, label: string): string {
  const result = stringField(value, field, label, 3)
  if (!/^[A-Z]{3}$/.test(result)) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return result
}

function canonicalTime(value: unknown, field: string, label: string): string {
  const result = stringField(value, field, label, 40)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$/.test(result) || Number.isNaN(Date.parse(result))) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return result
}

function boundedInteger(value: unknown, field: string, label: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0 || value > 1000000) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return value
}

function decimal(value: unknown, field: string, label: string, allowNegative = false): string {
  const result = stringField(value, field, label, 80)
  const pattern = allowNegative ? /^-?(?:0|[1-9]\d*)(?:\.\d+)?$/ : /^(?:0|[1-9]\d*)(?:\.\d+)?$/
  if (!pattern.test(result)) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return result
}

function stringArray(value: unknown, field: string, label: string, max = 512): string[] {
  if (!Array.isArray(value) || value.length > max) throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return value.map((item, index) => stringField(item, `${field}[${index}]`, label, 256))
}

function uniqueStringArray(value: unknown, field: string, label: string, max = 512): string[] {
  const result = stringArray(value, field, label, max)
  if (new Set(result).size !== result.length) throw new APIError(200, `Invalid ${label} response: duplicate ${field}.`)
  return result
}

function decodeMapping(value: unknown, label: string): MarketMapping {
  const body = objectResponse(value, label)
  exactKeys(body, ['provider', 'provider_symbol', 'quote_currency', 'active_from'], ['provenance'], label)
  const provider = stringField(body.provider, 'provider', label, 64)
  if (!['alpha_vantage', 'coingecko', 'finnhub', 'open_exchange_rates'].includes(provider)) throw new APIError(200, `Invalid ${label} response: provider.`)
  return { provider, provider_symbol: identifier(body.provider_symbol, 'provider_symbol', label), quote_currency: currencyCode(body.quote_currency, 'quote_currency', label), active_from: canonicalTime(body.active_from, 'active_from', label), ...(body.provenance === undefined ? {} : { provenance: stringField(body.provenance, 'provenance', label) }) }
}

function decodeMarketSetup(value: unknown): MarketDataSetup {
  const body = objectResponse(value, 'market-data setup')
  exactKeys(body, ['portfolio_id', 'etag', 'providers', 'instruments'], [], 'market-data setup')
  const portfolioID = canonicalID(body.portfolio_id, 'portfolio_id', 'market-data setup')
  const etag = stringField(body.etag, 'etag', 'market-data setup', 256)
  if (!/^"[A-Za-z0-9._~-]{1,128}"$/.test(etag)) throw new APIError(200, 'Invalid market-data setup response: etag.')
  if (!Array.isArray(body.providers) || body.providers.length > 32) throw new APIError(200, 'Invalid market-data setup response: providers.')
  const providers = body.providers.map((raw, index) => {
    const provider = objectResponse(raw, `market-data setup provider ${index}`)
    exactKeys(provider, ['name', 'configured', 'public'], [], `market-data setup provider ${index}`)
    const name = stringField(provider.name, 'name', 'market-data setup provider', 64)
    if (!/^[a-z][a-z0-9_]{1,63}$/.test(name)) throw new APIError(200, 'Invalid market-data setup response: provider name.')
    return { name, configured: provider.configured === true || provider.configured === false ? provider.configured : (() => { throw new APIError(200, 'Invalid market-data setup response: configured.') })(), public: provider.public === true || provider.public === false ? provider.public : (() => { throw new APIError(200, 'Invalid market-data setup response: public.') })() }
  })
  if (new Set(providers.map((provider) => provider.name)).size !== providers.length) throw new APIError(200, 'Invalid market-data setup response: duplicate provider.')
  if (!Array.isArray(body.instruments) || body.instruments.length > 10000) throw new APIError(200, 'Invalid market-data setup response: instruments.')
  const seen = new Set<string>()
  const instruments = body.instruments.map((raw, index) => {
    const instrument = objectResponse(raw, `market-data instrument ${index}`)
    exactKeys(instrument, ['instrument_id', 'symbol', 'currency', 'mappings'], ['description'], `market-data instrument ${index}`)
    const id = canonicalID(instrument.instrument_id, 'instrument_id', 'market-data instrument')
    if (seen.has(id)) throw new APIError(200, 'Invalid market-data setup response: duplicate instrument.')
    seen.add(id)
    if (!Array.isArray(instrument.mappings) || instrument.mappings.length > 32) throw new APIError(200, 'Invalid market-data setup response: mappings.')
    const currency = currencyCode(instrument.currency, 'currency', 'market-data instrument')
    const mappings = instrument.mappings.map((mapping) => decodeMapping(mapping, 'market-data mapping'))
    if (new Set(mappings.map((mapping) => mapping.provider)).size !== mappings.length || mappings.some((mapping) => mapping.quote_currency !== currency)) throw new APIError(200, 'Invalid market-data setup response: contradictory mapping.')
    return { instrument_id: id, symbol: stringField(instrument.symbol, 'symbol', 'market-data instrument', 128), ...(instrument.description === undefined ? {} : { description: stringField(instrument.description, 'description', 'market-data instrument') }), currency, mappings }
  })
  return { portfolio_id: portfolioID, etag, providers, instruments }
}

function decodeMappingResult(value: unknown): MappingBatchResult {
  const body = objectResponse(value, 'mapping batch')
  exactKeys(body, ['status', 'etag', 'outcomes'], [], 'mapping batch')
  if (!Array.isArray(body.outcomes) || body.outcomes.length > 256) throw new APIError(200, 'Invalid mapping batch response: outcomes.')
  const seen = new Set<string>()
  const outcomes = body.outcomes.map((raw, index) => {
    const outcome = objectResponse(raw, `mapping outcome ${index}`)
    exactKeys(outcome, ['instrument_id', 'status'], ['mapping', 'error'], `mapping outcome ${index}`)
    const instrumentID = identifier(outcome.instrument_id, 'instrument_id', 'mapping outcome')
    if (seen.has(instrumentID)) throw new APIError(200, 'Invalid mapping batch response: duplicate outcome.')
    seen.add(instrumentID)
    if (outcome.error !== undefined && typeof outcome.error !== 'string') throw new APIError(200, 'Invalid mapping batch response: error.')
    let mapping: MappingBatchOutcome['mapping']
    if (outcome.mapping !== undefined) {
      const rawMapping = objectResponse(outcome.mapping, 'mapping outcome mapping')
      exactKeys(rawMapping, ['id', 'instrument_id', 'provider', 'provider_symbol', 'quote_currency', 'active_from', 'provenance', 'created_at'], [], 'mapping outcome mapping')
      mapping = { id: canonicalID(rawMapping.id, 'id', 'mapping outcome mapping'), instrument_id: canonicalID(rawMapping.instrument_id, 'instrument_id', 'mapping outcome mapping'), provider: stringField(rawMapping.provider, 'provider', 'mapping outcome mapping', 64), provider_symbol: identifier(rawMapping.provider_symbol, 'provider_symbol', 'mapping outcome mapping'), quote_currency: currencyCode(rawMapping.quote_currency, 'quote_currency', 'mapping outcome mapping'), active_from: canonicalTime(rawMapping.active_from, 'active_from', 'mapping outcome mapping'), provenance: stringField(rawMapping.provenance, 'provenance', 'mapping outcome mapping'), created_at: canonicalTime(rawMapping.created_at, 'created_at', 'mapping outcome mapping') }
      if (mapping.instrument_id !== instrumentID) throw new APIError(200, 'Invalid mapping batch response: contradictory mapping identity.')
    }
    const status = stringField(outcome.status, 'status', 'mapping outcome', 64)
    if (status !== 'saved' || !mapping || outcome.error !== undefined) throw new APIError(200, 'Invalid mapping batch response: incomplete outcome.')
    return { instrument_id: instrumentID, status, mapping }
  })
  const status = stringField(body.status, 'status', 'mapping batch', 64)
  if (status !== 'saved' || outcomes.length === 0) throw new APIError(200, 'Invalid mapping batch response: incomplete result.')
  const etag = stringField(body.etag, 'etag', 'mapping batch', 256)
  if (!/^"[A-Za-z0-9._~-]{1,128}"$/.test(etag)) throw new APIError(200, 'Invalid mapping batch response: etag.')
  return { status, etag, outcomes }
}

function decodeRefreshResult(value: unknown, portfolioID: string, reportingCurrency: string, setup?: MarketDataSetup): MarketRefreshResult {
  const body = objectResponse(value, 'market refresh')
  exactKeys(body, ['refresh', 'snapshot', 'complete', 'freshness_policy'], ['missing_instruments'], 'market refresh')
  const refresh = objectResponse(body.refresh, 'market refresh')
  exactKeys(refresh, ['refresh_id', 'portfolio_id', 'status', 'started_at', 'ended_at', 'providers'], ['observation_ids'], 'market refresh')
  const snapshot = objectResponse(body.snapshot, 'market refresh snapshot')
  exactKeys(snapshot, ['snapshot_id', 'portfolio_id', 'calculation_version', 'created_at', 'as_of', 'complete', 'reporting_currency', 'currencies', 'subtotals', 'missing_count', 'stale_count', 'invalid_count', 'missing_dependencies', 'stale_dependencies', 'invalid_dependencies', 'quote_ids', 'fx_ids', 'source_timestamps', 'presentation_timezone'], ['reporting_total'], 'market refresh snapshot')
  if (body.complete !== true && body.complete !== false || snapshot.complete !== true && snapshot.complete !== false) throw new APIError(200, 'Invalid market refresh response: complete.')
  if (refresh.portfolio_id !== portfolioID || snapshot.portfolio_id !== portfolioID || snapshot.reporting_currency !== reportingCurrency.toUpperCase()) throw new APIError(200, 'Invalid market refresh response: stale portfolio scope.')
  const providers = Array.isArray(refresh.providers) ? refresh.providers.map((raw, index) => {
    const provider = objectResponse(raw, `market refresh provider ${index}`)
    exactKeys(provider, ['provider', 'status', 'request_count', 'succeeded_count', 'failed_count', 'missing_mapping_count'], ['observation_ids', 'errors'], `market refresh provider ${index}`)
    const providerName = stringField(provider.provider, 'provider', 'market refresh provider', 64)
    if (!/^[a-z][a-z0-9_]{1,63}$/.test(providerName)) throw new APIError(200, 'Invalid market refresh response: provider name.')
    const status = stringField(provider.status, 'status', 'market refresh provider', 64)
    if (status !== 'complete' && status !== 'incomplete') throw new APIError(200, 'Invalid market refresh response: provider status.')
    const observationIDs = provider.observation_ids === undefined ? [] : stringArray(provider.observation_ids, 'observation_ids', 'market refresh provider')
    if (new Set(observationIDs).size !== observationIDs.length) throw new APIError(200, 'Invalid market refresh response: duplicate provider observation.')
    return { provider: providerName, status, request_count: boundedInteger(provider.request_count, 'request_count', 'market refresh provider'), succeeded_count: boundedInteger(provider.succeeded_count, 'succeeded_count', 'market refresh provider'), failed_count: boundedInteger(provider.failed_count, 'failed_count', 'market refresh provider'), missing_mapping_count: boundedInteger(provider.missing_mapping_count, 'missing_mapping_count', 'market refresh provider'), observation_ids: observationIDs, errors: provider.errors === undefined ? [] : stringArray(provider.errors, 'errors', 'market refresh provider') }
  }) : (() => { throw new APIError(200, 'Invalid market refresh response: providers.') })()
  if (new Set(providers.map((provider) => provider.provider)).size !== providers.length) throw new APIError(200, 'Invalid market refresh response: duplicate provider.')
  if (providers.some((provider) => provider.status === 'complete' && provider.errors.length > 0)) throw new APIError(200, 'Invalid market refresh response: complete provider has errors.')
  const observationIDs = refresh.observation_ids === undefined ? [] : stringArray(refresh.observation_ids, 'observation_ids', 'market refresh')
  if (new Set(observationIDs).size !== observationIDs.length) throw new APIError(200, 'Invalid market refresh response: duplicate observation.')
  const nestedObservationIDs = providers.flatMap((provider) => provider.observation_ids)
  if (new Set(nestedObservationIDs).size !== nestedObservationIDs.length || nestedObservationIDs.length !== observationIDs.length || nestedObservationIDs.some((id) => !observationIDs.includes(id))) throw new APIError(200, 'Invalid market refresh response: contradictory observation identity.')
  const currencies = stringArray(snapshot.currencies, 'currencies', 'market refresh snapshot', 32)
  if (new Set(currencies).size !== currencies.length) throw new APIError(200, 'Invalid market refresh response: contradictory currencies.')
  const missingDependencies = stringArray(snapshot.missing_dependencies, 'missing_dependencies', 'market refresh snapshot')
  const staleDependencies = stringArray(snapshot.stale_dependencies, 'stale_dependencies', 'market refresh snapshot')
  const invalidDependencies = stringArray(snapshot.invalid_dependencies, 'invalid_dependencies', 'market refresh snapshot')
  if (new Set([...missingDependencies, ...staleDependencies, ...invalidDependencies]).size !== missingDependencies.length + staleDependencies.length + invalidDependencies.length) throw new APIError(200, 'Invalid market refresh response: duplicate dependency.')
  if (snapshot.missing_count !== missingDependencies.length || snapshot.stale_count !== staleDependencies.length || snapshot.invalid_count !== invalidDependencies.length) throw new APIError(200, 'Invalid market refresh response: contradictory dependency counts.')
  if (snapshot.complete === true && snapshot.reporting_total === undefined || snapshot.complete === false && snapshot.reporting_total !== undefined) throw new APIError(200, 'Invalid market refresh response: contradictory total.')
  uniqueStringArray(snapshot.quote_ids, 'quote_ids', 'market refresh snapshot')
  uniqueStringArray(snapshot.fx_ids, 'fx_ids', 'market refresh snapshot')
  const snapshotResult: MarketSnapshotStatus = { snapshot_id: canonicalID(snapshot.snapshot_id, 'snapshot_id', 'market refresh snapshot'), portfolio_id: portfolioID, calculation_version: stringField(snapshot.calculation_version, 'calculation_version', 'market refresh snapshot', 128), created_at: canonicalTime(snapshot.created_at, 'created_at', 'market refresh snapshot'), as_of: canonicalTime(snapshot.as_of, 'as_of', 'market refresh snapshot'), complete: snapshot.complete as boolean, reporting_currency: currencyCode(snapshot.reporting_currency, 'reporting_currency', 'market refresh snapshot'), ...(snapshot.reporting_total === undefined ? {} : { reporting_total: decimal(snapshot.reporting_total, 'reporting_total', 'market refresh snapshot') }), currencies: stringArray(snapshot.currencies, 'currencies', 'market refresh snapshot', 32), subtotals: Array.isArray(snapshot.subtotals) ? snapshot.subtotals.map((raw, index) => { const subtotal = objectResponse(raw, `snapshot subtotal ${index}`); exactKeys(subtotal, ['currency', 'market_value'], ['cost_basis', 'unrealised_pnl'], `snapshot subtotal ${index}`); return { currency: currencyCode(subtotal.currency, 'currency', 'snapshot subtotal'), market_value: decimal(subtotal.market_value, 'market_value', 'snapshot subtotal'), ...(subtotal.cost_basis === undefined ? {} : { cost_basis: decimal(subtotal.cost_basis, 'cost_basis', 'snapshot subtotal') }), ...(subtotal.unrealised_pnl === undefined ? {} : { unrealised_pnl: decimal(subtotal.unrealised_pnl, 'unrealised_pnl', 'snapshot subtotal', true) }) } }) : (() => { throw new APIError(200, 'Invalid market refresh response: subtotals.') })(), missing_count: boundedInteger(snapshot.missing_count, 'missing_count', 'market refresh snapshot'), stale_count: boundedInteger(snapshot.stale_count, 'stale_count', 'market refresh snapshot'), invalid_count: boundedInteger(snapshot.invalid_count, 'invalid_count', 'market refresh snapshot'), missing_dependencies: stringArray(snapshot.missing_dependencies, 'missing_dependencies', 'market refresh snapshot'), stale_dependencies: stringArray(snapshot.stale_dependencies, 'stale_dependencies', 'market refresh snapshot'), invalid_dependencies: stringArray(snapshot.invalid_dependencies, 'invalid_dependencies', 'market refresh snapshot'), quote_ids: stringArray(snapshot.quote_ids, 'quote_ids', 'market refresh snapshot'), fx_ids: stringArray(snapshot.fx_ids, 'fx_ids', 'market refresh snapshot'), source_timestamps: stringArray(snapshot.source_timestamps, 'source_timestamps', 'market refresh snapshot'), presentation_timezone: stringField(snapshot.presentation_timezone, 'presentation_timezone', 'market refresh snapshot', 64) }
  const refreshStatus = stringField(refresh.status, 'status', 'market refresh', 64)
  if (refreshStatus !== 'complete' && refreshStatus !== 'incomplete') throw new APIError(200, 'Invalid market refresh response: refresh status.')
  const result: MarketRefreshResult = { refresh: { refresh_id: canonicalID(refresh.refresh_id, 'refresh_id', 'market refresh'), portfolio_id: portfolioID, status: refreshStatus, started_at: canonicalTime(refresh.started_at, 'started_at', 'market refresh'), ended_at: canonicalTime(refresh.ended_at, 'ended_at', 'market refresh'), observation_ids: observationIDs, providers }, snapshot: snapshotResult, complete: body.complete as boolean, freshness_policy: stringField(body.freshness_policy, 'freshness_policy', 'market refresh', 512), missing_instruments: body.missing_instruments === undefined ? [] : stringArray(body.missing_instruments, 'missing_instruments', 'market refresh') }
  if (result.complete !== (result.refresh.status === 'complete' && result.snapshot.complete)) throw new APIError(200, 'Invalid market refresh response: contradictory completion state.')
  validateRefreshSemantics(result, setup)
  return result
}

function validateRefreshSemantics(result: MarketRefreshResult, setup?: MarketDataSetup): void {
  const refreshTimes = [
    ['started_at', result.refresh.started_at],
    ['ended_at', result.refresh.ended_at],
    ['snapshot.created_at', result.snapshot.created_at],
    ['snapshot.as_of', result.snapshot.as_of],
    ...result.snapshot.source_timestamps.map((value, index) => [`source_timestamps[${index}]`, value] as const),
  ] as const
  const parsedTimes = refreshTimes.map(([field, value]) => [field, Date.parse(value)] as const)
  if (parsedTimes.some(([, value]) => Number.isNaN(value))) throw new APIError(200, 'Invalid market refresh response: timestamp.')
  const now = Date.now() + 5000
  const future = parsedTimes.find(([, value]) => value > now)
  if (future) throw new APIError(200, `Future market refresh response timestamp: ${future[0]}.`)
  if (Date.parse(result.refresh.started_at) > Date.parse(result.refresh.ended_at)) throw new APIError(200, 'Invalid market refresh response: timestamp ordering.')

  const allObservationIDs = result.refresh.providers.flatMap((provider) => provider.observation_ids)
  for (const provider of result.refresh.providers) {
    if (provider.succeeded_count + provider.failed_count !== provider.request_count) throw new APIError(200, `Invalid market refresh response: ${provider.provider} counters.`)
    if (provider.observation_ids.length !== provider.succeeded_count) throw new APIError(200, `Invalid market refresh response: ${provider.provider} observations.`)
    const shouldBeComplete = provider.failed_count === 0 && provider.missing_mapping_count === 0 && provider.errors.length === 0 && provider.succeeded_count === provider.request_count
    if ((provider.status === 'complete') !== shouldBeComplete) throw new APIError(200, `Invalid market refresh response: ${provider.provider} status.`)
    if (provider.failed_count > 0 && provider.errors.length === 0) throw new APIError(200, `Invalid market refresh response: ${provider.provider} errors.`)
  }
  if (new Set(allObservationIDs).size !== allObservationIDs.length || new Set(allObservationIDs).size !== result.refresh.observation_ids.length) throw new APIError(200, 'Invalid market refresh response: observation identity.')
  if (result.refresh.status === 'complete' && result.refresh.providers.some((provider) => provider.status !== 'complete')) throw new APIError(200, 'Invalid market refresh response: refresh status.')
  if (result.refresh.status === 'incomplete' && result.refresh.providers.every((provider) => provider.status === 'complete')) throw new APIError(200, 'Invalid market refresh response: refresh status.')

  const currencies = new Set(result.snapshot.currencies)
  const subtotalCurrencies = new Set(result.snapshot.subtotals.map((subtotal) => subtotal.currency))
  if (currencies.size !== result.snapshot.currencies.length || currencies.size !== subtotalCurrencies.size || [...currencies].some((currency) => !subtotalCurrencies.has(currency))) throw new APIError(200, 'Invalid market refresh response: contradictory original-currency subtotals.')
  if (setup) {
    const instrumentIDs = new Set(setup.instruments.map((instrument) => instrument.instrument_id))
    const instrumentCurrencies = new Set(setup.instruments.map((instrument) => instrument.currency))
    const expectedFXCurrencies = new Set([...instrumentCurrencies].filter((currency) => currency !== result.snapshot.reporting_currency))
    const providerNames = new Set(setup.providers.map((provider) => provider.name))
    if (result.refresh.providers.some((provider) => !providerNames.has(provider.provider))) throw new APIError(200, 'Invalid market refresh response: unknown provider.')
    const missingInstrumentLabels = new Set(setup.instruments.map((instrument) => `${instrument.symbol} (${instrument.currency})`))
    if (new Set(result.missing_instruments).size !== result.missing_instruments.length || result.missing_instruments.some((instrument) => !missingInstrumentLabels.has(instrument))) throw new APIError(200, 'Invalid market refresh response: unknown missing instrument.')
    for (const dependency of [...result.snapshot.missing_dependencies, ...result.snapshot.stale_dependencies, ...result.snapshot.invalid_dependencies]) {
      const normalized = dependency.replace(/^(?:missing|stale|invalid)\s+/i, '')
      const quote = normalized.match(/^quote:([A-Za-z0-9._~-]+)$/)
      const fx = normalized.match(/^fx:([A-Z]{3})\/([A-Z]{3})$/)
      if (quote && !instrumentIDs.has(quote[1])) throw new APIError(200, 'Invalid market refresh response: unknown dependency identity.')
      if (fx && (!expectedFXCurrencies.has(fx[1]) || fx[2] !== result.snapshot.reporting_currency)) throw new APIError(200, 'Invalid market refresh response: unknown dependency currency.')
      if (!quote && !fx && !/^(?:lot|cost):[A-Za-z0-9._~-]+(?::[A-Za-z0-9._~-]+)?$/.test(normalized)) throw new APIError(200, 'Invalid market refresh response: unknown dependency identity.')
    }
    if (result.complete && result.missing_instruments.length !== 0) throw new APIError(200, 'Invalid market refresh response: complete result has missing instruments.')
    if (result.complete && result.snapshot.quote_ids.length !== setup.instruments.length) throw new APIError(200, 'Invalid market refresh response: incomplete quote dependencies.')
    if (result.complete && result.snapshot.fx_ids.length !== expectedFXCurrencies.size) throw new APIError(200, 'Invalid market refresh response: incomplete FX dependencies.')
    for (const subtotal of result.snapshot.subtotals) if (!instrumentCurrencies.has(subtotal.currency)) throw new APIError(200, 'Invalid market refresh response: unknown subtotal currency.')
  }
  if (result.complete) {
    if (result.snapshot.missing_count !== 0 || result.snapshot.stale_count !== 0 || result.snapshot.invalid_count !== 0 || result.snapshot.missing_dependencies.length !== 0 || result.snapshot.stale_dependencies.length !== 0 || result.snapshot.invalid_dependencies.length !== 0 || result.snapshot.reporting_total === undefined || result.snapshot.source_timestamps.length !== result.snapshot.quote_ids.length + result.snapshot.fx_ids.length) throw new APIError(200, 'Invalid market refresh response: incomplete result.')
  }
}

export async function getMarketDataSetup(portfolioID: string, signal?: AbortSignal): Promise<MarketDataSetup> {
  const result = decodeMarketSetup(await jsonResponse<unknown>(await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/market-data`, { signal })))
  if (result.portfolio_id !== portfolioID) throw new APIError(200, 'Invalid market-data setup response: stale portfolio scope.')
  return result
}

export async function saveMarketMappings(portfolioID: string, etag: string, mappings: MappingBatchItem[], csrfToken?: string, signal?: AbortSignal): Promise<MappingBatchResult> {
  const response = await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/market-data/mappings`, {
    method: 'POST', csrfToken, signal, headers: { 'Content-Type': 'application/json', 'If-Match': etag }, body: JSON.stringify({ mappings }),
  })
  return decodeMappingResult(await jsonResponse<unknown>(response))
}

export async function refreshMarketData(portfolioID: string, reportingCurrency: string, idempotencyKey: string, csrfToken?: string, signal?: AbortSignal, setup?: MarketDataSetup): Promise<MarketRefreshResult> {
  const response = await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/market-data/refresh`, {
    method: 'POST', csrfToken, signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ reporting_currency: reportingCurrency }),
  })
  return decodeRefreshResult(await jsonResponse<unknown>(response), portfolioID, reportingCurrency, setup)
}
