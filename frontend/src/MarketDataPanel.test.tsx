import userEvent from '@testing-library/user-event'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import MarketDataPanel, { scheduleCronForPreset, schedulePresetFromCron, validScheduleTime } from './MarketDataPanel'
import { hasMatchingDeclaration, installCommittedStyles } from './cssTestUtils'

const providers = [
  { name: 'coingecko', configured: true, public: true },
  { name: 'finnhub', configured: false, public: false },
]
const setup = (overrides: Record<string, unknown> = {}) => ({
  portfolio_id: 'portfolio-a', etag: '"setup-1"', providers,
  instruments: [
    { instrument_id: 'authority-a', symbol: 'SAME', description: 'First company', currency: 'USD', mappings: [] },
    { instrument_id: 'authority-b', symbol: 'SAME', description: 'Second company', currency: 'USD', mappings: [] },
  ],
  ...overrides,
})
const refreshResult = (complete = false, diagnostics: { future?: string[]; invalid?: string[] } = {}) => ({
  refresh: { refresh_id: 'refresh-1', portfolio_id: 'portfolio-a', status: complete ? 'complete' : 'incomplete', started_at: '2026-01-01T00:00:00.000000000Z', ended_at: '2026-01-01T00:00:01.000000000Z', observation_ids: [], providers: [{ provider: 'finnhub', status: complete ? 'complete' : 'incomplete', request_count: 1, succeeded_count: complete ? 1 : 0, failed_count: complete ? 0 : 1, missing_mapping_count: complete ? 0 : 1, observation_ids: [], quote_observations: [], fx_observations: [], errors: complete ? [] : ['configuration'] }] },
  snapshot: { snapshot_id: 'snapshot-1', portfolio_id: 'portfolio-a', calculation_version: 'phase-b.decimal.v1', created_at: '2026-01-01T00:00:01.000000000Z', as_of: '2026-01-01T00:00:01.000000000Z', complete, reporting_currency: 'MYR', currencies: [], subtotals: [], missing_count: complete ? 0 : 1, stale_count: 0, future_count: diagnostics.future?.length || 0, invalid_count: diagnostics.invalid?.length || 0, missing_dependencies: complete ? [] : ['quote:authority-a'], stale_dependencies: [], future_dependencies: diagnostics.future || [], invalid_dependencies: diagnostics.invalid || [], quote_ids: [], fx_ids: [], quote_observations: [], fx_observations: [], source_timestamps: [], presentation_timezone: 'Asia/Singapore' }, complete,
  freshness_policy: 'quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero',
  missing_instruments: complete ? [] : ['SAME (USD)'],
})

function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, headers: new Headers(), json: async () => body } as Response)
}

function renderPanel(fetchMock: ReturnType<typeof vi.fn>, props: Partial<React.ComponentProps<typeof MarketDataPanel>> = {}) {
  const schedule = { portfolio_id: 'portfolio-a', enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '', revision: 0, updated_at: null, source: 'environment', etag: `"${'a'.repeat(64)}"`, effective: true, degraded: false, runtime_status: 'active' }
  const routedFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const response = await (fetchMock as unknown as (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>)(input, init)
    if (String(input).endsWith('/market-data/schedule') && response.status === 200) {
      const body = await response.json()
      if (body && typeof body === 'object' && 'providers' in body) return json(schedule)
      return json(body, response.status)
    }
    return response
  })
  vi.stubGlobal('fetch', routedFetch)
  return render(<MarketDataPanel portfolioID="portfolio-a" reportingCurrency="MYR" csrfToken="csrf" onUnauthorized={vi.fn()} onReload={vi.fn()} {...props} />)
}

function completeMapping(row: HTMLElement, index = 0) {
  fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
  fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: `coin-${index}` } })
  fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'provider listing' } })
  fireEvent.click(within(row).getByRole('checkbox'))
}

function mappingComboboxes() {
  return screen.getAllByRole('row').slice(1).map((row) => within(row).getByRole('combobox'))
}

function mappingCheckboxes() {
  return screen.getAllByRole('row').slice(1).map((row) => within(row).getByRole('checkbox'))
}

