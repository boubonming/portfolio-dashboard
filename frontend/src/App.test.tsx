import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App, { compareDecimalStrings, isCSVContentType, safeCSVFilename, type PortfolioOverview } from './App'
import * as api from './api'

const completeOverview: PortfolioOverview = {
  portfolio: { id: 'portfolio-default', name: 'Personal Portfolio', default_reporting_currency: 'MYR' },
  reporting_currency: 'MYR',
  snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00.000000000Z', calculation_version: 'test.v1', reporting_total: '100.5', subtotals: [] },
  holdings: [
    { lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', description: 'Distinct AAA instrument description', quantity: '2', currency: 'USD', unit_cost: '5', cost_basis: '10', latest_price: '50.5', latest_value: '101', reporting_value: '100.5', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false },
    { lot_id: 'lot-b', symbol: 'BBB', name: 'BBB Fund', quantity: '10', currency: 'SGD', unit_cost: '1', latest_price: '2', latest_value: '20', reporting_value: '20', freshness: { status: 'stale' }, data_quality: ['stale_dependency'], missing_broker: true, missing_acquisition_date: true },
  ],
}

const marketSetup = {
  portfolio_id: 'portfolio-default',
  etag: '\"setup-1\"',
  providers: [{ name: 'finnhub', configured: true, public: false }],
  instruments: [],
}

const strictMarketSetup = {
  portfolio_id: 'portfolio-default',
  etag: '\"strict-setup-1\"',
  providers: [{ name: 'coingecko', configured: true, public: true }, { name: 'open_exchange_rates', configured: true, public: false }],
  instruments: [{ instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', mappings: [{ provider: 'coingecko', provider_symbol: 'aaa', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z' }] }],
}

function strictCompleteRefresh(): any {
  return {
    refresh: { refresh_id: 'refresh-1', portfolio_id: 'portfolio-default', status: 'complete', started_at: '2026-01-01T00:00:00.000000000Z', ended_at: '2026-01-01T00:00:01.000000000Z', observation_ids: ['quote-observation', 'fx-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], providers: [
      { provider: 'coingecko', status: 'complete', request_count: 1, succeeded_count: 1, failed_count: 0, missing_mapping_count: 0, observation_ids: ['quote-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [], errors: [] },
      { provider: 'open_exchange_rates', status: 'complete', request_count: 1, succeeded_count: 1, failed_count: 0, missing_mapping_count: 0, observation_ids: ['fx-observation'], quote_observations: [], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], errors: [] },
    ] },
    snapshot: { snapshot_id: 'snapshot-1', portfolio_id: 'portfolio-default', calculation_version: 'phase-b.decimal.v1', created_at: '2026-01-01T00:00:02.000000000Z', as_of: '2026-01-01T00:00:01.000000000Z', complete: true, reporting_currency: 'MYR', reporting_total: '12', currencies: ['USD'], subtotals: [{ currency: 'USD', market_value: '10', cost_basis: '8', unrealised_pnl: '2' }], missing_count: 0, stale_count: 0, future_count: 0, invalid_count: 0, missing_dependencies: [], stale_dependencies: [], future_dependencies: [], invalid_dependencies: [], quote_ids: ['quote-observation'], fx_ids: ['fx-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], source_timestamps: ['2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z'], presentation_timezone: 'Asia/Singapore' },
    complete: true, freshness_policy: 'quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero', missing_instruments: [],
  }
}

function strictIncompleteRefresh(): any {
  const response = strictCompleteRefresh()
  response.complete = false
  response.refresh.status = 'incomplete'
  response.refresh.providers[0].status = 'incomplete'
  response.refresh.providers[0].failed_count = 1
  response.refresh.providers[0].succeeded_count = 0
  response.refresh.providers[0].observation_ids = []
  response.refresh.providers[0].quote_observations = []
  response.refresh.providers[0].errors = ['missing mapping']
  response.refresh.observation_ids = ['fx-observation']
  response.refresh.quote_observations = []
  response.snapshot.complete = false
  delete response.snapshot.reporting_total
  response.snapshot.missing_count = 1
  response.snapshot.missing_dependencies = ['missing quote:instrument-a']
  response.snapshot.quote_ids = []
  response.snapshot.quote_observations = []
  response.snapshot.source_timestamps = ['2026-01-01T00:00:00.000000000Z']
  return response
}

const appReview = {
  review_id: 'review-app', portfolio_id: 'portfolio-default', snapshot_id: 'snap-1', calculation_version: 'test.v1',
  scope: { type: 'holding' as const, holding_ids: ['lot-c'], instrument_ids: ['instrument-c'] }, status: 'complete' as const, recommendation: 'hold' as const,
  rationale: 'App integration review', risks: [], counterarguments: [], conditions: [], metrics: [], sourced_facts: [{ statement: 'Fact', evidence_ids: ['evidence-app'] }], model_opinion: 'Opinion',
  evidence: [{ evidence_id: 'evidence-app', url: 'https://example.test/app', title: 'App source', published_at: null, accessed_at: '2026-01-02T12:00:00.000000000Z', availability: 'available' as const, freshness: 'current' as const, excerpt: 'Excerpt' }],
  model: { provider: 'local-ai', model: 'review-model', version: '1', runtime: 'hermes-environment' as const }, generated_at: '2026-01-02T12:00:00.000000000Z', submitted_at: '2026-01-02T12:00:00.000000000Z',
  submitted_by: { principal_id: 'machine-1', principal_type: 'machine' as const, permissions: ['reviews:write' as const] }, lifecycle: { historical_status: 'complete' as const, current_snapshot_id: 'snap-1', outdated: false },
}

const decimalSortOverview: PortfolioOverview = {
  ...completeOverview,
  holdings: [
    { ...completeOverview.holdings[0], lot_id: 'lot-two', symbol: 'TWO', latest_price: '2', reporting_value: '10.1' },
    { ...completeOverview.holdings[0], lot_id: 'lot-ten', symbol: 'TEN', latest_price: '10', reporting_value: '10.02' },
    { ...completeOverview.holdings[0], lot_id: 'lot-ten02', symbol: 'TEN02', latest_price: '10.02', reporting_value: '10' },
    { ...completeOverview.holdings[0], lot_id: 'lot-ten1', symbol: 'TEN1', latest_price: '10.1', reporting_value: '2' },
  ],
}

const pnlOverview: PortfolioOverview = {
  ...completeOverview,
  holdings: [
    { ...completeOverview.holdings[0], lot_id: 'lot-gain', symbol: 'GAIN', latest_price: undefined, unrealised_pnl: '10.1' },
    { ...completeOverview.holdings[0], lot_id: 'lot-loss', symbol: 'LOSS', unrealised_pnl: '-10' },
    { ...completeOverview.holdings[0], lot_id: 'lot-zero', symbol: 'ZERO', unrealised_pnl: '0.00' },
    { ...completeOverview.holdings[0], lot_id: 'lot-unavailable', symbol: 'UNAVAILABLE', unrealised_pnl: undefined, freshness: { status: 'missing' } },
  ],
}

const allocationOverview: PortfolioOverview = {
  ...completeOverview,
  allocation: {
    state: 'complete',
    coverage: { total_lots: 2, valued_lots: 2, missing_dependencies: 0, stale_dependencies: 0, invalid_dependencies: 0 },
    by_instrument: [
      { instrument_id: 'instrument-a', symbol: 'AAA', value: '80.25', percentage: '80.25' },
      { instrument_id: 'instrument-b', symbol: 'BBB', value: '20.25', percentage: '19.75' },
    ],
    by_source_currency: [
      { currency: 'MYR', value: '80.25', percentage: '80.25' },
      { currency: 'USD', value: '20.25', percentage: '19.75' },
    ],
  },
}

const expiresAt = new Date(Date.now() + 60 * 60 * 1000).toISOString()
const authenticatedSession = { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'fixture-token', expires_at: expiresAt }) } as Response
function authenticatedFetch(handler: (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>) {
  return vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).includes('/api/v1/auth/session') ? Promise.resolve(authenticatedSession) : handler(input, init))
}

function appThesis(lotId = 'lot-a', overrides: Record<string, unknown> = {}) {
  return {
    id: `thesis-${lotId}`, portfolio_id: 'portfolio-default', holding_id: lotId, instrument_id: `instrument-${lotId}`,
    summary: `Summary ${lotId}`, intended_holding_period: 'Long term', allocation_limit: null,
    reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [],
    revision: 1, revision_id: `revision-${lotId}`, updated_at: '2026-01-02T12:00:00.000000000Z', ...overrides,
  }
}

