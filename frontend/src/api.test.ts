import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError, apiFetch, getMarketDataSetup, getMarketRefreshSchedule, refreshMarketData, saveMarketRefreshSchedule, type MarketDataSetup } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('typed API helper', () => {
  it('keeps CSRF state out of storage and attaches it only to unsafe requests', async () => {
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(() => Promise.resolve({ ok: true, status: 204 } as Response))
    vi.stubGlobal('fetch', fetchMock)
    await apiFetch('/api/v1/auth/session')
    await apiFetch('/api/v1/test', { method: 'POST', csrfToken: 'memory-only-token', body: '{}' })
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/auth/session', expect.objectContaining({ credentials: 'same-origin', headers: expect.any(Headers) }))
    expect((fetchMock.mock.calls[0][1]?.headers as Headers).get('X-CSRF-Token')).toBeNull()
    expect((fetchMock.mock.calls[1][1]?.headers as Headers).get('X-CSRF-Token')).toBe('memory-only-token')
    expect((globalThis.localStorage?.length ?? 0)).toBe(0)
    expect((globalThis.sessionStorage?.length ?? 0)).toBe(0)
  })

  it('surfaces 401 as an expired-session error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 401 } as Response)))
    await expect(apiFetch('/api/v1/private')).rejects.toMatchObject({ status: 401 })
  })

  it('preserves a caller-supplied Accept header', async () => {
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(() => Promise.resolve({ ok: true, status: 200 } as Response))
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/v1/export.csv', { headers: { Accept: 'text/csv' } })

    expect((fetchMock.mock.calls[0][1]?.headers as Headers).get('Accept')).toBe('text/csv')
  })

  it('accepts enabled environment fallbacks and disabled saved schedules while preserving mutation headers', async () => {
    const etag = `"${'a'.repeat(64)}"`
    const environment = { portfolio_id: 'portfolio-a', enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '', revision: 0, updated_at: null, source: 'environment', etag, effective: true, degraded: false, runtime_status: 'active' }
    const savedDisabled = { portfolio_id: 'portfolio-a', enabled: false, cron: '', timezone: 'Asia/Singapore', reporting_currency: '', revision: 1, updated_at: '2026-01-01T00:00:00.000000000Z', source: 'saved', etag: `"${'b'.repeat(64)}"`, effective: false, degraded: false, runtime_status: 'disabled' }
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => init?.method === 'PUT' ? savedDisabled : environment } as Response))
    vi.stubGlobal('fetch', fetchMock)
    await expect(getMarketRefreshSchedule('portfolio-a')).resolves.toMatchObject({ source: 'environment', effective: true })
    await expect(saveMarketRefreshSchedule('portfolio-a', { enabled: false, cron: '', timezone: 'Asia/Singapore', reporting_currency: '' }, etag, 'csrf-token')).resolves.toMatchObject({ source: 'saved', enabled: false, effective: false })
    const request = fetchMock.mock.calls[1][1] as RequestInit
    expect(new Headers(request.headers).get('If-Match')).toBe(etag)
    expect(new Headers(request.headers).get('X-CSRF-Token')).toBe('csrf-token')
  })

  it('accepts a valid degraded enabled schedule but rejects contradictory runtime state and unknown fields', async () => {
    const base = { portfolio_id: 'portfolio-a', enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '', revision: 0, updated_at: null, source: 'environment', etag: `"${'c'.repeat(64)}"`, effective: false, degraded: true, runtime_status: 'degraded' }
    const fetchMock = vi.fn((_input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => base } as Response))
    vi.stubGlobal('fetch', fetchMock)
    await expect(getMarketRefreshSchedule('portfolio-a')).resolves.toMatchObject({ degraded: true, runtime_status: 'degraded', effective: false })

    const contradictory = { ...base, degraded: false }
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => contradictory } as Response)))
    await expect(getMarketRefreshSchedule('portfolio-a')).rejects.toThrow(/contradictory degraded/i)

    const unknown = { ...base, operator_note: 'unexpected' }
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => unknown } as Response)))
    await expect(getMarketRefreshSchedule('portfolio-a')).rejects.toThrow(/unexpected operator_note/i)
  })

  it('accepts an empty mapping array in a market-data setup response', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => ({
      portfolio_id: 'portfolio-a', etag: '"setup-1"', providers: [{ name: 'coingecko', configured: true, public: true }],
      instruments: [{ instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', mappings: [] }],
    }) } as Response)))

    await expect(getMarketDataSetup('portfolio-a')).resolves.toMatchObject({ instruments: [{ mappings: [] }] })
  })
})

const marketSetup: MarketDataSetup = {
  portfolio_id: 'portfolio-a', etag: '"setup-1"',
  providers: [{ name: 'coingecko', configured: true, public: true }, { name: 'open_exchange_rates', configured: true, public: false }],
  instruments: [{ instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', mappings: [{ provider: 'coingecko', provider_symbol: 'aaa', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z' }] }],
}