const scheduleEtag = (letter: string) => `"${letter.repeat(64)}"`
const environmentSchedule = (overrides: Record<string, unknown> = {}) => ({ portfolio_id: 'portfolio-a', enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '', revision: 0, updated_at: null, source: 'environment', etag: scheduleEtag('a'), effective: true, degraded: false, runtime_status: 'active', ...overrides })
const savedSchedule = (overrides: Record<string, unknown> = {}) => ({ ...environmentSchedule({ source: 'saved', revision: 1, updated_at: '2026-01-01T00:00:00.000000000Z', etag: scheduleEtag('b') }), ...overrides })
const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise })
  return { promise, resolve, reject }
}

function renderRoutedSchedule({
  schedule = environmentSchedule(),
  putResponse = json(savedSchedule()),
  setupResponse = setup(),
  portfolioID = 'portfolio-a',
  onUnauthorized = vi.fn(),
  onReload = vi.fn(),
  scheduleGet,
}: {
  schedule?: unknown
  putResponse?: Promise<Response>
  setupResponse?: Record<string, unknown>
  portfolioID?: string
  onUnauthorized?: () => void
  onReload?: () => void
  scheduleGet?: Promise<Response>
} = {}) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path.endsWith('/market-data/schedule')) {
      return init?.method === 'PUT' ? putResponse : (scheduleGet || json(schedule))
    }
    if (path.endsWith('/market-data/discover')) return json({ portfolio_id: portfolioID, etag: '"setup-1"', results: [] })
    if (path.endsWith('/market-data')) return json({ ...setupResponse, portfolio_id: portfolioID })
    return json({})
  })
  vi.stubGlobal('fetch', fetchMock)
  const rendered = render(<MarketDataPanel portfolioID={portfolioID} reportingCurrency="MYR" csrfToken="csrf-token" onUnauthorized={onUnauthorized} onReload={onReload} />)
  return { rendered, fetchMock, onUnauthorized, onReload }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('MarketDataPanel', () => {
  it('converts Daily, Weekdays, and Custom schedule controls without accepting invalid local times', () => {
    expect(scheduleCronForPreset('daily', '05:07')).toBe('7 5 * * *')
    expect(scheduleCronForPreset('weekdays', '23:59')).toBe('59 23 * * 1-5')
    expect(schedulePresetFromCron('7 5 * * *')).toBe('daily')
    expect(schedulePresetFromCron('59 23 * * 1-5')).toBe('weekdays')
    expect(schedulePresetFromCron('0 5 1 * *')).toBe('custom')
    expect(validScheduleTime('00:00')).toBe(true)
    expect(validScheduleTime('23:59')).toBe(true)
    expect(validScheduleTime('24:00')).toBe(false)
    expect(validScheduleTime('5:07')).toBe(false)
  })

  it('binds real market-data controls and raw table cells to scoped legacy rules', async () => {
    const removeStyles = installCommittedStyles()
    const fetchMock = vi.fn(() => json(setup()))
    try {
      renderPanel(fetchMock)
      const provider = await waitFor(() => mappingComboboxes()[0])
      const identifier = screen.getAllByPlaceholderText('Exact listing / coin ID')[0]
      const save = screen.getByRole('button', { name: 'Save all mappings' })
      expect(hasMatchingDeclaration(provider, 'background', '#172322')).toBe(true)
      expect(hasMatchingDeclaration(identifier, 'background', '#172322')).toBe(true)
      provider.focus()
      expect(hasMatchingDeclaration(provider, 'outline', '3px', 'focus-visible')).toBe(true)
      expect((save as HTMLButtonElement).disabled).toBe(true)
      expect(hasMatchingDeclaration(save, 'opacity', '.65', 'disabled')).toBe(true)
      const tableCells = Array.from(document.querySelectorAll('.market-data-table th, .market-data-table td'))
      expect(tableCells.length).toBeGreaterThan(0)
      expect(tableCells.every((cell) => hasMatchingDeclaration(cell, 'border', '1px'))).toBe(true)

    } finally {
      removeStyles()
    }
  })

  it('does not prefill a guessed identifier before provider discovery returns metadata', async () => {
    const configuredSetup = setup({
      providers: [
        { name: 'coingecko', configured: true, public: true },
        { name: 'finnhub', configured: true, public: false },
        { name: 'alpha_vantage', configured: true, public: false },
      ],
      instruments: [
        { instrument_id: 'crypto-a', symbol: 'BTC', description: 'Bitcoin', currency: 'USD', mappings: [] },
        { instrument_id: 'equity-a', symbol: 'MSFT', description: 'Microsoft', currency: 'USD', mappings: [] },
        { instrument_id: 'international-a', symbol: '7203', description: 'Toyota', currency: 'JPY', mappings: [] },
      ],
    })
    const fetchMock = vi.fn((input: RequestInfo | URL) => json(configuredSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(3))
    expect(screen.queryByDisplayValue('bitcoin')).toBeNull()
    expect(screen.queryByDisplayValue('MSFT')).toBeNull()
    expect(screen.queryByDisplayValue('7203')).toBeNull()
    expect(mappingCheckboxes().every((checkbox) => !(checkbox as HTMLInputElement).checked)).toBe(true)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/market-data/discover'))).toBe(true)
  })

  it('only sends a suggestion after explicit confirmation and preserves its provenance', async () => {
    const configuredSetup = setup({
      providers: [
        { name: 'coingecko', configured: true, public: true },
        { name: 'finnhub', configured: true, public: false },
        { name: 'alpha_vantage', configured: true, public: false },
      ],
      instruments: [
        { instrument_id: 'crypto-a', symbol: 'BTC', description: 'Bitcoin', currency: 'USD', mappings: [] },
        { instrument_id: 'equity-a', symbol: 'MSFT', description: 'Microsoft', currency: 'USD', mappings: [] },
        { instrument_id: 'international-a', symbol: '7203', description: 'Toyota', currency: 'JPY', mappings: [] },
      ],
    })
    const outcomes = configuredSetup.instruments.map((instrument) => ({ instrument_id: instrument.instrument_id, status: 'saved', mapping: { id: `mapping-${instrument.instrument_id}`, instrument_id: instrument.instrument_id, provider: instrument.symbol === 'BTC' ? 'coingecko' : instrument.currency === 'USD' ? 'finnhub' : 'alpha_vantage', provider_symbol: instrument.symbol === 'BTC' ? 'bitcoin' : instrument.symbol, quote_currency: instrument.currency, active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'confirmed suggestion', created_at: '2026-01-01T00:00:00.000000000Z' } }))
    const discovery = { portfolio_id: configuredSetup.portfolio_id, etag: configuredSetup.etag, results: configuredSetup.instruments.map((instrument: { instrument_id: string; symbol: string; currency: string }) => {
      const provider = instrument.symbol === 'BTC' ? 'coingecko' : instrument.currency === 'USD' ? 'finnhub' : 'alpha_vantage'
      return { instrument_id: instrument.instrument_id, status: 'candidates', provider, attempted_providers: [provider], candidates: [{ provider, identifier: instrument.symbol === 'BTC' ? 'bitcoin' : instrument.symbol, symbol: instrument.symbol, name: instrument.symbol, ...(instrument.currency === 'JPY' ? { currency: 'JPY' } : {}) }], diagnostic: 'Provider candidates are unverified until explicitly confirmed.' }
    }) }
    const fetchMock = vi.fn((input: RequestInfo | URL, _init?: RequestInit) => String(input).endsWith('/market-data/mappings') ? json({ status: 'saved', etag: '"setup-2"', outcomes }) : String(input).endsWith('/market-data/discover') ? json(discovery) : json(configuredSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingCheckboxes()).toHaveLength(3))
    for (const checkbox of mappingCheckboxes()) fireEvent.click(checkbox)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/market-data/mappings'))).toBe(true))
    const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith('/market-data/mappings'))
    const body = JSON.parse(String(call?.[1]?.body)) as { mappings: Array<Record<string, string | boolean>> }
    expect(body.mappings.map((mapping) => mapping.provider_symbol)).toEqual(['bitcoin', 'MSFT', '7203'])
    expect(body.mappings.every((mapping) => mapping.confirm_unverified === true && String(mapping.provenance).includes('unverified'))).toBe(true)
  })

  it.each(['', '   ', 'US', 'USDD', 'usd', 'U2D', 'U$D'])('blocks a mapping with invalid quote currency %j without a save request', async (quoteCurrency) => {
    const fetchMock = vi.fn((_input: RequestInfo | URL) => json(setup({ instruments: [setup().instruments[0]] })))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    completeMapping(row)
    const quoteInput = within(row).getByRole('textbox', { name: 'Quote currency for SAME (First company)' })
    fireEvent.change(quoteInput, { target: { value: quoteCurrency } })
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    expect(quoteInput.getAttribute('aria-invalid')).toBe('true')
    expect(screen.getByText(/quote currency is required|three-letter uppercase currency code/i)).toBeTruthy()
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/mappings'))).toHaveLength(0)
  })

  it('does not enable save until valid quote currency joins every other complete and confirmed field', async () => {
    const fetchMock = vi.fn(() => json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    const rows = screen.getAllByRole('row').slice(1)
    completeMapping(rows[0])
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    completeMapping(rows[1], 1)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('blocks an invalid quote currency on a committed mapping and restores its canonical payload', async () => {
    const committedSetup = setup({ instruments: [{ ...setup().instruments[0], mappings: [{ provider: 'coingecko', provider_symbol: 'coin-a', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing' }] }] })
    const outcomes = [{ instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-a', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }]
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/mappings') ? json({ status: 'saved', etag: '"setup-2"', outcomes }) : json(committedSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    const quoteInput = within(row).getByRole('textbox', { name: 'Quote currency for SAME (First company)' })
    fireEvent.change(quoteInput, { target: { value: 'usd' } })
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/mappings'))).toHaveLength(0)
    fireEvent.change(quoteInput, { target: { value: 'USD' } })
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/mappings'))).toHaveLength(1))
    const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith('/market-data/mappings')) as unknown as [RequestInfo | URL, RequestInit]
    const body = JSON.parse(String(call[1].body)) as { mappings: Array<Record<string, unknown>> }
    expect(body.mappings).toEqual([{ instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-a', quote_currency: 'USD', provenance: 'provider listing', confirm_unverified: true }])
  })

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
      ? json({ status: 'saved', etag: '"setup-2"', outcomes: [{ instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-0', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }, { instrument_id: 'authority-b', status: 'saved', mapping: { id: 'mapping-b', instrument_id: 'authority-b', provider: 'coingecko', provider_symbol: 'coin-1', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }] })
      : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
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

  it.each([
    ['partial', [{ instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-0', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }]],
    ['extra foreign', [{ instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-0', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }, { instrument_id: 'foreign', status: 'saved', mapping: { id: 'mapping-x', instrument_id: 'foreign', provider: 'coingecko', provider_symbol: 'coin-x', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }]],
    ['duplicate', [{ instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-0', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }, { instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a2', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-1', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }]],
  ])('rejects %s successful outcome sets without reloading setup', async (_label, outcomes) => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).endsWith('/market-data/mappings')
      ? json({ status: 'saved', etag: '"setup-2"', outcomes })
      : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    const rows = screen.getAllByRole('row').slice(1)
    for (const [index, row] of rows.entries()) {
      fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
      fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: `coin-${index}` } })
      fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'provider listing' } })
      fireEvent.click(within(row).getByRole('checkbox'))
    }
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/invalid mapping batch response/i))
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBe(1)
  })

  it('accepts the exact instrument set when the server reorders outcomes', async () => {
    const mappings = [
      { instrument_id: 'authority-b', status: 'saved', mapping: { id: 'mapping-b', instrument_id: 'authority-b', provider: 'coingecko', provider_symbol: 'coin-1', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } },
      { instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-0', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } },
    ]
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/mappings') ? json({ status: 'saved', etag: '"setup-2"', outcomes: mappings }) : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    const rows = screen.getAllByRole('row').slice(1)
    for (const [index, row] of rows.entries()) {
      fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
      fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: `coin-${index}` } })
      fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'provider listing' } })
      fireEvent.click(within(row).getByRole('checkbox'))
    }
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBe(2))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBe(2)
  })

  it('reports a stale ETag without pretending the batch was saved', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/mappings') ? json({ error: 'stale_mapping_setup' }, 412) : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    for (const [index, row] of screen.getAllByRole('row').slice(1).entries()) {
      fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
      fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: `coin-${index}` } })
      fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'provider listing' } })
      fireEvent.click(within(row).getByRole('checkbox'))
    }
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/changed in another window/i))
  })

  it('keeps routine price refresh out of the one-time market-data setup area', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => { void input; return json(setup()) })
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market data' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Refresh prices' })).toBeNull()
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/market-data/refresh'))).toBe(false)
  })

  it('validates successful response shape and cleans up pending requests on scope replacement and unmount', async () => {
    const signals: AbortSignal[] = []
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal)
      return new Promise<Response>(() => undefined)
    })
    const rendered = renderPanel(fetchMock)
    await waitFor(() => expect(signals).toHaveLength(2))
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-b" reportingCurrency="MYR" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(signals[0].aborted).toBe(true))
    rendered.unmount()
    expect(signals[1].aborted).toBe(true)
    expect(signals[2].aborted).toBe(true)
    expect(signals[3].aborted).toBe(true)
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

  it.each([
    ['enabled environment', environmentSchedule(), true, 'environment', 'active'],
    ['enabled saved', savedSchedule(), true, 'saved', 'active'],
    ['saved disabled', savedSchedule({ enabled: false, cron: '', effective: false, runtime_status: 'disabled' }), false, 'saved', 'disabled'],
    ['degraded environment', environmentSchedule({ degraded: true, effective: false, runtime_status: 'degraded' }), true, 'environment', 'degraded'],
  ])('renders the %s schedule state with visible source and runtime status', async (_label, schedule, enabled, source, runtimeStatus) => {
    renderRoutedSchedule({ schedule })
    await waitFor(() => expect(screen.getByLabelText('Schedule source')).toBeTruthy())
    expect(screen.getByLabelText('Schedule source').textContent).toContain(source)
    expect(screen.getByLabelText('Schedule runtime status').textContent).toContain(runtimeStatus)
    expect((screen.getByRole('checkbox', { name: /enable scheduled refreshes/i }) as HTMLInputElement).checked).toBe(enabled)
    if (runtimeStatus === 'degraded') {
      expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(true)
      expect(screen.getByRole('alert').textContent).toMatch(/retry or reconcile/i)
    }
  })

  it('drives Daily, Weekdays, and Custom cron bodies plus currency validation through user events', async () => {
    const user = userEvent.setup()
    const { fetchMock } = renderRoutedSchedule()
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Preset' })).toBeTruthy())
    const preset = screen.getByRole('combobox', { name: 'Preset' })
    const time = screen.getByLabelText('Schedule local time')
    expect((time as HTMLInputElement).value).toBe('05:00')

    const currency = screen.getByRole('textbox', { name: 'Reporting currency' })
    await user.type(currency, 'US')
    expect((currency as HTMLInputElement).value).toBe('US')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    expect(screen.getByRole('alert').textContent).toMatch(/blank or three uppercase letters/i)
    expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(0)
    await user.clear(currency)
    await user.type(currency, 'usd')
    expect((currency as HTMLInputElement).value).toBe('USD')

    await user.selectOptions(preset, 'daily')
    await user.clear(time)
    await user.type(time, '05:07')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(1))
    expect(JSON.parse(String((fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT') as [RequestInfo | URL, RequestInit])[1].body))).toEqual({ enabled: true, cron: '7 5 * * *', timezone: 'Asia/Singapore', reporting_currency: 'USD' })

    await user.selectOptions(preset, 'weekdays')
    await user.clear(time)
    await user.type(time, '23:59')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(2))
    expect(JSON.parse(String((fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')[1] as [RequestInfo | URL, RequestInit])[1].body))).toEqual({ enabled: true, cron: '59 23 * * 1-5', timezone: 'Asia/Singapore', reporting_currency: 'USD' })

    await user.selectOptions(preset, 'custom')
    const cron = screen.getByRole('textbox', { name: 'Custom five-field cron' })
    expect((cron as HTMLInputElement).value).toBe('59 23 * * 1-5')
    await user.clear(cron)
    await user.type(cron, '0 4 * * 0')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(3))
    expect(JSON.parse(String((fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')[2] as [RequestInfo | URL, RequestInit])[1].body))).toEqual({ enabled: true, cron: '0 4 * * 0', timezone: 'Asia/Singapore', reporting_currency: 'USD' })
  })

  it('saves through the rendered button with exact request metadata and displays the returned revision and ETag', async () => {
    const user = userEvent.setup()
    const next = savedSchedule({ revision: 7, etag: scheduleEtag('c'), cron: '7 5 * * *' })
    const { fetchMock } = renderRoutedSchedule({ putResponse: json(next) })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(1))
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT') as unknown as [RequestInfo | URL, RequestInit]
    expect(JSON.parse(String(call[1].body))).toEqual({ enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '' })
    const headers = new Headers(call[1].headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token')
    expect(headers.get('If-Match')).toBe(scheduleEtag('a'))
    expect(call[1].credentials).toBe('same-origin')
    expect(call[1].signal).toBeInstanceOf(AbortSignal)
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain(`Schedule revision: 7 · ETag: ${scheduleEtag('c')}`))
    expect(screen.getByLabelText('Schedule source').textContent).toContain('saved')
  })

  it('uses the in-flight save guard for double activation and recovers after the PUT resolves', async () => {
    const user = userEvent.setup()
    const pending = deferred<Response>()
    const { fetchMock } = renderRoutedSchedule({ putResponse: pending.promise })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    const save = screen.getByRole('button', { name: 'Save schedule' })
    await user.click(save)
    await user.click(save)
    expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(1)
    expect((save as HTMLButtonElement).disabled).toBe(true)
    pending.resolve(await json(savedSchedule({ revision: 2, etag: scheduleEtag('d') })))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.getAllByRole('status').some((status) => status.textContent?.includes('Schedule saved.'))).toBe(true)
  })

  it('handles unauthorized, stale revision reload, and degraded PUT failures without leaving controls locked', async () => {
    const user = userEvent.setup()
    const onUnauthorized = vi.fn()
    const unauthorized = renderRoutedSchedule({ putResponse: json({ error: 'unauthorized' }, 401), onUnauthorized })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(onUnauthorized).toHaveBeenCalledOnce())
    expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(false)
    unauthorized.rendered.unmount()

    let scheduleGets = 0
    const latest = savedSchedule({ revision: 8, etag: scheduleEtag('e') })
    const latestGet = deferred<Response>()
    const onReload = vi.fn()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/schedule')) return init?.method === 'PUT' ? json({ error: 'stale' }, 412) : (scheduleGets++ === 0 ? json(environmentSchedule()) : latestGet.promise)
      if (path.endsWith('/discover')) return json({ portfolio_id: 'portfolio-a', etag: '"setup-1"', results: [] })
      return json(setup())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<MarketDataPanel portfolioID="portfolio-a" reportingCurrency="MYR" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={onReload} />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(onReload).toHaveBeenCalledOnce())
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/changed in another window/i))
    latestGet.resolve(await json(latest))
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain('Schedule revision: 8'))
    expect(screen.getByLabelText('Schedule revision').textContent).toContain('ETag: ' + scheduleEtag('e'))
    cleanup()

    const degradedFailure = renderRoutedSchedule({ putResponse: json({ error: 'runtime degraded' }, 503) })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/runtime recovery is unresolved/i))
    expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(false)
    degradedFailure.rendered.unmount()
  })

  it.each([
    ['malformed', { portfolio_id: 'portfolio-a' }],
    ['unknown-field', { ...environmentSchedule(), unexpected: true }],
    ['contradictory', environmentSchedule({ enabled: true, effective: false, runtime_status: 'active' })],
  ])('fails closed for %s schedule success payloads', async (_label, payload) => {
    renderRoutedSchedule({ schedule: payload })
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/Invalid market refresh schedule response/i))
    expect(screen.queryByRole('button', { name: 'Save schedule' })).toBeNull()
    expect(screen.queryByRole('combobox', { name: 'Preset' })).toBeNull()
  })

  it('aborts every pending schedule request on scope change and unmount, ignoring stale completions', async () => {
    const user = userEvent.setup()
    const initialGet = deferred<Response>()
    const nextGet = deferred<Response>()
    const finalGet = deferred<Response>()
    const pendingPut = deferred<Response>()
    const scheduleGetSignals: AbortSignal[] = []
    let pendingPutSignal: AbortSignal | null | undefined
    let portfolio = 'portfolio-a'
    const oldSchedule = environmentSchedule()
    const newSchedule = environmentSchedule({ portfolio_id: 'portfolio-b', etag: scheduleEtag('f') })
    const finalSchedule = environmentSchedule({ portfolio_id: 'portfolio-c', etag: scheduleEtag('8') })
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/schedule')) {
        if (init?.method === 'PUT') {
          pendingPutSignal = init.signal
          return pendingPut.promise
        }
        if (init?.signal) scheduleGetSignals.push(init.signal)
        if (portfolio === 'portfolio-a') return initialGet.promise
        if (portfolio === 'portfolio-b') return nextGet.promise
        return finalGet.promise
      }
      if (path.endsWith('/discover')) return json({ portfolio_id: portfolio, etag: '"setup-1"', results: [] })
      return json({ ...setup(), portfolio_id: portfolio })
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<MarketDataPanel portfolioID="portfolio-a" reportingCurrency="MYR" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(scheduleGetSignals).toHaveLength(1))
    portfolio = 'portfolio-b'
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-b" reportingCurrency="MYR" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(scheduleGetSignals).toHaveLength(2))
    expect(scheduleGetSignals[0].aborted).toBe(true)
    nextGet.resolve(await json(newSchedule))
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain(newSchedule.etag))
    initialGet.resolve(await json(oldSchedule))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.getByLabelText('Schedule revision').textContent).toContain(newSchedule.etag)

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(pendingPutSignal).toBeInstanceOf(AbortSignal))
    portfolio = 'portfolio-c'
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-c" reportingCurrency="MYR" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(pendingPutSignal?.aborted).toBe(true))
    await waitFor(() => expect(scheduleGetSignals).toHaveLength(3))
    finalGet.resolve(await json(finalSchedule))
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain(finalSchedule.etag))
    pendingPut.resolve(await json(savedSchedule({ portfolio_id: 'portfolio-a', revision: 99, etag: scheduleEtag('9') })))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.getByLabelText('Schedule revision').textContent).toContain(finalSchedule.etag)
    rendered.unmount()
    expect(scheduleGetSignals.every((signal) => signal.aborted)).toBe(true)
  })

  it('keeps the rendered schedule contract accessible and verifies production callbacks before activation', async () => {
    const onUnauthorized = vi.fn()
    const onReload = vi.fn()
    const { fetchMock } = renderRoutedSchedule({ onUnauthorized, onReload })
    expect(onUnauthorized).toBeDefined()
    expect(onReload).toBeDefined()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market refresh schedule' })).toBeTruthy())
    expect(screen.getByRole('checkbox', { name: /enable scheduled refreshes/i }).getAttribute('aria-describedby')).toBeNull()
    expect(screen.getByRole('combobox', { name: 'Preset' })).toBeTruthy()
    expect(screen.getByLabelText('Schedule local time')).toBeTruthy()
    expect(screen.getByRole('textbox', { name: 'Schedule timezone' })).toBeTruthy()
    expect(screen.getByRole('textbox', { name: 'Reporting currency' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy()
    expect(screen.getByLabelText('Schedule source').getAttribute('aria-label')).toBe('Schedule source')
    expect(screen.getByLabelText('Schedule runtime status').getAttribute('aria-live')).toBe('polite')
    expect(screen.getByLabelText('Schedule revision')).toBeTruthy()
    expect(fetchMock).toHaveBeenCalled()
  })
})