const activityCursor = 'Y3Vyc29yLW9uZS0xMjM'
function activityItem(id: string, occurredAt = '2026-01-02T11:00:00.000000000Z', overrides: Record<string, unknown> = {}) {
  return { id, portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: occurredAt, summary: `Activity ${id}`, ...overrides }
}
function activityResponse(items: unknown[], hasMore = false, nextCursor?: string, status = 200) {
  const body = hasMore ? { items, has_more: true, next_cursor: nextCursor } : { items, has_more: false }
  return Promise.resolve({ ok: status >= 200 && status < 300, status, json: async () => body } as Response)
}
async function openActivity(fetchMock: (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>) {
  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
  render(<App />)
  await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
  await waitFor(() => expect(screen.getByRole('heading', { name: 'Activity' })).toBeTruthy())
}

async function flushEffects() {
  await act(async () => {
    for (let index = 0; index < 10; index += 1) await Promise.resolve()
  })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('Portfolio Dashboard', () => {
	it('accepts the exact backend CSV filename and falls back for malformed values', () => {
		expect(safeCSVFilename('attachment; filename="portfolio-export-MYR.csv"', 'USD')).toBe('portfolio-export-MYR.csv')
		expect(safeCSVFilename(String.raw`attachment; filename=\"portfolio-export-MYR.csv\"`, 'USD')).toBe('portfolio-export-USD.csv')
		expect(safeCSVFilename('attachment; filename="../../private.csv"', 'USD')).toBe('portfolio-export-USD.csv')
		expect(safeCSVFilename('attachment; filename="nested/private.csv"', 'USD')).toBe('portfolio-export-USD.csv')
		expect(safeCSVFilename(String.raw`attachment; filename="nested\private.csv"`, 'USD')).toBe('portfolio-export-USD.csv')
		expect(safeCSVFilename('attachment; filename="evil\r\nX.csv"', 'USD')).toBe('portfolio-export-USD.csv')
		expect(safeCSVFilename(`attachment; filename="${'a'.repeat(129)}.csv"`, 'USD')).toBe('portfolio-export-USD.csv')
	})

	it('downloads the selected reporting currency using the server filename and revokes its blob URL', async () => {
		let downloadedFilename = ''
		vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { downloadedFilename = this.download })
		const createObjectURL = vi.fn(() => 'blob:fixture')
		const revokeObjectURL = vi.fn()
		Object.assign(URL, { createObjectURL, revokeObjectURL })
		const fetchMock = vi.fn((input: RequestInfo | URL) => {
			const url = String(input)
			if (url.includes('/api/v1/portfolios/') && url.includes('/export.csv')) return Promise.resolve({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/csv; charset=utf-8', 'Content-Disposition': 'attachment; filename="portfolio-export-USD-20261014.csv"' }), blob: async () => new Blob(['portfolio_id\n']) } as Response)
			return Promise.resolve({ ok: true, status: 200, json: async () => url.includes('currency=USD') ? { ...completeOverview, reporting_currency: 'USD' } : completeOverview } as Response)
		})
		vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
		render(<App />)
		await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
		fireEvent.change(screen.getByLabelText(/reporting currency/i), { target: { value: 'USD' } })
		await waitFor(() => expect(screen.getByRole('button', { name: /download csv/i })).toBeTruthy())
		fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
		await waitFor(() => expect(createObjectURL).toHaveBeenCalledOnce())
		const exportCall = fetchMock.mock.calls.find(([input]) => String(input).includes('/export.csv'))
		expect(String(exportCall?.[0])).toContain('currency=USD')
		expect(downloadedFilename).toBe('portfolio-export-USD-20261014.csv')
		await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith('blob:fixture'))
	})

	it('uses the rendered overview portfolio id and advertises CSV', async () => {
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
	  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
	    const url = String(input)
	    if (url.includes('/export.csv')) return Promise.resolve({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/csv' }), blob: async () => new Blob(['ok']) } as unknown as Response)
	    return Promise.resolve({ ok: true, status: 200, json: async () => ({ ...completeOverview, portfolio: { ...completeOverview.portfolio, id: 'portfolio-current' } }) } as Response)
	  })
	  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
	  render(<App />)
	  await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
	  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/portfolios/portfolio-current/export.csv'))).toBe(true))
	  const exportCall = fetchMock.mock.calls.find(([input]) => String(input).includes('/export.csv'))
	  expect(new Headers(exportCall?.[1]?.headers as HeadersInit).get('Accept')).toBe('text/csv')
	})

	it('rejects a successful non-CSV response before blob creation', async () => {
	  const blob = vi.fn(async () => new Blob(['not csv']))
	  const createObjectURL = vi.fn(() => 'blob:invalid')
	  Object.assign(URL, { createObjectURL })
	  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/export.csv')
	    ? Promise.resolve({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/html' }), blob } as unknown as Response)
	    : Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
	  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
	  render(<App />)
	  await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
	  await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/CSV response/i))
	  expect(blob).not.toHaveBeenCalled()
	  expect(createObjectURL).not.toHaveBeenCalled()
	  })

	  it('fails closed without a valid rendered overview portfolio', async () => {
	  let resolveOverview: ((response: Response) => void) | undefined
	  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/overview')
	    ? new Promise<Response>((resolve) => { resolveOverview = resolve })
	    : Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
	  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
	  render(<App />)
	  await waitFor(() => expect(screen.getByRole('button', { name: /download csv/i })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
	  expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/export.csv'))).toBe(false)
	  expect(screen.getByRole('alert').textContent).toMatch(/no valid portfolio overview/i)
	  resolveOverview?.({ ok: true, status: 200, json: async () => completeOverview } as Response)
	  })

	  it('keeps CSV download keyboard-accessible and announces preparation', async () => {
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
	  let resolveExport: ((response: Response) => void) | undefined
	  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/export.csv')
	    ? new Promise<Response>((resolve) => { resolveExport = resolve })
	    : Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
	  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
	  render(<App />)
	  await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
	  const button = screen.getByRole('button', { name: /download csv/i })
	  expect(button.getAttribute('type')).toBe('button')
	  button.focus()
	  fireEvent.keyDown(button, { key: 'Enter' })
	  fireEvent.keyUp(button, { key: 'Enter' })
	  button.click()
	  await waitFor(() => expect(screen.getByRole('status').textContent).toMatch(/preparing csv/i))
	  resolveExport?.({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/csv' }), blob: async () => new Blob(['ok']) } as unknown as Response)
	  })

	  it('accepts CSV media type parameters only when syntactically valid', () => {
	  expect(isCSVContentType('TEXT/CSV; charset=utf-8')).toBe(true)
	  expect(isCSVContentType('text/csv; charset="utf-8"')).toBe(true)
	  expect(isCSVContentType('text/csv; charset')).toBe(false)
	  expect(isCSVContentType('text/csv; =utf-8')).toBe(false)
	  expect(isCSVContentType('text/html')).toBe(false)
	  expect(isCSVContentType(null)).toBe(false)
	  })

	  it.each([400, 403, 404, 500])('offers a retry after CSV HTTP %s and downloads once after success', async (status) => {
	  let exportAttempts = 0
	  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
	  const createObjectURL = vi.fn(() => 'blob:retry')
	  Object.assign(URL, { createObjectURL })
	  const fetchMock = vi.fn((input: RequestInfo | URL) => {
	    const url = String(input)
	    if (!url.includes('/export.csv')) return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
	    exportAttempts += 1
	    return exportAttempts === 1
	      ? Promise.resolve({ ok: false, status, headers: new Headers() } as Response)
	      : Promise.resolve({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/csv' }), blob: async () => new Blob(['ok']) } as Response)
	  })
	  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
	  render(<App />)
	  await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
	  await waitFor(() => expect(screen.getByRole('button', { name: /retry download/i })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /retry download/i }))
	  await waitFor(() => expect(createObjectURL).toHaveBeenCalledOnce())
	  expect(exportAttempts).toBe(2)
	  expect(click).toHaveBeenCalledOnce()
	  })

	  it('offers a retry after a network failure and cleans up when click throws', async () => {
	  let exportAttempts = 0
	  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => { throw new Error('download blocked') })
	  const createObjectURL = vi.fn(() => 'blob:throwing-download')
	  const revokeObjectURL = vi.fn()
	  Object.assign(URL, { createObjectURL, revokeObjectURL })
	  const fetchMock = vi.fn((input: RequestInfo | URL) => {
	    const url = String(input)
	    if (!url.includes('/export.csv')) return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
	    exportAttempts += 1
	    if (exportAttempts === 1) return Promise.reject(new Error('network unavailable'))
	    return Promise.resolve({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/csv' }), blob: async () => new Blob(['ok']) } as Response)
	  })
	  vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
	  render(<App />)
	  await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
	  await waitFor(() => expect(screen.getByRole('button', { name: /retry download/i })).toBeTruthy())
	  fireEvent.click(screen.getByRole('button', { name: /retry download/i }))
	  await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith('blob:throwing-download'))
	  expect(document.querySelector('a[href="blob:throwing-download"]')).toBeNull()
	  expect(click).toHaveBeenCalledOnce()
	  })

	  it('prevents duplicate CSV requests while an export is pending', async () => {
		vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
		let resolveExport: ((response: Response) => void) | undefined
		const fetchMock = vi.fn((input: RequestInfo | URL) => {
			if (String(input).includes('/export.csv')) return new Promise<Response>((resolve) => { resolveExport = resolve })
			return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
		})
		vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
		render(<App />)
		await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
		const button = screen.getByRole('button', { name: /download csv/i })
		fireEvent.click(button)
		fireEvent.click(button)
		expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/export.csv'))).toHaveLength(1)
		expect(button.getAttribute('aria-busy')).toBe('true')
		expect(screen.getByRole('status').textContent).toMatch(/preparing csv/i)
		resolveExport?.({ ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/csv' }), blob: async () => new Blob(['ok']) } as Response)
	})

	it('aborts a pending export on unmount and ignores its late response', async () => {
		const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
		const createObjectURL = vi.fn(() => 'blob:late-export')
		Object.assign(URL, { createObjectURL })
		let resolveExport: ((response: Response) => void) | undefined
		let exportSignal: AbortSignal | undefined
		const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).includes('/export.csv')
			? (exportSignal = init?.signal as AbortSignal, new Promise<Response>((resolve) => { resolveExport = resolve }))
			: Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
		vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
		render(<App />)
		await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
		fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
		expect(resolveExport).toBeDefined()
		cleanup()
		expect(exportSignal?.aborted).toBe(true)
		resolveExport?.({ ok: true, status: 200, headers: new Headers({ 'Content-Disposition': 'attachment; filename="portfolio-export-MYR.csv"' }), blob: async () => new Blob(['late']) } as Response)
		await flushEffects()
		expect(createObjectURL).not.toHaveBeenCalled()
		expect(click).not.toHaveBeenCalled()
	})

	it('clears private data and focuses login when CSV export returns 401', async () => {
		const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/export.csv')
			? Promise.resolve({ ok: false, status: 401, headers: new Headers(), blob: async () => new Blob() } as Response)
			: Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
		vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
		render(<App />)
		await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
		fireEvent.click(screen.getByRole('button', { name: /download csv/i }))
		await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
		expect(document.activeElement).toBe(screen.getByLabelText('Username'))
	})

  it('restores focus to the exact remounted same-symbol lot button after closing its thesis', async () => {
    const sameSymbolOverview = {
      ...completeOverview,
      holdings: [
        { ...completeOverview.holdings[0], lot_id: 'lot-a', symbol: 'SAME' },
        { ...completeOverview.holdings[0], lot_id: 'lot-c', symbol: 'SAME' },
      ],
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/thesis')
      ? Promise.resolve({ ok: false, status: 404, json: async () => ({}) } as Response)
      : Promise.resolve({ ok: true, status: 200, json: async () => sameSymbolOverview } as Response))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const lotAButton = screen.getByRole('button', { name: 'Open thesis for SAME lot lot-a' })
    const lotCButton = screen.getByRole('button', { name: 'Open thesis for SAME lot lot-c' })
    fireEvent.click(lotCButton)
    await waitFor(() => expect(screen.getByRole('heading', { name: /investment thesis.*same/i })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open thesis for SAME lot lot-c' })))
    expect(document.activeElement).not.toBe(screen.getByRole('button', { name: 'Open thesis for SAME lot lot-a' }))
  })

  it('loads top-level reviews unfiltered, sends the exact same-symbol lot, and restores Back focus', async () => {
    const sameSymbolOverview = {
      ...completeOverview,
      holdings: [
        { ...completeOverview.holdings[0], lot_id: 'lot-a', symbol: 'SAME' },
        { ...completeOverview.holdings[0], lot_id: 'lot-c', symbol: 'SAME' },
      ],
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/reviews')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [appReview], has_more: false }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => sameSymbolOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Reviews' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'View review details' })).toBeTruthy())
    const globalReviewsCall = fetchMock.mock.calls.find(([input]) => String(input).includes('/reviews'))
    expect(String(globalReviewsCall?.[0])).not.toContain('holding_id=')
    fireEvent.click(screen.getByRole('button', { name: 'Holdings' }))
    const lotC = screen.getByRole('button', { name: 'View reviews for SAME lot lot-c' })
    fireEvent.click(lotC)
    await waitFor(() => expect(screen.getByText('Filtered to exact lot lot-c')).toBeTruthy())
    const filteredCall = fetchMock.mock.calls.filter(([input]) => String(input).includes('/reviews')).at(-1)
    expect(String(filteredCall?.[0])).toContain('holding_id=lot-c')
    fireEvent.click(screen.getByRole('button', { name: 'Back to overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Holdings' })).toBeTruthy())
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'View reviews for SAME lot lot-c' }))
  })

  it('shows a loading state while the typed API request is pending', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => undefined)))
    render(<App />)
    expect(screen.getByRole('status').textContent).toMatch(/loading portfolio/i)
  })

  it('opens on the first-class Holdings destination and keeps Overview focused on valuation', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => completeOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Holdings' })).toBeTruthy())
    expect(screen.getByRole('button', { name: 'Holdings' }).getAttribute('aria-current')).toBe('page')
    expect(screen.getByRole('button', { name: 'Add holding' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByText('Overview', { selector: '.eyebrow' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Add holding' })).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Holdings' })).toBeNull()
  })

  it('renders the canonical Holdings table before compact management controls', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => completeOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('table', { name: 'Holdings' })).toBeTruthy())
    const table = screen.getByRole('table', { name: 'Holdings' })
    const management = screen.getByRole('heading', { name: 'Manage holdings' })
    expect(Boolean(table.compareDocumentPosition(management) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true)
    expect(screen.queryByRole('button', { name: /Archive lot/i })).toBeNull()
  })

  it('tears down holding mutation controls when navigating away and remounts them on Holdings', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => completeOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add holding' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    expect(screen.getByRole('heading', { name: 'Add one holding' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    expect(screen.queryByRole('heading', { name: 'Add one holding' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Holdings' }))
    expect(screen.getByRole('button', { name: 'Add holding' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Add one holding' })).toBeNull()
  })

  it('renders complete snapshots, flags, filtering and currency changes', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const response = url.includes('currency=USD') ? { ...completeOverview, reporting_currency: 'USD' } : completeOverview
      return Promise.resolve({ ok: true, json: async () => response } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getByText('100.5 MYR')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Holdings' }))
    await waitFor(() => expect(screen.getByText(/Distinct AAA instrument description/)).toBeTruthy())
    expect(screen.getByText('Source unit cost')).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: /cost basis/i })).toBeTruthy()
    expect(screen.getAllByText('10').length).toBeGreaterThan(0)
    expect(screen.getByText(/stale_dependency/i)).toBeTruthy()
    expect(screen.getAllByText('Missing').length).toBeGreaterThan(0)
    fireEvent.change(screen.getByLabelText(/filter holdings/i), { target: { value: 'AAA' } })
    expect(screen.getByText('AAA')).toBeTruthy()
    expect(screen.queryByText('BBB')).toBeNull()
    fireEvent.change(screen.getByLabelText(/reporting currency/i), { target: { value: 'USD' } })
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith(expect.stringContaining('currency=USD'), expect.anything()))
  })

  it('compares decimals exactly, including values that lexical ordering gets wrong', () => {
    expect(compareDecimalStrings('2', '10')).toBeLessThan(0)
    expect(compareDecimalStrings('10.02', '10.1')).toBeLessThan(0)
    expect(compareDecimalStrings('-2', '-10')).toBeGreaterThan(0)
    expect(compareDecimalStrings('1.000', '1')).toBe(0)
  })

  it('exposes accessible active sort state for value columns', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => completeOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const priceHeader = screen.getByRole('columnheader', { name: /latest price/i })
    expect(priceHeader.getAttribute('aria-sort')).toBe('none')
    fireEvent.click(screen.getByRole('button', { name: /sort by latest price/i }))
    expect(priceHeader.getAttribute('aria-sort')).toBe('ascending')
    fireEvent.click(screen.getByRole('button', { name: /sort by latest price/i }))
    expect(priceHeader.getAttribute('aria-sort')).toBe('descending')
    expect(screen.getByRole('columnheader', { name: /reporting value/i }).getAttribute('aria-sort')).toBe('none')
    expect(screen.getByRole('columnheader', { name: /freshness/i })).toBeTruthy()
  })

  it('renders exact decimal row ordering for latest price and reporting value', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => decimalSortOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const holdingsTable = () => screen.getByRole('table', { name: 'Holdings' })
    const rowSymbols = () => within(holdingsTable()).getAllByRole('row').slice(1).map((row) => within(row).getByRole('rowheader').querySelector('strong')?.textContent)
    const latestPriceHeader = screen.getByRole('columnheader', { name: /latest price/i })
    const reportingValueHeader = screen.getByRole('columnheader', { name: /reporting value/i })

    fireEvent.click(screen.getByRole('button', { name: /sort by latest price/i }))
    expect(latestPriceHeader.getAttribute('aria-sort')).toBe('ascending')
    expect(rowSymbols()).toEqual(['TWO', 'TEN', 'TEN02', 'TEN1'])
    fireEvent.click(screen.getByRole('button', { name: /sort by latest price/i }))
    expect(latestPriceHeader.getAttribute('aria-sort')).toBe('descending')
    expect(rowSymbols()).toEqual(['TEN1', 'TEN02', 'TEN', 'TWO'])

    fireEvent.click(screen.getByRole('button', { name: /sort by reporting value/i }))
    expect(reportingValueHeader.getAttribute('aria-sort')).toBe('ascending')
    expect(latestPriceHeader.getAttribute('aria-sort')).toBe('none')
    expect(rowSymbols()).toEqual(['TEN1', 'TEN02', 'TEN', 'TWO'])
    fireEvent.click(screen.getByRole('button', { name: /sort by reporting value/i }))
    expect(reportingValueHeader.getAttribute('aria-sort')).toBe('descending')
    expect(rowSymbols()).toEqual(['TWO', 'TEN', 'TEN02', 'TEN1'])
  })

  it('renders accessible P/L status and sorts signed exact decimals with unavailable values withheld', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => pnlOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getByText('10.1 USD')).toBeTruthy()
    expect(screen.getByText('Gain').className).toContain('subline')
    expect(screen.getByText('-10 USD')).toBeTruthy()
    expect(screen.getByText('Loss').className).toContain('subline')
    expect(screen.getByText('0.00 USD')).toBeTruthy()
    expect(screen.getByText('Break-even').className).toContain('subline')
    expect(screen.getByText('Unavailable').className).toContain('pnl-unavailable')
    expect(screen.queryByText('Unavailable')?.className).not.toMatch(/pnl-(gain|loss|break-even)/)
    const holdingsRows = within(screen.getByRole('table', { name: 'Holdings' })).getAllByRole('row').slice(1)
    const rowFor = (symbol: string) => holdingsRows.find((row) => within(row).getByRole('rowheader').querySelector('strong')?.textContent === symbol)
    expect(rowFor('GAIN')?.querySelector('.pnl-gain')).toBeTruthy()
    expect(rowFor('GAIN')?.querySelectorAll('td')[4]?.querySelector('.pnl-gain, .pnl-loss, .pnl-break-even')).toBeNull()
    expect(rowFor('GAIN')?.querySelectorAll('td')[5]?.querySelector('.pnl-gain')).toBeTruthy()
    expect(rowFor('LOSS')?.querySelector('.pnl-loss')).toBeTruthy()
    expect(rowFor('ZERO')?.querySelector('.pnl-break-even')).toBeTruthy()
    expect(rowFor('UNAVAILABLE')?.querySelector('.pnl-gain')).toBeNull()

    const holdingsTable = () => screen.getByRole('table', { name: 'Holdings' })
    const rowSymbols = () => within(holdingsTable()).getAllByRole('row').slice(1).map((row) => within(row).getByRole('rowheader').querySelector('strong')?.textContent)
    fireEvent.click(screen.getByRole('button', { name: /sort by unrealised p\/l/i }))
    expect(rowSymbols()).toEqual(['UNAVAILABLE', 'LOSS', 'ZERO', 'GAIN'])
    fireEvent.click(screen.getByRole('button', { name: /sort by unrealised p\/l/i }))
    expect(rowSymbols()).toEqual(['GAIN', 'ZERO', 'LOSS', 'UNAVAILABLE'])
  })

  it('renders exact per-lot P/L even when the global response is incomplete', async () => {
    const incomplete = { ...pnlOverview, snapshot: { ...pnlOverview.snapshot, state: 'incomplete' as const, complete: false } }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => incomplete } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getAllByText('Unavailable')).toHaveLength(1)
    expect(screen.getAllByText(/Gain/)).toHaveLength(1)
    expect(screen.getAllByText(/Loss/)).toHaveLength(2)
    expect(screen.getAllByText(/Break-even/)).toHaveLength(2)
  })

  it('reloads Holdings after a successfully committed incomplete refresh', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => String(input).endsWith('/market-data') ? marketSetup : completeOverview } as Response))
    const refreshSpy = vi.spyOn(api, 'refreshMarketData').mockResolvedValue({
      refresh: { portfolio_id: 'portfolio-default' },
      snapshot: { portfolio_id: 'portfolio-default', reporting_currency: 'MYR' },
      complete: false,
      missing_instruments: [],
    } as unknown as Awaited<ReturnType<typeof api.refreshMarketData>>)
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const overviewCallsBefore = fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByText(/valuation is incomplete; usable per-lot values were loaded/i)).toBeTruthy())
    expect(refreshSpy).toHaveBeenCalledOnce()
    expect(refreshSpy.mock.calls[0]?.[0]).toBe('portfolio-default')
    expect(refreshSpy.mock.calls[0]?.[5]).toEqual(marketSetup)
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/portfolios/portfolio-default/market-data'))).toBe(true)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length).toBe(overviewCallsBefore + 1)
    fireEvent.click(screen.getByRole('button', { name: 'Market data' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market data' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Refresh prices' })).toBeNull()
  })

  it('reloads Holdings and announces a complete refresh', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => String(input).endsWith('/market-data') ? marketSetup : completeOverview } as Response))
    const refreshSpy = vi.spyOn(api, 'refreshMarketData').mockResolvedValue({
      refresh: { portfolio_id: 'portfolio-default' },
      snapshot: { portfolio_id: 'portfolio-default', reporting_currency: 'MYR' },
      complete: true,
      missing_instruments: [],
    } as unknown as Awaited<ReturnType<typeof api.refreshMarketData>>)
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getAllByRole('button', { name: 'Refresh prices' })).toHaveLength(1)
    const overviewCallsBefore = fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByText('Prices refreshed. Complete valuation loaded.')).toBeTruthy())
    expect(refreshSpy).toHaveBeenCalledOnce()
    expect(refreshSpy.mock.calls[0]?.[0]).toBe('portfolio-default')
    expect(refreshSpy.mock.calls[0]?.[5]).toEqual(marketSetup)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length).toBe(overviewCallsBefore + 1)
    fireEvent.click(screen.getByRole('button', { name: 'Market data' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market data' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Refresh prices' })).toBeNull()
  })

  it.each([
    ['a foreign quote witness', (response: ReturnType<typeof strictCompleteRefresh>) => { response.refresh.providers[0].quote_observations[0].instrument_id = 'instrument-foreign' }],
    ['a foreign FX witness', (response: ReturnType<typeof strictCompleteRefresh>) => { response.refresh.providers[1].fx_observations[0].base_currency = 'EUR' }],
    ['a foreign missing instrument', (response: ReturnType<typeof strictCompleteRefresh>) => { response.complete = false; response.refresh.status = 'incomplete'; response.refresh.providers[0].status = 'incomplete'; response.refresh.providers[0].failed_count = 1; response.refresh.providers[0].succeeded_count = 0; response.refresh.providers[0].observation_ids = []; response.refresh.providers[0].quote_observations = []; response.refresh.providers[0].errors = ['missing mapping']; response.refresh.observation_ids = ['fx-observation']; response.refresh.quote_observations = []; response.snapshot.complete = false; delete response.snapshot.reporting_total; response.snapshot.missing_count = 1; response.snapshot.missing_dependencies = ['missing quote:instrument-a']; response.snapshot.quote_ids = []; response.snapshot.quote_observations = []; response.snapshot.source_timestamps = ['2026-01-01T00:00:00.000000000Z']; response.missing_instruments = ['FOREIGN (USD)'] }],
  ])('rejects a setup-bound refresh response containing %s before reload or success', async (_name, mutate) => {
    const response = strictCompleteRefresh()
    mutate(response)
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.endsWith('/market-data') ? strictMarketSetup : url.includes('/market-data/refresh') ? response : completeOverview
      return Promise.resolve({ ok: true, status: 200, json: async () => body } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const overviewCallsBefore = fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/Invalid market refresh response/))
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/portfolios/portfolio-default/market-data'))).toBe(true)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/market-data/refresh'))).toHaveLength(1)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length).toBe(overviewCallsBefore)
    expect(screen.queryByText(/Prices refreshed/)).toBeNull()
  })

  it('accepts a valid setup-bound complete refresh through the production Holdings path', async () => {
    const response = strictCompleteRefresh()
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.endsWith('/market-data') ? strictMarketSetup : url.includes('/market-data/refresh') ? response : completeOverview
      return Promise.resolve({ ok: true, status: 200, json: async () => body } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const overviewCallsBefore = fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByText('Prices refreshed. Complete valuation loaded.')).toBeTruthy())
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length).toBe(overviewCallsBefore + 1)
  })

  it('accepts a valid setup-bound incomplete refresh through the production Holdings path', async () => {
    const response = strictIncompleteRefresh()
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.endsWith('/market-data') ? strictMarketSetup : url.includes('/market-data/refresh') ? response : completeOverview
      return Promise.resolve({ ok: true, status: 200, json: async () => body } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByText(/valuation is incomplete; usable per-lot values were loaded/i)).toBeTruthy())
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview'))).toHaveLength(2)
  })

  it('does not refresh or reload when setup scope validation fails', async () => {
    const foreignSetup = { ...strictMarketSetup, portfolio_id: 'portfolio-foreign' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.endsWith('/market-data') ? foreignSetup : completeOverview
      return Promise.resolve({ ok: true, status: 200, json: async () => body } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const overviewCallsBefore = fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/stale portfolio scope/i))
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/market-data/refresh'))).toHaveLength(0)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length).toBe(overviewCallsBefore)
  })

  it('ignores a second activation while a Holdings refresh is in flight', async () => {
    let resolveRefresh: ((value: Awaited<ReturnType<typeof api.refreshMarketData>>) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => String(input).endsWith('/market-data') ? marketSetup : completeOverview } as Response))
    const refreshSpy = vi.spyOn(api, 'refreshMarketData').mockImplementation(() => new Promise((resolve) => { resolveRefresh = resolve }))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const refreshButton = screen.getByRole('button', { name: 'Refresh prices' })
    fireEvent.click(refreshButton)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refreshing prices…' })).toHaveProperty('disabled', true))
    fireEvent.click(refreshButton)
    expect(refreshSpy).toHaveBeenCalledOnce()
    resolveRefresh?.({
      refresh: { portfolio_id: 'portfolio-default' },
      snapshot: { portfolio_id: 'portfolio-default', reporting_currency: 'MYR' },
      complete: true,
      missing_instruments: [],
    } as unknown as Awaited<ReturnType<typeof api.refreshMarketData>>)
    await waitFor(() => expect(screen.getByText('Prices refreshed. Complete valuation loaded.')).toBeTruthy())
  })

  it('keeps Holdings usable and announces a refresh error', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => String(input).endsWith('/market-data') ? marketSetup : completeOverview } as Response))
    vi.spyOn(api, 'refreshMarketData').mockRejectedValue(new Error('provider unavailable'))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('provider unavailable'))
    expect(screen.getByRole('table', { name: 'Holdings' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Refresh prices' })).toHaveProperty('disabled', false)
    expect(screen.getByText('Price refresh failed.')).toBeTruthy()
  })

  it('clears private state and returns to login after a refresh 401', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => String(input).endsWith('/market-data') ? marketSetup : completeOverview } as Response))
    vi.spyOn(api, 'refreshMarketData').mockRejectedValue(new api.APIError(401))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(screen.queryByRole('heading', { name: 'Personal Portfolio' })).toBeNull()
  })

  it('invalidates a late refresh when switching views', async () => {
    let resolveRefresh: ((value: Awaited<ReturnType<typeof api.refreshMarketData>>) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
    vi.spyOn(api, 'refreshMarketData').mockImplementation(() => new Promise((resolve) => { resolveRefresh = resolve }))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    const overviewCallsBefore = fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refreshing prices…' })).toHaveProperty('disabled', true))
    fireEvent.click(screen.getByRole('button', { name: 'Market data' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market data' })).toBeTruthy())
    resolveRefresh?.({
      refresh: { portfolio_id: 'portfolio-default' },
      snapshot: { portfolio_id: 'portfolio-default', reporting_currency: 'MYR' },
      complete: true,
      missing_instruments: [],
    } as unknown as Awaited<ReturnType<typeof api.refreshMarketData>>)
    await flushEffects()
    fireEvent.click(screen.getByRole('button', { name: 'Holdings' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/overview')).length).toBe(overviewCallsBefore)
    expect(screen.queryByText(/Prices refreshed/)).toBeNull()
  })

  it('invalidates a late refresh when changing reporting currency', async () => {
    let resolveRefresh: ((value: Awaited<ReturnType<typeof api.refreshMarketData>>) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
    vi.spyOn(api, 'refreshMarketData').mockImplementation(() => new Promise((resolve) => { resolveRefresh = resolve }))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Refresh prices' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refreshing prices…' })).toHaveProperty('disabled', true))
    fireEvent.change(screen.getByLabelText(/reporting currency/i), { target: { value: 'USD' } })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('currency=USD'), expect.anything()))
    resolveRefresh?.({
      refresh: { portfolio_id: 'portfolio-default' },
      snapshot: { portfolio_id: 'portfolio-default', reporting_currency: 'MYR' },
      complete: true,
      missing_instruments: [],
    } as unknown as Awaited<ReturnType<typeof api.refreshMarketData>>)
    await flushEffects()
    expect(screen.queryByText(/Prices refreshed/)).toBeNull()
    expect(screen.queryByText(/Refreshing prices/)).toBeNull()
  })

  it('renders Unavailable for stale snapshots, no snapshots, and every affected freshness state', async () => {
    let response: PortfolioOverview = {
      ...pnlOverview,
      holdings: pnlOverview.holdings.map((holding) => ({ ...holding, unrealised_pnl: undefined })),
      snapshot: { ...pnlOverview.snapshot, state: 'stale', complete: true },
    }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => response } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getAllByText('Unavailable')).toHaveLength(4)
    expect(screen.queryByText('Gain')).toBeNull()
    expect(screen.queryByText('Loss')).toBeNull()
    expect(screen.queryByText('Break-even')).toBeNull()

    response = {
      ...pnlOverview,
      holdings: pnlOverview.holdings.map((holding) => ({ ...holding, unrealised_pnl: undefined })),
      snapshot: { state: 'no_snapshot', complete: false, subtotals: [] },
    }
    cleanup()
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getAllByText('Unavailable')).toHaveLength(4)
    expect(screen.queryByText('Gain')).toBeNull()
    expect(screen.queryByText('Loss')).toBeNull()
    expect(screen.queryByText('Break-even')).toBeNull()

    for (const freshness of ['stale', 'missing', 'future', 'invalid'] as const) {
      response = {
        ...pnlOverview,
        holdings: [{ ...pnlOverview.holdings[0], unrealised_pnl: '10', freshness: { status: freshness } }],
      }
      cleanup()
      render(<App />)
      await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
      expect(screen.getByText('Unavailable').className).toContain('pnl-unavailable')
      expect(screen.queryByText('Gain')).toBeNull()
      expect(screen.queryByText('Loss')).toBeNull()
      expect(screen.queryByText('Break-even')).toBeNull()
    }
  })

  it('keeps the latest currency selection when requests resolve out of order', async () => {
    let resolveMYR: ((response: Response) => void) | undefined
    let resolveUSD: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => new Promise<Response>((resolve) => {
      if (String(input).includes('currency=USD')) resolveUSD = resolve
      else resolveMYR = resolve
    }))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByLabelText(/reporting currency/i)).toBeTruthy())
    fireEvent.change(screen.getByLabelText(/reporting currency/i), { target: { value: 'USD' } })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('currency=USD'), expect.anything()))
    resolveUSD?.({ ok: true, json: async () => ({ ...completeOverview, reporting_currency: 'USD' }) } as Response)
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByText(/Reporting in USD/i)).toBeTruthy())
    resolveMYR?.({ ok: true, json: async () => completeOverview } as Response)
    await waitFor(() => expect(screen.getByText(/Reporting in USD/i)).toBeTruthy())
  })

  it('renders explicit incomplete and no-snapshot states without a fabricated total', async () => {
    const incomplete = { ...completeOverview, snapshot: { ...completeOverview.snapshot, state: 'incomplete' as const, complete: false, reporting_total: undefined } }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => incomplete } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByText(/valuation is incomplete/i)).toBeTruthy())
    expect(screen.queryByText('100.5 MYR')).toBeNull()
    const noSnapshot = { ...completeOverview, snapshot: { state: 'no_snapshot' as const, complete: false, subtotals: [] }, holdings: [] }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => noSnapshot } as Response)))
    cleanup()
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByText(/no immutable snapshot yet/i)).toBeTruthy())
    expect(screen.getByText(/frozen point-in-time valuation/i)).toBeTruthy()
    expect(screen.getByText(/does not place trades or change holdings/i)).toBeTruthy()
  })

  it('renders accessible complete instrument and source-currency allocation detail', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => allocationOverview } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Allocation' })).toBeTruthy())
    expect(screen.getByRole('list', { name: 'Allocation by instrument' })).toBeTruthy()
    expect(screen.getByRole('list', { name: 'Allocation by original source currency' })).toBeTruthy()
    expect(within(screen.getByRole('list', { name: 'Allocation by instrument' })).getByText('AAA allocation')).toBeTruthy()
    expect(within(screen.getByRole('list', { name: 'Allocation by instrument' })).getByText('80.25 · 80.25%')).toBeTruthy()
    expect(screen.getByRole('table', { name: 'Allocation detail, also available without color' })).toBeTruthy()
    expect(screen.getByRole('table', { name: /source currency allocation detail/i })).toBeTruthy()
  })

  it('distinguishes same-symbol instruments and exposes a keyboard allocation region', async () => {
    const sameSymbol = {
      ...allocationOverview,
      allocation: {
        ...allocationOverview.allocation!,
        by_instrument: [
          { instrument_id: 'instrument-usd', symbol: 'SAME', source_currency: 'USD', value: '0', percentage: '0' },
          { instrument_id: 'instrument-sgd', symbol: 'SAME', source_currency: 'SGD', value: '100', percentage: '100' },
        ],
      },
    }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => sameSymbol } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getAllByText('SAME (USD) allocation').length).toBeGreaterThan(0))
    expect(screen.getAllByText('SAME (SGD) allocation').length).toBeGreaterThan(0)
    const region = screen.getByRole('region', { name: 'Allocation detail tables' })
    expect(region.getAttribute('tabindex')).toBe('0')
    expect(screen.getByRole('list', { name: 'Allocation by instrument' }).querySelector('.allocation-track span')?.getAttribute('style')).toContain('width: 0%')
  })

  it('suppresses allocation visuals and reports coverage for partial snapshots', async () => {
    const incomplete = { ...allocationOverview, snapshot: { ...allocationOverview.snapshot, state: 'incomplete' as const, complete: false, reporting_total: undefined }, allocation: { ...allocationOverview.allocation!, state: 'partial' as const, coverage: { total_lots: 33, valued_lots: 31, missing_dependencies: 1, stale_dependencies: 1, invalid_dependencies: 0 }, by_instrument: [], by_source_currency: [] } }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => incomplete } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: /allocation percentages withheld/i })).toBeTruthy())
    expect(screen.getByText('31 / 33')).toBeTruthy()
    expect(screen.getByText(/partial values are not presented/i)).toBeTruthy()
    expect(screen.queryByRole('list', { name: /allocation by instrument/i })).toBeNull()
  })

  it('fails closed when allocation is complete but the immutable snapshot is not complete', async () => {
    const inconsistent = {
      ...allocationOverview,
      snapshot: { ...allocationOverview.snapshot, state: 'incomplete' as const, complete: false, reporting_total: undefined },
    }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => inconsistent } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: /allocation percentages withheld/i })).toBeTruthy())
    expect(screen.queryByRole('list', { name: /allocation by instrument/i })).toBeNull()
    expect(screen.queryByRole('table', { name: /allocation detail/i })).toBeNull()
  })

  it('uses distinct unavailable copy for a portfolio without an immutable snapshot', async () => {
    const noSnapshot = {
      ...allocationOverview,
      snapshot: { state: 'no_snapshot' as const, complete: false, subtotals: [] },
      allocation: { ...allocationOverview.allocation!, state: 'unavailable' as const, coverage: { total_lots: 0, valued_lots: 0, missing_dependencies: 0, stale_dependencies: 0, invalid_dependencies: 0 }, by_instrument: undefined, by_source_currency: undefined },
      holdings: [],
    }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => noSnapshot } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: /allocation unavailable/i })).toBeTruthy())
    expect(screen.getByText(/no immutable valuation exists yet/i)).toBeTruthy()
    expect(screen.queryByText('0 / 0')).toBeNull()
    expect(screen.queryByRole('list', { name: /allocation by instrument/i })).toBeNull()
  })

  it('renders reporting-currency allocation values from each response currency', async () => {
    const usdAllocation = {
      ...allocationOverview,
      reporting_currency: 'USD',
      snapshot: { ...allocationOverview.snapshot, reporting_total: '50.25' },
      allocation: {
        ...allocationOverview.allocation!,
        by_instrument: [{ instrument_id: 'instrument-a', symbol: 'AAA', value: '40.25', percentage: '80.10' }, { instrument_id: 'instrument-b', symbol: 'BBB', value: '10', percentage: '19.90' }],
        by_source_currency: [{ currency: 'MYR', value: '40.25', percentage: '80.10' }, { currency: 'USD', value: '10', percentage: '19.90' }],
      },
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve({ ok: true, json: async () => String(input).includes('currency=USD') ? usdAllocation : allocationOverview } as Response))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Allocation' })).toBeTruthy())
    expect(screen.getAllByText('80.25 · 80.25%').length).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: 'Holdings' }))
    fireEvent.change(screen.getByLabelText(/reporting currency/i), { target: { value: 'USD' } })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    await waitFor(() => expect(screen.getAllByText('40.25 · 80.10%').length).toBeGreaterThan(0))
    expect(screen.getAllByText('Value (USD)').length).toBe(2)
    expect(screen.queryByText('80.25 · 80.25%')).toBeNull()
    expect(screen.getAllByText(/MYR allocation/).length).toBeGreaterThan(0)
  })

  it('renders a clear API error state', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.reject(new Error('fixture unavailable'))))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/unable to load portfolio/i))
  })

  it('does not request private data before session bootstrap and supports login', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
      if (url.includes('/api/v1/auth/login')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'fixture-token', expires_at: expiresAt }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(fetchMock.mock.calls.map(([input]) => String(input)).some((url) => url.includes('/api/v1/portfolios/'))).toBe(false)
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'fixture-user' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'fixture-only-value' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect((globalThis.localStorage?.length ?? 0)).toBe(0)
    expect((globalThis.sessionStorage?.length ?? 0)).toBe(0)
  })

  it('shows a uniform login failure without revealing which credential failed', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).includes('/api/v1/auth/session')) return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
      return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'wrong-user' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'wrong-value' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/sign-in failed/i))
    expect(screen.getByRole('alert').textContent).not.toMatch(/wrong-user|wrong-value/i)
  })

  it('clears private data, moves focus to login, and shows failed logout revocation', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve(authenticatedSession)
      if (url.includes('/api/v1/auth/logout')) return Promise.resolve({ ok: false, status: 503, json: async () => ({ error: 'unavailable' }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/revocation|sign-out was not confirmed/i))
    expect(screen.queryByRole('heading', { name: 'Personal Portfolio' })).toBeNull()
    expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy()
    expect(document.activeElement).toBe(screen.getByLabelText('Username'))
  })

  it('does not repopulate private data when a portfolio response resolves after logout', async () => {
    let resolveOverview: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve(authenticatedSession)
      if (url.includes('/api/v1/portfolios/')) return new Promise<Response>((resolve) => { resolveOverview = resolve })
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: false }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign out' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    resolveOverview?.({ ok: true, status: 200, json: async () => completeOverview } as Response)
    await Promise.resolve()
    expect(screen.queryByRole('heading', { name: 'Personal Portfolio' })).toBeNull()
  })

  it('clears private state and focuses login when the absolute session expiry fires', async () => {
    vi.useFakeTimers()
    try {
      const expiry = new Date(Date.now() + 1000).toISOString()
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input)
        if (url.includes('/api/v1/auth/session')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'fixture-token', expires_at: expiry }) } as Response)
        return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
      })
      vi.stubGlobal('fetch', fetchMock)
      render(<App />)
      await flushEffects()
      expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy()
      await act(async () => { vi.advanceTimersByTime(999) })
      expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy()
      await act(async () => { vi.advanceTimersByTime(1) })
      expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy()
      expect(screen.getByRole('alert').textContent).toMatch(/session expired/i)
      expect(document.activeElement).toBe(screen.getByLabelText('Username'))
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not let a stale StrictMode bootstrap completion replace the active expiry timer', async () => {
    vi.useFakeTimers()
    try {
      const sessions: Array<(response: Response) => void> = []
      const shortExpiry = new Date(Date.now() + 1000).toISOString()
      const longExpiry = new Date(Date.now() + 60_000).toISOString()
      const fetchMock = vi.fn((input: RequestInfo | URL) => {
        const url = String(input)
        if (url.includes('/api/v1/auth/session')) return new Promise<Response>((resolve) => { sessions.push(resolve) })
        return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
      })
      vi.stubGlobal('fetch', fetchMock)
      render(<StrictMode><App /></StrictMode>)
      expect(sessions).toHaveLength(2)
      sessions[1]({ ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'active-token', expires_at: shortExpiry }) } as Response)
      await flushEffects()
      expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy()
      sessions[0]({ ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'stale-token', expires_at: longExpiry }) } as Response)
      await flushEffects()
      await act(async () => { vi.advanceTimersByTime(1000) })
      expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })

  it('clears private data and focuses login after a private overview 401', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve(authenticatedSession)
      if (url.includes('/api/v1/portfolios/')) return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: false }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(screen.queryByRole('heading', { name: 'Personal Portfolio' })).toBeNull()
    expect(document.activeElement).toBe(screen.getByLabelText('Username'))
  })

  it('sends the in-memory CSRF token on successful logout without a false warning', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve(authenticatedSession)
      if (url.includes('/api/v1/auth/logout')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: false }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    const logoutCall = fetchMock.mock.calls.find(([input]) => String(input).includes('/api/v1/auth/logout'))
    expect(new Headers(logoutCall?.[1]?.headers as HeadersInit).get('X-CSRF-Token')).toBe('fixture-token')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Personal Portfolio' })).toBeNull()
  })

  it('aborts bootstrap, login, and overview work when the app unmounts', async () => {
    let bootstrapInit: RequestInit | undefined
    let resolveSession: ((response: Response) => void) | undefined
    const bootstrapFetch = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      bootstrapInit = init
      return new Promise<Response>((resolve) => { resolveSession = resolve })
    })
    vi.stubGlobal('fetch', bootstrapFetch)
    const bootstrap = render(<App />)
    bootstrap.unmount()
    expect(bootstrapInit?.signal && (bootstrapInit.signal as AbortSignal).aborted).toBe(true)

    let loginInit: RequestInit | undefined
    const loginFetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
      loginInit = init
      return new Promise<Response>(() => undefined)
    })
    vi.stubGlobal('fetch', loginFetch)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'fixture-user' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'fixture-only-value' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(loginInit).toBeTruthy())
    cleanup()
    expect(loginInit?.signal && (loginInit.signal as AbortSignal).aborted).toBe(true)

    let overviewInit: RequestInit | undefined
    const overviewFetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve(authenticatedSession)
      overviewInit = init
      return new Promise<Response>(() => undefined)
    })
    vi.stubGlobal('fetch', overviewFetch)
    const overview = render(<App />)
    await waitFor(() => expect(overviewInit).toBeTruthy())
    overview.unmount()
    expect(overviewInit?.signal && (overviewInit.signal as AbortSignal).aborted).toBe(true)
    void resolveSession
  })

  it('renders humanized holding activity while retaining the lot as secondary detail', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity')) return activityResponse([
        activityItem('holding-created', '2026-01-02T12:00:00.000000000Z', { action: 'holding_created', category: 'holding', entity_type: 'holding', entity_id: 'lot-a', summary: 'Holding created: AAA — Acme Growth Fund', details: 'Lot lot-a · quantity 1.25 USD · unit cost 8.00 · cost basis 10.00' }),
        activityItem('holding-archived', '2026-01-02T11:00:00.000000000Z', { action: 'holding_removed', category: 'holding', entity_type: 'holding', entity_id: 'lot-b', summary: 'Holding archived: BBB — Archived Fund', details: 'Lot lot-b · quantity 2 SGD · unit cost 5 · cost basis 10' }),
        activityItem('generic', '2026-01-02T10:00:00.000000000Z'),
      ])
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    expect(screen.getByText('Holding created: AAA — Acme Growth Fund')).toBeTruthy()
    expect(screen.getByText(/Lot lot-a.*quantity 1\.25 USD/)).toBeTruthy()
    expect(screen.getByText('Holding archived: BBB — Archived Fund')).toBeTruthy()
    expect(screen.getByText('Activity generic')).toBeTruthy()
  })

  it('loads safe authenticated activity with bounded load-more pagination', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=Y3Vyc29yLW9uZS0xMjM')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-two', portfolio_id: 'portfolio-default', action: 'portfolio.snapshot.created', category: 'portfolio', actor_category: 'system', occurred_at: '2026-01-02T12:00:00.000000000Z', summary: 'Immutable snapshot created' }], has_more: false }) } as Response)
      if (url.includes('/activity')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T11:00:00.000000000Z', summary: 'Manual quote added', details: 'A safe summary' }], next_cursor: 'Y3Vyc29yLW9uZS0xMjM', has_more: true }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Activity' })).toBeTruthy())
    expect(screen.getByText('Manual quote added')).toBeTruthy()
    expect(screen.getByText(/Actor: administrative/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(screen.getByText('Immutable snapshot created')).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Load more activity' })).toBeNull()
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/activity?limit=20&cursor=Y3Vyc29yLW9uZS0xMjM'))).toBe(true)
  })

  it('fails closed on malformed load-more pages and retries the same cursor without page-one refetch', async () => {
    let activityCalls = 0
    const cursor = 'Y3Vyc29yLW9uZS0xMjM'
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity')) {
        activityCalls += 1
        if (activityCalls === 1) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T11:00:00.000000000Z', summary: 'Manual quote added' }], next_cursor: cursor, has_more: true }) } as Response)
        if (activityCalls === 2) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [], has_more: true }) } as Response)
        return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [], has_more: false }) } as Response)
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(screen.getByText('Manual quote added')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/invalid activity pagination/i))
    expect(screen.getByText('Manual quote added')).toBeTruthy()
    expect(activityCalls).toBe(2)
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading activity' }))
    await waitFor(() => expect(activityCalls).toBe(3))
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('/activity')).at(-1)?.[0]).toContain(`cursor=${cursor}`)
  })

  it('deduplicates activity rows and sorts equivalent timestamp forms globally', async () => {
    const cursor = 'Y3Vyc29yLW9uZS0xMjM'
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T11:00:00.000000000Z', summary: 'Duplicate row' }, { id: 'activity-two', portfolio_id: 'portfolio-default', action: 'market.fx.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T10:00:00.000000000Z', summary: 'Older row' }], has_more: false }) } as Response)
      if (url.includes('/activity')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T12:00:00.000000000Z', summary: 'Newest row' }], next_cursor: cursor, has_more: true }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(screen.getByText('Newest row')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(screen.getByText('Older row')).toBeTruthy())
    expect(screen.getAllByText(/row$/).map((node) => node.textContent)).toEqual(['Newest row', 'Older row'])
    expect(screen.queryByText('Duplicate row')).toBeNull()
  })

  it('clears activity state and focuses login after an activity 401', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity')) return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(document.activeElement).toBe(screen.getByLabelText('Username'))
  })

  it('announces the exact deduplicated keyboard append count and focuses the first new row on the final page', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) return activityResponse([activityItem('two')])
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    const loadMore = screen.getByRole('button', { name: 'Load more activity' })
    loadMore.focus()
    fireEvent.keyDown(loadMore, { key: 'Enter' })
    fireEvent.click(loadMore)
    await waitFor(() => expect(screen.getByText('Activity two')).toBeTruthy())
    expect(screen.getByText('Loaded 1 more activity.')).toBeTruthy()
    expect(document.activeElement?.getAttribute('data-activity-id')).toBe('two')
  })

  it('announces zero deduplicated keyboard additions and focuses stable status', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) return activityResponse([activityItem('one')])
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    const loadMore = screen.getByRole('button', { name: 'Load more activity' })
    loadMore.focus()
    fireEvent.keyDown(loadMore, { key: ' ' })
    fireEvent.click(loadMore)
    await waitFor(() => expect(screen.getByText('Loaded 0 more activities.')).toBeTruthy())
    expect(document.activeElement).toBe(screen.getByText('Loaded 0 more activities.'))
  })

  it('does not move focus for a pointer-triggered append', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) return activityResponse([activityItem('two')])
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    const overview = screen.getByRole('button', { name: 'Overview' })
    overview.focus()
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(screen.getByText('Activity two')).toBeTruthy())
    expect(document.activeElement).toBe(overview)
  })

  it('covers Activity loading, empty, initial failure, and successful retry states', async () => {
    let resolveActivity: ((response: Response) => void) | undefined
    const pendingFetch = vi.fn((input: RequestInfo | URL) => String(input).includes('/activity')
      ? new Promise<Response>((resolve) => { resolveActivity = resolve })
      : Promise.resolve({ ok: true, json: async () => completeOverview } as Response))
    vi.stubGlobal('fetch', authenticatedFetch(pendingFetch))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    expect(screen.getAllByRole('status').some((node) => node.textContent?.match(/loading activity/i))).toBe(true)
    resolveActivity?.({ ok: true, status: 200, json: async () => ({ items: [], has_more: false }) } as Response)
    await waitFor(() => expect(screen.getByText(/no activity recorded/i)).toBeTruthy())

    cleanup()
    let activityCalls = 0
    const retryFetch = vi.fn((input: RequestInfo | URL) => {
      if (String(input).includes('/activity')) {
        activityCalls += 1
        return activityCalls === 1
          ? Promise.reject(new Error('activity fixture unavailable'))
          : activityResponse([activityItem('retry')])
      }
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(retryFetch)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/activity fixture unavailable/i))
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.getByText('Activity retry')).toBeTruthy())
    expect(activityCalls).toBe(2)
  })

  it('retains rows after append failure, retries the same cursor, and coalesces pending activation', async () => {
    let resolveAppend: ((response: Response) => void) | undefined
    let activityCalls = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) {
        activityCalls += 1
        if (activityCalls === 1) return Promise.reject(new Error('append unavailable'))
        if (activityCalls === 2) return activityResponse([activityItem('two')], true, activityCursor)
        return new Promise<Response>((resolve) => { resolveAppend = resolve })
      }
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/append unavailable/i))
    expect(screen.getByText('Activity one')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading activity' }))
    await waitFor(() => expect(screen.getByText('Activity two')).toBeTruthy())
    expect(String(fetchMock.mock.calls.find(([input]) => String(input).includes('cursor='))?.[0])).toContain(`cursor=${activityCursor}`)

    const pendingButton = screen.getByRole('button', { name: 'Load more activity' })
    fireEvent.click(pendingButton)
    fireEvent.click(pendingButton)
    expect(activityCalls).toBe(3)
    resolveAppend?.({ ok: true, status: 200, json: async () => ({ items: [], has_more: false }) } as Response)
  })

  it('clears all private Activity state and focuses username after append-time 401', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) return activityResponse([], false, undefined, 401)
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(screen.queryByText('Activity one')).toBeNull()
    expect(document.activeElement).toBe(screen.getByLabelText('Username'))
  })

  it('aborts Activity requests on navigation and unmount and ignores stale completions', async () => {
    const requests: Array<{ input: string; init?: RequestInit; resolve: (response: Response) => void }> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/activity')) return new Promise<Response>((resolve) => { requests.push({ input: url, init, resolve }) })
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    const app = render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(requests).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    expect((requests[0].init?.signal as AbortSignal).aborted).toBe(true)
    requests[0].resolve({ ok: true, status: 200, json: async () => ({ items: [activityItem('stale')], has_more: false }) } as Response)
    await flushEffects()
    expect(screen.queryByText('Activity stale')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(requests).toHaveLength(2))
    app.unmount()
    expect((requests[1].init?.signal as AbortSignal).aborted).toBe(true)
  })

  it('removes old Activity rows and controls during Activity page-one reload and exposes retry after reload failure', async () => {
    let activityCalls = 0
    let rejectReload: ((reason?: unknown) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity')) {
        activityCalls += 1
        if (activityCalls === 1) return activityResponse([activityItem('old')], true, activityCursor)
        return new Promise<Response>((_resolve, reject) => { rejectReload = reject })
      }
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    expect(screen.getByText('Activity old')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
    await waitFor(() => expect(activityCalls).toBe(2))
    expect(screen.queryByText('Activity old')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Load more activity' })).toBeNull()
    expect(screen.getAllByRole('status').some((node) => node.textContent?.match(/loading activity/i))).toBe(true)
    rejectReload?.(new Error('reload unavailable'))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/reload unavailable/i))
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Load more activity' })).toBeNull()
  })

  it('aborts Activity work on logout and ignores stale retry and load-more completions', async () => {
    let activityCalls = 0
    const pending: Array<(response: Response) => void> = []
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity')) {
        activityCalls += 1
        if (activityCalls === 1) return Promise.reject(new Error('initial unavailable'))
        return new Promise<Response>((resolve) => { pending.push(resolve) })
      }
      if (url.includes('/auth/logout')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: false }) } as Response)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/initial unavailable/i))
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(pending).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    pending[0]({ ok: true, status: 200, json: async () => ({ items: [activityItem('stale-retry')], has_more: false }) } as Response)
    await flushEffects()
    expect(screen.queryByText('Activity stale-retry')).toBeNull()
  })

  it('aborts a pending load-more request on navigation and ignores its stale completion', async () => {
    let resolveAppend: ((response: Response) => void) | undefined
    let appendInit: RequestInit | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) {
        appendInit = init
        return new Promise<Response>((resolve) => { resolveAppend = resolve })
      }
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    fireEvent.click(screen.getByRole('button', { name: 'Load more activity' }))
    await waitFor(() => expect(appendInit).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    expect((appendInit?.signal as AbortSignal).aborted).toBe(true)
    resolveAppend?.({ ok: true, status: 200, json: async () => ({ items: [activityItem('stale-load-more')], has_more: false }) } as Response)
    await flushEffects()
    expect(screen.queryByText('Activity stale-load-more')).toBeNull()
  })

  const invalidActivityPages: Array<[string, unknown]> = [
    ['unknown page field', { items: [], has_more: false, extra: true }],
    ['unknown item field', { items: [activityItem('unknown', undefined, { extra: true })], has_more: false }],
    ['empty required string', { items: [activityItem('')], has_more: false }],
    ['overlength required string', { items: [activityItem('valid', undefined, { summary: 'x'.repeat(257) })], has_more: false }],
    ['invalid optional type', { items: [activityItem('valid', undefined, { details: 42 })], has_more: false }],
    ['overlength optional string', { items: [activityItem('valid', undefined, { details: 'x'.repeat(2001) })], has_more: false }],
    ['impossible timestamp', { items: [activityItem('valid', '2026-02-30T12:00:00.000000000Z')], has_more: false }],
    ['noncanonical timestamp', { items: [activityItem('valid', '2026-01-02T12:00:00Z')], has_more: false }],
    ['missing cursor', { items: [activityItem('valid')], has_more: true }],
    ['malformed cursor', { items: [activityItem('valid')], has_more: true, next_cursor: '%ZZ' }],
    ['semicolon cursor', { items: [activityItem('valid')], has_more: true, next_cursor: 'cursor;drop' }],
    ['final-page cursor', { items: [activityItem('valid')], has_more: false, next_cursor: activityCursor }],
  ]

  it.each(invalidActivityPages)('fails closed on %s without a render crash or page-one loop', async (_name, page) => {
    let activityCalls = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity')) {
        activityCalls += 1
        return Promise.resolve({ ok: true, status: 200, json: async () => page } as Response)
      }
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/invalid activity/i))
    expect(activityCalls).toBe(1)
    expect(screen.queryByRole('button', { name: 'Load more activity' })).toBeNull()
  })

  it('uses keyboard activation and aria-live semantics for Activity announcements', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=')) return activityResponse([activityItem('two')])
      if (url.includes('/activity')) return activityResponse([activityItem('one')], true, activityCursor)
      return Promise.resolve({ ok: true, json: async () => completeOverview } as Response)
    })
    await openActivity(fetchMock)
    const status = screen.getByRole('status')
    expect(status.getAttribute('aria-live')).toBe('polite')
    const button = screen.getByRole('button', { name: 'Load more activity' })
    expect(button.getAttribute('aria-busy')).toBe('false')
    fireEvent.keyDown(button, { key: 'Enter' })
    fireEvent.click(button)
    expect(button.getAttribute('aria-busy')).toBe('true')
    await waitFor(() => expect(screen.getByText('Loaded 1 more activity.')).toBeTruthy())
  })

  it('renders maximum contract-valid unbroken strings without a 320px horizontal overflow', async () => {
    const max = 'x'.repeat(128)
    const page = { items: [activityItem(max, '2026-01-02T11:00:00.000000000Z', { action: max, category: 'x'.repeat(64), actor_category: 'x'.repeat(64), entity_type: max, entity_id: max, summary: 'x'.repeat(256), details: 'x'.repeat(2000) })], has_more: false }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/activity')
      ? Promise.resolve({ ok: true, status: 200, json: async () => page } as Response)
      : Promise.resolve({ ok: true, json: async () => completeOverview } as Response))
    await openActivity(fetchMock)
    Object.defineProperties(document.documentElement, { clientWidth: { configurable: true, value: 320 }, scrollWidth: { configurable: true, value: 320 } })
    expect(screen.getByText('x'.repeat(256))).toBeTruthy()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(320)
  })

  it('does not fetch Activity before authentication or persist Activity and CSRF values in web storage', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'fixture-token', expires_at: expiresAt }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(fetchMock.mock.calls.map(([input]) => String(input)).some((url) => url.includes('/activity'))).toBe(false)
    expect(globalThis.localStorage?.length ?? 0).toBe(0)
    expect(globalThis.sessionStorage?.length ?? 0).toBe(0)
  })

  it('opens the exact holding lot thesis and treats a missing thesis as revision zero', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/v1/portfolios/portfolio-default/holdings/lot-a/thesis')) {
        if (init?.method === 'PUT') return Promise.resolve({ ok: true, status: 200, json: async () => ({
          id: 'thesis-a', portfolio_id: 'portfolio-default', holding_id: 'lot-a', instrument_id: 'AAA', summary: 'Hold', intended_holding_period: 'Long term',
          allocation_limit: null, reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [], revision: 1, revision_id: 'revision-a', updated_at: '2026-01-02T12:00:00.000000000Z',
        }) } as Response)
        return Promise.resolve({ ok: false, status: 404, json: async () => ({ error: 'not found' }) } as Response)
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByRole('heading', { name: /investment thesis.*aaa/i })).toBeTruthy())
    expect(screen.getByText(/No thesis has been reviewed for this lot yet/i)).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Hold' } })
    fireEvent.change(screen.getByLabelText('Intended holding period'), { target: { value: 'Long term' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 1/i)).toBeTruthy())
    const saveCall = fetchMock.mock.calls.find(([request, requestInit]) => String(request).includes('/holdings/lot-a/thesis') && requestInit?.method === 'PUT')
    expect(saveCall?.[1]?.body).toContain('"expected_revision":0')
  })

  it('preserves a conflicting draft until an explicit reload adopts the latest thesis', async () => {
    const canonical = { id: 'thesis-a', portfolio_id: 'portfolio-default', holding_id: 'lot-a', instrument_id: 'AAA', summary: 'Server summary', intended_holding_period: 'Long term', allocation_limit: '12.50', reasons_to_hold: ['Evidence'], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [{ label: 'Source', url: 'https://example.com/source', publication_date: '2026-01-01' }], revision: 2, revision_id: 'revision-2', updated_at: '2026-01-02T12:00:00.000000000Z' }
    let thesisGets = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/holdings/lot-a/thesis')) {
        if (init?.method === 'PUT') return Promise.resolve({ ok: false, status: 409, json: async () => ({ error: 'conflict' }) } as Response)
        thesisGets += 1
        return Promise.resolve({ ok: true, status: 200, json: async () => ({ ...canonical, summary: thesisGets === 1 ? 'Original summary' : 'Latest summary', revision: thesisGets === 1 ? 1 : 2 }) } as Response)
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('Original summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Unsaved draft' } })
    fireEvent.change(screen.getByLabelText('Intended holding period'), { target: { value: 'Short term' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/newer revision exists/i))
    expect(screen.getByDisplayValue('Unsaved draft')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Reload latest revision' }))
    await waitFor(() => expect(screen.getByDisplayValue('Latest summary')).toBeTruthy())
    expect(screen.queryByDisplayValue('Unsaved draft')).toBeNull()
    expect(screen.getByText(/Revision expected on save: 2/)).toBeTruthy()
  })

  it('keeps canonical decimals and dynamic lists and sources in the actual PUT payload', async () => {
    const canonical = { id: 'thesis-a', portfolio_id: 'portfolio-default', holding_id: 'lot-a', instrument_id: 'AAA', summary: 'Existing', intended_holding_period: 'Long term', allocation_limit: '12.5', reasons_to_hold: ['First'], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [], revision: 3, revision_id: 'revision-3', updated_at: '2026-01-02T12:00:00.000000000Z' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/holdings/lot-a/thesis') && init?.method === 'PUT') return Promise.resolve({ ok: true, status: 200, json: async () => ({ ...canonical, revision: 4, revision_id: 'revision-4' }) } as Response)
      if (url.includes('/holdings/lot-a/thesis')) return Promise.resolve({ ok: true, status: 200, json: async () => canonical } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('12.5')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Add reasons to hold' }))
    const reasons = screen.getAllByLabelText(/Reasons to hold/i)
    fireEvent.change(reasons[1], { target: { value: 'Second' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add source' }))
    fireEvent.change(screen.getByLabelText('Source 1 label'), { target: { value: 'Research' } })
    fireEvent.change(screen.getByLabelText('Source 1 URL'), { target: { value: 'https://example.com/research' } })
    fireEvent.change(screen.getByLabelText('Source 1 publication date (optional)'), { target: { value: '2026-02-03' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 4/i)).toBeTruthy())
    const saveCall = fetchMock.mock.calls.find(([request, requestInit]) => String(request).includes('/holdings/lot-a/thesis') && requestInit?.method === 'PUT')
    const payload = JSON.parse(String(saveCall?.[1]?.body)) as Record<string, unknown>
    expect(payload.allocation_limit).toBe('12.5')
    expect(payload.reasons_to_hold).toEqual(['First', 'Second'])
    expect(payload.sources).toEqual([{ label: 'Research', url: 'https://example.com/research', publication_date: '2026-02-03' }])
    expect(new Headers(saveCall?.[1]?.headers as HeadersInit).get('Content-Type')).toBe('application/json')
    expect(new Headers(saveCall?.[1]?.headers as HeadersInit).get('X-CSRF-Token')).toBe('fixture-token')
  })

  it('validates allocation limits as exact text at inclusive boundaries', async () => {
    const canonical = { id: 'thesis-a', portfolio_id: 'portfolio-default', holding_id: 'lot-a', instrument_id: 'AAA', summary: 'Existing', intended_holding_period: 'Long term', allocation_limit: null, reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [], revision: 1, revision_id: 'revision-1', updated_at: '2026-01-02T12:00:00.000000000Z' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).includes('/holdings/lot-a/thesis') ? Promise.resolve({ ok: true, status: 200, json: async () => canonical } as Response) : Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response))
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('Existing')).toBeTruthy())
    const limit = screen.getByLabelText(/optional allocation limit/i)
    for (const value of ['0', '0.00', '100', '100.00', '12.34']) {
      fireEvent.change(limit, { target: { value } })
      expect(screen.queryByText(/Enter a percentage/i)).toBeNull()
    }
    for (const value of ['-0.01', '100.01', '12.345', '001']) {
      fireEvent.change(limit, { target: { value } })
      expect(screen.getByText(/Enter a percentage/i)).toBeTruthy()
    }
    expect(fetchMock.mock.calls.some((call) => call[1]?.method === 'PUT')).toBe(false)
  })

  it('loads immutable history independently and renders safe source links oldest first', async () => {
    const thesis = (revision: number, summary: string) => ({ id: 'thesis-a', portfolio_id: 'portfolio-default', holding_id: 'lot-a', instrument_id: 'AAA', summary, intended_holding_period: 'Long term', allocation_limit: null, reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [{ label: `Source ${revision}`, url: `https://example.com/${revision}`, publication_date: null }], revision, revision_id: `revision-${revision}`, updated_at: '2026-01-02T12:00:00.000000000Z', created_at: `2026-01-0${revision}T12:00:00.000000000Z` })
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/history')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [thesis(1, 'First summary'), thesis(2, 'Second summary')] }) } as Response)
      if (url.includes('/holdings/lot-a/thesis')) { const { created_at: _createdAt, ...current } = thesis(2, 'Second summary'); return Promise.resolve({ ok: true, status: 200, json: async () => current } as Response) }
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('Second summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Revision 1' })).toBeTruthy())
    expect(screen.getByText('First summary')).toBeTruthy()
    expect(screen.getAllByText('Second summary').length).toBeGreaterThan(0)
    expect(screen.getByRole('link', { name: 'Source 1' }).getAttribute('rel')).toBe('noopener noreferrer')
    const revisions = screen.getAllByRole('heading', { name: /Revision [12]/ }).filter((node) => /^Revision [12]$/.test(node.textContent || '')).map((node) => node.textContent)
    expect(revisions).toEqual(['Revision 1', 'Revision 2'])
  })

  it('isolates same-symbol lots across endpoint identity and ignores a late lot response', async () => {
    const sameSymbolOverview = {
      ...completeOverview,
      holdings: [
        { ...completeOverview.holdings[0], lot_id: 'lot-a', symbol: 'SAME' },
        { ...completeOverview.holdings[0], lot_id: 'lot-c', symbol: 'SAME' },
      ],
    }
    let resolveLotA: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/holdings/lot-a/thesis')) return new Promise<Response>((resolve) => { resolveLotA = resolve })
      if (url.endsWith('/holdings/lot-c/thesis')) return Promise.resolve({ ok: true, status: 200, json: async () => appThesis('lot-c', { summary: 'Lot C summary' }) } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => sameSymbolOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Open thesis for SAME lot lot-a' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/holdings/lot-a/thesis'))).toBe(true))
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    fireEvent.click(screen.getByRole('button', { name: 'Open thesis for SAME lot lot-c' }))
    await waitFor(() => expect(screen.getByDisplayValue('Lot C summary')).toBeTruthy())
    resolveLotA?.({ ok: true, status: 200, json: async () => appThesis('lot-a', { summary: 'Stale lot A summary' }) } as Response)
    await flushEffects()
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/holdings/lot-a/thesis'))).toBe(true)
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/holdings/lot-c/thesis'))).toBe(true)
    expect(screen.queryByDisplayValue('Stale lot A summary')).toBeNull()
    expect(screen.getByDisplayValue('Lot C summary')).toBeTruthy()
  })

  it.each(['current', 'save', 'history'] as const)('fails closed for a thesis %s 401 response', async (lane) => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/holdings/lot-a/thesis')) {
        if (lane === 'current' && !init?.method) return Promise.resolve({ ok: false, status: 401, json: async () => ({}) } as Response)
        if (lane === 'history' && url.endsWith('/history')) return Promise.resolve({ ok: false, status: 401, json: async () => ({}) } as Response)
        if (lane === 'save' && init?.method === 'PUT') return Promise.resolve({ ok: false, status: 401, json: async () => ({}) } as Response)
        return Promise.resolve({ ok: true, status: 200, json: async () => appThesis() } as Response)
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    if (lane === 'save') {
      await waitFor(() => expect(screen.getByDisplayValue('Summary lot-a')).toBeTruthy())
      fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Private draft must disappear' } })
      fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    } else if (lane === 'history') {
      await waitFor(() => expect(screen.getByDisplayValue('Summary lot-a')).toBeTruthy())
      fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    }
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(screen.queryByRole('heading', { name: /Investment thesis/ })).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Personal Portfolio' })).toBeNull()
    expect(screen.queryByDisplayValue('Private draft must disappear')).toBeNull()
    expect(screen.queryByText(/Revision [0-9]+/)).toBeNull()
    expect(document.activeElement).toBe(screen.getByLabelText('Username'))
  })

  it('aborts all thesis lanes on close and ignores late current, save, and history completions', async () => {
    let resolveSave: ((response: Response) => void) | undefined
    let resolveHistory: ((response: Response) => void) | undefined
    let currentInit: RequestInit | undefined
    let saveInit: RequestInit | undefined
    let historyInit: RequestInit | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/history')) { historyInit = init; return new Promise<Response>((resolve) => { resolveHistory = resolve }) }
      if (init?.method === 'PUT') { saveInit = init; return new Promise<Response>((resolve) => { resolveSave = resolve }) }
      if (url.endsWith('/holdings/lot-a/thesis')) { currentInit = init; return Promise.resolve({ ok: true, status: 200, json: async () => appThesis() } as Response) }
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('Summary lot-a')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(historyInit).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Stale draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(saveInit).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    expect(currentInit?.signal && (currentInit.signal as AbortSignal).aborted).toBe(true)
    expect(saveInit?.signal && (saveInit.signal as AbortSignal).aborted).toBe(true)
    expect(historyInit?.signal && (historyInit.signal as AbortSignal).aborted).toBe(true)
    resolveSave?.({ ok: true, status: 200, json: async () => appThesis('lot-a', { summary: 'Late save' }) } as Response)
    resolveHistory?.({ ok: true, status: 200, json: async () => ({ items: [] }) } as Response)
    await flushEffects()
    expect(screen.queryByText('Late save')).toBeNull()
    expect(screen.queryByText(/No revision history/)).toBeNull()
  })

  it.each(['logout', 'simulated session expiry'] as const)('aborts thesis lanes on %s and leaves no private late completion', async (action) => {
    const expiry = new Date(Date.now() + (action === 'simulated session expiry' ? 500 : 60 * 60 * 1000)).toISOString()
    let resolveSave: ((response: Response) => void) | undefined
    let resolveHistory: ((response: Response) => void) | undefined
    let saveInit: RequestInit | undefined
    let historyInit: RequestInit | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/v1/auth/session')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'fixture-token', expires_at: expiry }) } as Response)
      if (url.endsWith('/history')) { historyInit = init; return new Promise<Response>((resolve) => { resolveHistory = resolve }) }
      if (init?.method === 'PUT') { saveInit = init; return new Promise<Response>((resolve) => { resolveSave = resolve }) }
      if (url.endsWith('/holdings/lot-a/thesis')) return Promise.resolve({ ok: true, status: 200, json: async () => appThesis() } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('Summary lot-a')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(historyInit).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Private late draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(saveInit).toBeTruthy())
    if (action === 'logout') fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Sign in' })).toBeTruthy())
    expect(saveInit?.signal && (saveInit.signal as AbortSignal).aborted).toBe(true)
    expect(historyInit?.signal && (historyInit.signal as AbortSignal).aborted).toBe(true)
    resolveSave?.({ ok: true, status: 200, json: async () => appThesis('lot-a', { summary: 'Late private save' }) } as Response)
    resolveHistory?.({ ok: true, status: 200, json: async () => ({ items: [] }) } as Response)
    await flushEffects()
    expect(screen.queryByText('Late private save')).toBeNull()
    expect(screen.queryByText('Private late draft')).toBeNull()
  })

  it('keeps CSRF, draft, thesis responses, and history out of storage after a real thesis save', async () => {
    globalThis.localStorage?.clear()
    globalThis.sessionStorage?.clear()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/holdings/lot-a/thesis') && init?.method === 'PUT') return Promise.resolve({ ok: true, status: 200, json: async () => appThesis('lot-a', { summary: 'Saved in memory', revision: 2, revision_id: 'revision-2' }) } as Response)
      if (url.endsWith('/holdings/lot-a/thesis')) return Promise.resolve({ ok: true, status: 200, json: async () => appThesis() } as Response)
      return Promise.resolve({ ok: true, status: 200, json: async () => completeOverview } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: /open thesis for aaa lot lot-a/i }))
    await waitFor(() => expect(screen.getByDisplayValue('Summary lot-a')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Saved in memory' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByDisplayValue('Saved in memory')).toBeTruthy())
    expect(globalThis.localStorage?.length ?? 0).toBe(0)
    expect(globalThis.sessionStorage?.length ?? 0).toBe(0)
    expect(globalThis.localStorage?.getItem('csrf_token') ?? null).toBeNull()
    expect(globalThis.sessionStorage?.getItem('thesis_draft') ?? null).toBeNull()
    expect(globalThis.localStorage?.getItem('thesis_response') ?? null).toBeNull()
    expect(globalThis.sessionStorage?.getItem('thesis_history') ?? null).toBeNull()
  })
})