const coherentRefresh = (): any => ({
  refresh: { refresh_id: 'refresh-1', portfolio_id: 'portfolio-a', status: 'complete', started_at: '2026-01-01T00:00:00.000000000Z', ended_at: '2026-01-01T00:00:01.000000000Z', observation_ids: ['quote-observation', 'fx-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], providers: [
    { provider: 'coingecko', status: 'complete', request_count: 1, succeeded_count: 1, failed_count: 0, missing_mapping_count: 0, observation_ids: ['quote-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], errors: [] },
    { provider: 'open_exchange_rates', status: 'complete', request_count: 1, succeeded_count: 1, failed_count: 0, missing_mapping_count: 0, observation_ids: ['fx-observation'], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], errors: [] },
  ] },
  snapshot: { snapshot_id: 'snapshot-1', portfolio_id: 'portfolio-a', calculation_version: 'phase-b.decimal.v1', created_at: '2026-01-01T00:00:02.000000000Z', as_of: '2026-01-01T00:00:01.000000000Z', complete: true, reporting_currency: 'MYR', reporting_total: '12', currencies: ['USD'], subtotals: [{ currency: 'USD', market_value: '10', cost_basis: '8', unrealised_pnl: '2' }], missing_count: 0, stale_count: 0, future_count: 0, invalid_count: 0, missing_dependencies: [], stale_dependencies: [], future_dependencies: [], invalid_dependencies: [], quote_ids: ['quote-observation'], fx_ids: ['fx-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], source_timestamps: ['2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z'], presentation_timezone: 'Asia/Singapore' },
  complete: true, freshness_policy: 'quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero', missing_instruments: [],
})

async function decodeMarketResult(value: unknown) {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => value } as Response)))
  return refreshMarketData('portfolio-a', 'MYR', 'refresh-key-1', undefined, undefined, marketSetup)
}

describe('semantic market refresh decoding', () => {
  it('accepts coherent quote, FX, and original-currency subtotal data', async () => {
    await expect(decodeMarketResult(coherentRefresh())).resolves.toMatchObject({ complete: true })
  })

  it.each([
    ['counter mismatch', (value: ReturnType<typeof coherentRefresh>) => { value.refresh.providers[0].request_count = 2 }],
    ['unknown dependency', (value: ReturnType<typeof coherentRefresh>) => { value.snapshot.complete = false; value.complete = false; value.refresh.status = 'incomplete'; value.refresh.providers[0].status = 'incomplete'; value.refresh.providers[0].failed_count = 1; value.refresh.providers[0].succeeded_count = 0; value.refresh.providers[0].observation_ids = []; value.refresh.observation_ids = ['fx-observation']; value.snapshot.reporting_total = undefined; value.snapshot.missing_count = 1; value.snapshot.missing_dependencies = ['missing quote:unknown']; value.snapshot.quote_ids = [] }],
    ['missing FX dependency', (value: ReturnType<typeof coherentRefresh>) => { value.snapshot.fx_ids = [] }],
    ['contradictory currencies', (value: ReturnType<typeof coherentRefresh>) => { value.snapshot.currencies = ['EUR'] }],
  ])('rejects %s', async (_name, mutate) => {
    const value = coherentRefresh()
    mutate(value)
    await expect(decodeMarketResult(value)).rejects.toSatisfy((error: unknown) => error instanceof APIError && /Invalid/.test(error.message))
  })

  it('distinguishes future timestamps from invalid ordering', async () => {
    const future = coherentRefresh()
    future.refresh.started_at = '2999-01-01T00:00:00.000000000Z'
    await expect(decodeMarketResult(future)).rejects.toSatisfy((error: unknown) => error instanceof APIError && /Future/.test(error.message))
    const reversed = coherentRefresh()
    reversed.refresh.started_at = '2026-01-01T00:00:02.000000000Z'
    await expect(decodeMarketResult(reversed)).rejects.toSatisfy((error: unknown) => error instanceof APIError && /ordering/.test(error.message))
  })

  it('accepts setup-bound future quote and FX dependencies', async () => {
    const value = coherentRefresh()
    value.snapshot.future_count = 2
    value.snapshot.future_dependencies = ['quote:instrument-a', 'fx:USD/MYR']
    await expect(decodeMarketResult(value)).resolves.toMatchObject({ complete: true })
  })

  it.each([
    ['foreign future quote instrument', 'quote:foreign-instrument'],
    ['foreign future FX source currency', 'fx:EUR/MYR'],
    ['foreign future FX target currency', 'fx:USD/SGD'],
    ['malformed future dependency', 'future:instrument-a'],
  ])('rejects %s', async (_name, dependency) => {
    const value = coherentRefresh()
    value.snapshot.future_count = 1
    value.snapshot.future_dependencies = [dependency]
    await expect(decodeMarketResult(value)).rejects.toSatisfy((error: unknown) => error instanceof APIError && /Invalid/.test(error.message))
  })

  it('rejects duplicate future dependency identities', async () => {
    const value = coherentRefresh()
    value.snapshot.future_count = 2
    value.snapshot.future_dependencies = ['quote:instrument-a', 'quote:instrument-a']
    await expect(decodeMarketResult(value)).rejects.toSatisfy((error: unknown) => error instanceof APIError && /duplicate dependency/.test(error.message))
  })

  it.each([
    ['unbound quote observation', (value: ReturnType<typeof coherentRefresh>) => { value.snapshot.quote_ids = ['not-an-observation'] }],
    ['snapshot as-of after creation', (value: ReturnType<typeof coherentRefresh>) => { value.snapshot.as_of = '2026-01-01T00:00:03.000000000Z' }],
    ['incomplete original currency set', (value: ReturnType<typeof coherentRefresh>) => { value.snapshot.currencies = [] }],
  ])('rejects %s', async (_name, mutate) => {
    const value = coherentRefresh()
    mutate(value)
    await expect(decodeMarketResult(value)).rejects.toSatisfy((error: unknown) => error instanceof APIError && /Invalid/.test(error.message))
  })
})
