import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import MarketDataPanel from './MarketDataPanel'

const providers = [
  { name: 'coingecko', configured: true, public: true },
  { name: 'finnhub', configured: false },
]
const setup = (overrides: Record<string, unknown> = {}) => ({
  portfolio_id: 'portfolio-a', etag: '"setup-1"', providers,
  instruments: [
    { instrument_id: 'authority-a', symbol: 'SAME', description: 'First company', currency: 'USD', mappings: [] },
    { instrument_id: 'authority-b', symbol: 'SAME', description: 'Second company', currency: 'USD', mappings: [] },
  ],
  ...overrides,
})
const refreshResult = (complete = false) => ({
  refresh: { status: complete ? 'complete' : 'incomplete', providers: [{ provider: 'finnhub', status: complete ? 'complete' : 'incomplete', request_count: 1, succeeded_count: complete ? 1 : 0, failed_count: complete ? 0 : 1, missing_mapping_count: complete ? 0 : 1 }] },
  snapshot: { complete, missing_count: complete ? 0 : 1, stale_count: 0, invalid_count: 0 }, complete,
  freshness_policy: 'quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero',
  missing_instruments: complete ? [] : ['SAME (USD)'],
})

function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, headers: new Headers(), json: async () => body } as Response)
}

function renderPanel(fetchMock: ReturnType<typeof vi.fn>, props: Partial<React.ComponentProps<typeof MarketDataPanel>> = {}) {
  vi.stubGlobal('fetch', fetchMock)
  return render(<MarketDataPanel portfolioID="portfolio-a" reportingCurrency="MYR" csrfToken="csrf" onUnauthorized={vi.fn()} onReload={vi.fn()} {...props} />)
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('MarketDataPanel', () => {
  it('uses human-readable instruments and exposes readiness without asking for authority IDs or secrets', async () => {
    const fetchMock = vi.fn(() => json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market data' })).toBeTruthy())
    expect(screen.getAllByText('SAME')).toHaveLength(2)
    expect(screen.getAllByRole('row')[1].textContent).toContain('First company')
    expect(screen.getByText('finnhub: not configured')).toBeTruthy()
    expect(screen.getByText('coingecko: ready')).toBeTruthy()
    expect(screen.queryByText('authority-a')).toBeNull()
    expect(screen.queryByText(/api key|secret/i)).toBeNull()
  })

  it('submits every confirmed mapping in one atomic batch, preserving duplicate symbols by row', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).endsWith('/market-data/mappings')
      ? json({ status: 'saved', etag: '"setup-2"', outcomes: [{ instrument_id: 'authority-a' }, { instrument_id: 'authority-b' }] })
      : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getAllByRole('combobox')).toHaveLength(2))
    const rows = screen.getAllByRole('row').slice(1)
    for (const [index, row] of rows.entries()) {
      fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
      fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: `coin-${index}` } })
      fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'provider listing' } })
      fireEvent.click(within(row).getByRole('checkbox'))
    }
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/market-data/mappings'))).toBe(true))
    const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith('/market-data/mappings'))
    const body = JSON.parse(String(call?.[1]?.body)) as { mappings: Array<Record<string, unknown>> }
    expect(body.mappings).toHaveLength(2)
    expect(body.mappings.map((item) => item.instrument_id)).toEqual(['authority-a', 'authority-b'])
    expect(body.mappings.every((item) => item.confirm_unverified === true)).toBe(true)
    expect(new Headers(call?.[1]?.headers as HeadersInit).get('If-Match')).toBe('"setup-1"')
  })

  it('reports a stale ETag without pretending the batch was saved', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/mappings') ? json({ error: 'stale_mapping_setup' }, 412) : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getAllByRole('combobox')).toHaveLength(2))
    const row = screen.getAllByRole('row')[1]
    fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
    fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: 'coin-a' } })
    fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'provider listing' } })
    fireEvent.click(within(row).getByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/changed in another window/i))
  })

  it('performs one refresh action, prevents double-submit, and withholds an incomplete total', async () => {
    let resolveRefresh: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/market-data/refresh')) return new Promise<Response>((resolve) => { resolveRefresh = resolve })
      return json(setup())
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh prices' })).toBeTruthy())
    const button = screen.getByRole('button', { name: 'Refresh prices' })
    fireEvent.click(button)
    fireEvent.click(button)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/refresh'))).toHaveLength(1)
    resolveRefresh?.(await json(refreshResult(false)))
    await waitFor(() => expect(screen.getByText(/No partial total is shown/i)).toBeTruthy())
    expect(screen.getByText(/Latest refresh: incomplete/i)).toBeTruthy()
    expect(screen.getByText(/Missing exact mappings/i)).toBeTruthy()
  })

  it('validates successful response shape and cleans up pending requests on scope replacement and unmount', async () => {
    const signals: AbortSignal[] = []
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal)
      return new Promise<Response>(() => undefined)
    })
    const rendered = renderPanel(fetchMock)
    await waitFor(() => expect(signals).toHaveLength(1))
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-b" reportingCurrency="MYR" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(signals[0].aborted).toBe(true))
    rendered.unmount()
    expect(signals[1].aborted).toBe(true)
  })

  it('tears down the active workflow synchronously on session expiry', async () => {
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn(() => json({ error: 'unauthorized' }, 401))
    renderPanel(fetchMock, { onUnauthorized })
    await waitFor(() => expect(onUnauthorized).toHaveBeenCalledOnce())
  })

  it('rejects a malformed successful setup response instead of rendering guessed controls', async () => {
    const fetchMock = vi.fn(() => json({ portfolio_id: 'portfolio-a', etag: '"setup-1"', providers: 'not-an-array', instruments: [] }))
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/Invalid market-data setup response/i))
    expect(screen.queryByRole('button', { name: 'Save all mappings' })).toBeNull()
  })
})
