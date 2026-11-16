import userEvent from '@testing-library/user-event'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import MarketDataPanel from './MarketDataPanel'
import { committedStyleRules, hasMatchingDeclaration, installCommittedStyles } from './cssTestUtils'

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
  vi.stubGlobal('fetch', fetchMock)
  return render(<MarketDataPanel portfolioID="portfolio-a" reportingCurrency="MYR" csrfToken="csrf" onUnauthorized={vi.fn()} {...props} />)
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

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('MarketDataPanel', () => {
  it('bounds narrow mapping identity and controls so the complete focus target fits beside the sticky cell', () => {
    const removeStyles = installCommittedStyles()
    try {
      const narrowRules = committedStyleRules()
      expect(narrowRules.some(({ cssText }) => cssText.includes('.market-data-table .mapping-identity-cell') && cssText.includes('width: 6rem'))).toBe(true)
      expect(narrowRules.some(({ cssText }) => cssText.includes('table-layout: fixed'))).toBe(true)
      expect(narrowRules.some(({ cssText }) => cssText.includes('min-width: 7.5rem') && cssText.includes('max-width: 7.5rem') && cssText.includes('width: 7.5rem'))).toBe(true)
    } finally {
      removeStyles()
    }
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
      const mappingsRegion = screen.getByRole('region', { name: 'Market data mappings table horizontal scroll region' })
      expect(mappingsRegion).toHaveProperty('tabIndex', 0)
      expect(mappingsRegion.querySelector('.mapping-identity-cell')).toBeTruthy()
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

  it('keeps a confirmed mapping incomplete when quote currency differs from the instrument currency', async () => {
    const fetchMock = vi.fn((_input: RequestInfo | URL) => json(setup({ instruments: [setup().instruments[0]] })))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    completeMapping(row)
    const quoteInput = within(row).getByRole('textbox', { name: 'Quote currency for SAME (First company)' })
    fireEvent.change(quoteInput, { target: { value: 'EUR' } })
    fireEvent.click(within(row).getByRole('checkbox'))
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/must match the instrument currency USD/i)).toBeTruthy()
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/mappings'))).toHaveLength(0)
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
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(within(row).getByRole('checkbox'))
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
  ])('rejects %s successful outcome sets and reconciles before another save', async (_label, outcomes) => {
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
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBeGreaterThan(1)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
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
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/market-data/schedule'))).toBe(false)
  })

  it('validates successful response shape and cleans up pending requests on scope replacement and unmount', async () => {
    const signals: AbortSignal[] = []
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.signal) signals.push(init.signal)
      return new Promise<Response>(() => undefined)
    })
    const rendered = renderPanel(fetchMock)
    await waitFor(() => expect(signals).toHaveLength(1))
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-b" reportingCurrency="MYR" onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(signals[0].aborted).toBe(true))
    await waitFor(() => expect(signals).toHaveLength(2))
    rendered.unmount()
    expect(signals[1].aborted).toBe(true)
  })

  it('ignores the old setup response after a portfolio replacement', async () => {
    let resolveOld!: (response: Response) => void
    let resolveReplacement!: (response: Response) => void
    let reads = 0
    const fetchMock = vi.fn(() => new Promise<Response>((resolve) => {
      reads += 1
      if (reads === 1) resolveOld = resolve
      else resolveReplacement = resolve
    }))
    const rendered = renderPanel(fetchMock)
    await waitFor(() => expect(reads).toBe(1))
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-b" reportingCurrency="MYR" onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(reads).toBe(2))
    resolveOld(json(setup()) as unknown as Response)
    resolveReplacement(json(setup({ portfolio_id: 'portfolio-b', instruments: [] })) as unknown as Response)
    await waitFor(() => expect(screen.getByText(/No active instruments to map/)).toBeTruthy())
    expect(screen.queryByText('First company')).toBeNull()
  })

  it('does not let late discovery overwrite an owner-touched row', async () => {
    const configuredSetup = setup({ instruments: [setup().instruments[0]] })
    let resolveDiscovery!: (response: Response) => void
    const discovery = {
      portfolio_id: configuredSetup.portfolio_id,
      etag: configuredSetup.etag,
      results: [{ instrument_id: 'authority-a', status: 'candidates', provider: 'coingecko', attempted_providers: ['coingecko'], candidates: [{ provider: 'coingecko', identifier: 'coin-server', symbol: 'SAME', name: 'Server candidate', exchange: 'X' }], diagnostic: 'Unverified server candidate.' }],
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/discover')
      ? new Promise<Response>((resolve) => { resolveDiscovery = resolve })
      : json(configuredSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
    fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: 'coin-owner' } })
    resolveDiscovery(json(discovery) as unknown as Response)
    await waitFor(() => expect(within(row).getByDisplayValue('coin-owner')).toBeTruthy())
    expect(within(row).queryByDisplayValue('coin-server')).toBeNull()
  })

  it('does not let late discovery overwrite a confirmed row', async () => {
    const configuredSetup = setup({ instruments: [setup().instruments[0]] })
    let resolveDiscovery!: (response: Response) => void
    const discovery = {
      portfolio_id: configuredSetup.portfolio_id,
      etag: configuredSetup.etag,
      results: [{ instrument_id: 'authority-a', status: 'candidates', provider: 'coingecko', attempted_providers: ['coingecko'], candidates: [{ provider: 'coingecko', identifier: 'coin-server', symbol: 'SAME', name: 'Server candidate' }], diagnostic: 'candidate' }],
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/discover')
      ? new Promise<Response>((resolve) => { resolveDiscovery = resolve })
      : json(configuredSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    completeMapping(row)
    resolveDiscovery(json(discovery) as unknown as Response)
    await waitFor(() => expect(within(row).getByDisplayValue('coin-0')).toBeTruthy())
    expect((within(row).getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
    expect(within(row).queryByDisplayValue('coin-server')).toBeNull()
  })

  it('explains the unfinished row and focuses the next unfinished mapping', async () => {
    const fetchMock = vi.fn(() => json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    expect(screen.getByText(/Finish mapping for SAME \(First company\)/)).toBeTruthy()
    const next = screen.getByRole('button', { name: 'Next unfinished mapping' })
    fireEvent.click(next)
    expect(document.activeElement).toBe(mappingComboboxes()[0])
  })

  it('clears confirmation when an identity field changes', async () => {
    const fetchMock = vi.fn(() => json(setup({ instruments: [setup().instruments[0]] })))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    completeMapping(row)
    expect((within(row).getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
    fireEvent.change(within(row).getByRole('textbox', { name: 'Quote currency for SAME (First company)' }), { target: { value: 'EUR' } })
    expect((within(row).getByRole('checkbox') as HTMLInputElement).checked).toBe(false)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('keeps a confirmed receipt visible when the post-save setup read fails', async () => {
    const configuredSetup = setup({ instruments: [setup().instruments[0]] })
    let setupReads = 0
    const outcome = { instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-0', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/market-data/mappings')) return json({ status: 'saved', etag: '"setup-2"', outcomes: [outcome] })
      if (String(input).endsWith('/market-data/discover')) return json(configuredSetup)
      setupReads += 1
      return setupReads === 1 ? json(configuredSetup) : Promise.reject(new Error('read failed'))
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    completeMapping(screen.getAllByRole('row')[1])
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByText(/Saved 1 mapping as a new revision/)).toBeTruthy())
    expect(screen.getByRole('alert').textContent).toMatch(/read failed/i)
  })

  it('freezes edits during a save and requires a read after an uncertain result', async () => {
    const configuredSetup = setup({ instruments: [setup().instruments[0]] })
    let rejectSave!: (reason: Error) => void
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/market-data/mappings')) return new Promise<Response>((_resolve, reject) => { rejectSave = reject })
      if (String(input).endsWith('/market-data/discover')) return json(configuredSetup)
      return json(configuredSetup)
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    const row = screen.getAllByRole('row')[1]
    completeMapping(row)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect((within(row).getByRole('combobox') as HTMLSelectElement).disabled).toBe(true))
    rejectSave(new Error('network down'))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/uncertain.*Reload setup/i))
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Reload setup' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBeGreaterThanOrEqual(2))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Keep editing with current setup' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Keep editing with current setup' }))
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
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

  it('does not let a late discovery candidate replace an owner-edited row', async () => {
    let resolveDiscovery: ((response: unknown) => void) | undefined
    const configuredSetup = setup({ providers: [{ name: 'coingecko', configured: true, public: true }] })
    const discovery = {
      portfolio_id: 'portfolio-a', etag: '"setup-1"', results: configuredSetup.instruments.map((instrument) => ({
        instrument_id: instrument.instrument_id, status: 'candidates', provider: 'coingecko', attempted_providers: ['coingecko'],
        candidates: [{ provider: 'coingecko', identifier: `candidate-${instrument.instrument_id}`, symbol: instrument.symbol, name: 'Provider candidate', exchange: 'XNAS', type: 'Common Stock', currency: 'USD' }], diagnostic: 'candidate',
      })),
    }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/discover')
      ? new Promise<Response>((resolve) => { resolveDiscovery = (response) => resolve(json(response) as unknown as Response) })
      : json(configuredSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    const row = screen.getAllByRole('row')[1]
    fireEvent.change(within(row).getByRole('combobox'), { target: { value: 'coingecko' } })
    fireEvent.change(within(row).getByPlaceholderText('Exact listing / coin ID'), { target: { value: 'owner-id' } })
    fireEvent.change(within(row).getByPlaceholderText('Where you verified it'), { target: { value: 'owner source' } })
    resolveDiscovery?.(discovery)
    await waitFor(() => expect(screen.getByDisplayValue('owner-id')).toBeTruthy())
    expect(screen.queryByDisplayValue('candidate-authority-a')).toBeNull()
    expect(screen.getByText(/exchange: XNAS|XNAS/)).toBeTruthy()
  })

  it('clears confirmation on every material identity edit and explains incomplete progress', async () => {
    const committedSetup = setup({ instruments: [{ ...setup().instruments[0], mappings: [{ provider: 'coingecko', provider_symbol: 'coin-a', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing' }] }, setup().instruments[1]] })
    const fetchMock = vi.fn(() => json(committedSetup))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    expect(screen.getByText(/1 of 2 mappings confirmed/)).toBeTruthy()
    expect(screen.getByText(/Finish SAME \(Second company\)/)).toBeTruthy()
    const committedRow = screen.getAllByRole('row')[1]
    fireEvent.change(within(committedRow).getByPlaceholderText('Exact listing / coin ID'), { target: { value: 'changed-id' } })
    expect((within(committedRow).getByRole('checkbox') as HTMLInputElement).checked).toBe(false)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('keeps a validated mapping save visible when the follow-up setup read fails', async () => {
    const savedSetup = setup({ instruments: [setup().instruments[0]] })
    const outcome = { instrument_id: 'authority-a', status: 'saved', mapping: { id: 'mapping-a', instrument_id: 'authority-a', provider: 'coingecko', provider_symbol: 'coin-a', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'provider listing', created_at: '2026-01-01T00:00:00.000000000Z' } }
    let setupReads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/market-data/mappings')) return json({ status: 'saved', etag: '"setup-2"', outcomes: [outcome] })
      if (url.endsWith('/market-data')) { setupReads += 1; return setupReads === 1 ? json(savedSetup) : json({ error: 'read failed' }, 503) }
      return json({ portfolio_id: 'portfolio-a', etag: '"setup-1"', results: [{ instrument_id: 'authority-a', status: 'no_result', attempted_providers: ['coingecko'], candidates: [], diagnostic: 'none' }] })
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(1))
    completeMapping(screen.getAllByRole('row')[1])
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByText('Mappings saved')).toBeTruthy())
    expect(screen.getByText(/Prices have not been refreshed/)).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toMatch(/Could not reload Market Data|Reload setup/)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/mappings'))).toHaveLength(1)
  })

  it('requires an explicit adoption choice after a stale setup conflict', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/market-data/mappings') ? json({ error: 'stale' }, 412) : json(setup()))
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    completeMapping(screen.getAllByRole('row')[1])
    completeMapping(screen.getAllByRole('row')[2], 1)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/Reload setup/))
    fireEvent.click(screen.getByRole('button', { name: 'Reload setup' }))
    await waitFor(() => expect(screen.getByText(/Keep editing with current setup/)).toBeTruthy())
    expect(screen.getByText(/Discard edits and reload/)).toBeTruthy()
    const readsBefore = fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length
    fireEvent.click(screen.getByRole('button', { name: /Keep editing with current setup/ }))
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBe(readsBefore)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/changed in another window/i))
    fireEvent.click(screen.getByRole('button', { name: 'Reload setup' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Discard edits and reload' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Discard edits and reload' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data')).length).toBeGreaterThan(readsBefore))
  })

  it('keeps stale recovery locked until its validated GET completes', async () => {
    let resolveRead!: (response: Response) => void
    let reads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/market-data/mappings')) return json({ error: 'stale' }, 412)
      reads += 1
      if (reads === 1) return json(setup())
      return new Promise<Response>((resolve) => { resolveRead = resolve })
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    completeMapping(screen.getAllByRole('row')[1])
    completeMapping(screen.getAllByRole('row')[2], 1)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Reload setup' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Reload setup' }))
    expect(screen.queryByRole('button', { name: 'Keep editing with current setup' })).toBeNull()
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    resolveRead(json(setup()) as unknown as Response)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Keep editing with current setup' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Keep editing with current setup' }))
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('keeps stale recovery locked after a failed GET and generic retry', async () => {
    let reads = 0
    let failNextRead = false
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/market-data/mappings')) return json({ error: 'stale' }, 412)
      reads += 1
      if (failNextRead) { failNextRead = false; return json({ error: 'temporary read outage' }, 503) }
      return json(setup())
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    completeMapping(screen.getAllByRole('row')[1])
    completeMapping(screen.getAllByRole('row')[2], 1)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Reload setup' })).toBeTruthy())
    failNextRead = true
    fireEvent.click(screen.getByRole('button', { name: 'Reload setup' }))
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Reload setup' }).length).toBeGreaterThan(1))
    expect(screen.queryByRole('button', { name: 'Keep editing with current setup' })).toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: 'Reload setup' })[0])
    await waitFor(() => expect(screen.getByRole('button', { name: 'Keep editing with current setup' })).toBeTruthy())
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Keep editing with current setup' }))
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(false)
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/market-data/mappings'))).toHaveLength(1)
  })

  it('disables draft adoption when reconciliation changes the active instrument set', async () => {
    let reads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/market-data/mappings')) return json({ error: 'stale' }, 412)
      reads += 1
      return reads === 1 ? json(setup()) : json(setup({ instruments: [setup().instruments[0]] }))
    })
    renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    completeMapping(screen.getAllByRole('row')[1])
    completeMapping(screen.getAllByRole('row')[2], 1)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Reload setup' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Reload setup' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Keep editing with current setup' })).toBeTruthy())
    expect((screen.getByRole('button', { name: 'Keep editing with current setup' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Save all mappings' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('aborts a pending write on portfolio replacement and ignores its late response', async () => {
    let resolveSave!: (response: Response) => void
    let writeSignal: AbortSignal | null | undefined
    const replacementSetup = setup({ portfolio_id: 'portfolio-b', instruments: [] })
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/market-data/mappings')) return new Promise<Response>((resolve) => { resolveSave = resolve; writeSignal = init?.signal })
      return url.includes('/portfolio-b/') ? json(replacementSetup) : json(setup())
    })
    const rendered = renderPanel(fetchMock)
    await waitFor(() => expect(mappingComboboxes()).toHaveLength(2))
    completeMapping(screen.getAllByRole('row')[1])
    completeMapping(screen.getAllByRole('row')[2], 1)
    fireEvent.click(screen.getByRole('button', { name: 'Save all mappings' }))
    await waitFor(() => expect(writeSignal).toBeTruthy())
    rendered.rerender(<MarketDataPanel portfolioID="portfolio-b" reportingCurrency="MYR" csrfToken="replacement-csrf" onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(writeSignal?.aborted).toBe(true))
    resolveSave(json({ status: 'saved', etag: '"replacement"', outcomes: [] }) as unknown as Response)
    await waitFor(() => expect(screen.getByText(/No active instruments to map/)).toBeTruthy())
    expect(screen.queryByText('Mappings saved')).toBeNull()
  })

  it('explains zero active instruments and unconfigured providers without credential controls', async () => {
    const fetchMock = vi.fn(() => json(setup({ providers: [{ name: 'finnhub', configured: false, public: false }], instruments: [] })))
    renderPanel(fetchMock)
    await waitFor(() => expect(screen.getByText(/No active instruments to map/)).toBeTruthy())
    expect(screen.getByText(/Add a holding in Holdings first/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: /credential|api key|secret/i })).toBeNull()
    expect(screen.queryByRole('textbox')).toBeNull()
  })
})
