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

export async function jsonResponse<T>(response: Response): Promise<T> {
  if (!response.ok) throw new APIError(response.status)
  return response.json() as Promise<T>
}

export type ProviderReadiness = { name: string; configured: boolean; public?: boolean }
export type MarketMapping = { provider: string; provider_symbol: string; quote_currency: string; active_from: string; provenance?: string }
export type MarketInstrument = { instrument_id: string; symbol: string; description?: string; currency: string; mappings: MarketMapping[] }
export type MarketDataSetup = { portfolio_id: string; etag: string; providers: ProviderReadiness[]; instruments: MarketInstrument[] }
export type MappingBatchItem = { instrument_id: string; provider: string; provider_symbol: string; quote_currency: string; provenance: string; confirm_unverified: boolean }
export type MarketRefreshResult = {
  refresh: { status: string; providers: Array<{ provider: string; status: string; request_count: number; succeeded_count: number; failed_count: number; missing_mapping_count: number; errors?: string[] }> }
  snapshot: { complete: boolean; reporting_total?: string; missing_count: number; stale_count: number; invalid_count: number }
  complete: boolean
  freshness_policy: string
  missing_instruments?: string[]
}

export type MappingBatchResult = { status: string; etag: string; outcomes: Array<Record<string, unknown>> }

function objectResponse(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new APIError(200, `Invalid ${label} response.`)
  return value as Record<string, unknown>
}

function stringField(value: unknown, field: string, label: string): string {
  if (typeof value !== 'string' || value === '') throw new APIError(200, `Invalid ${label} response: ${field}.`)
  return value
}

function decodeMarketSetup(value: unknown): MarketDataSetup {
  const body = objectResponse(value, 'market-data setup')
  if (!Array.isArray(body.providers) || !Array.isArray(body.instruments)) throw new APIError(200, 'Invalid market-data setup response.')
  stringField(body.portfolio_id, 'portfolio_id', 'market-data setup')
  stringField(body.etag, 'etag', 'market-data setup')
  return body as unknown as MarketDataSetup
}

function decodeMappingResult(value: unknown): MappingBatchResult {
  const body = objectResponse(value, 'mapping batch')
  if (typeof body.status !== 'string' || typeof body.etag !== 'string' || !Array.isArray(body.outcomes)) throw new APIError(200, 'Invalid mapping batch response.')
  return { status: body.status, etag: body.etag, outcomes: body.outcomes.filter((item): item is Record<string, unknown> => item !== null && typeof item === 'object' && !Array.isArray(item)) }
}

function decodeRefreshResult(value: unknown): MarketRefreshResult {
  const body = objectResponse(value, 'market refresh')
  const refresh = objectResponse(body.refresh, 'market refresh')
  const snapshot = objectResponse(body.snapshot, 'market refresh')
  if (typeof body.complete !== 'boolean' || typeof body.freshness_policy !== 'string' || typeof refresh.status !== 'string' || !Array.isArray(refresh.providers) || typeof snapshot.complete !== 'boolean') throw new APIError(200, 'Invalid market refresh response.')
  if (body.missing_instruments !== undefined && !Array.isArray(body.missing_instruments)) throw new APIError(200, 'Invalid market refresh response.')
  return body as unknown as MarketRefreshResult
}

export async function getMarketDataSetup(portfolioID: string, signal?: AbortSignal): Promise<MarketDataSetup> {
  return decodeMarketSetup(await jsonResponse<unknown>(await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/market-data`, { signal })))
}

export async function saveMarketMappings(portfolioID: string, etag: string, mappings: MappingBatchItem[], csrfToken?: string, signal?: AbortSignal): Promise<MappingBatchResult> {
  const response = await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/market-data/mappings`, {
    method: 'POST', csrfToken, signal, headers: { 'Content-Type': 'application/json', 'If-Match': etag }, body: JSON.stringify({ mappings }),
  })
  return decodeMappingResult(await jsonResponse<unknown>(response))
}

export async function refreshMarketData(portfolioID: string, reportingCurrency: string, idempotencyKey: string, csrfToken?: string, signal?: AbortSignal): Promise<MarketRefreshResult> {
  const response = await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/market-data/refresh`, {
    method: 'POST', csrfToken, signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ reporting_currency: reportingCurrency }),
  })
  return decodeRefreshResult(await jsonResponse<unknown>(response))
}
