import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App, { compareDecimalStrings, type PortfolioOverview } from './App'

const completeOverview: PortfolioOverview = {
  portfolio: { id: 'portfolio-default', name: 'Personal Portfolio', default_reporting_currency: 'MYR' },
  reporting_currency: 'MYR',
  snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00Z', calculation_version: 'test.v1', reporting_total: '100.5', subtotals: [] },
  holdings: [
    { lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', description: 'Distinct AAA instrument description', quantity: '2', currency: 'USD', unit_cost: '5', cost_basis: '10', latest_price: '50.5', latest_value: '101', reporting_value: '100.5', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false },
    { lot_id: 'lot-b', symbol: 'BBB', name: 'BBB Fund', quantity: '10', currency: 'SGD', unit_cost: '1', latest_price: '2', latest_value: '20', reporting_value: '20', freshness: { status: 'stale' }, data_quality: ['stale_dependency'], missing_broker: true, missing_acquisition_date: true },
  ],
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

async function flushEffects() {
  await act(async () => {
    for (let index = 0; index < 10; index += 1) await Promise.resolve()
  })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('Portfolio Dashboard', () => {
  it('shows a loading state while the typed API request is pending', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => undefined)))
    render(<App />)
    expect(screen.getByRole('status').textContent).toMatch(/loading portfolio/i)
  })

  it('renders complete snapshots, flags, filtering and currency changes', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const response = url.includes('currency=USD') ? { ...completeOverview, reporting_currency: 'USD' } : completeOverview
      return Promise.resolve({ ok: true, json: async () => response } as Response)
    })
    vi.stubGlobal('fetch', authenticatedFetch(fetchMock))
    render(<App />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Personal Portfolio' })).toBeTruthy())
    expect(screen.getByText('100.5 MYR')).toBeTruthy()
    expect(screen.getByText(/Distinct AAA instrument description/)).toBeTruthy()
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
    await waitFor(() => expect(screen.getByText(/Reporting in USD/i)).toBeTruthy())
    resolveMYR?.({ ok: true, json: async () => completeOverview } as Response)
    await waitFor(() => expect(screen.getByText(/Reporting in USD/i)).toBeTruthy())
  })

  it('renders explicit incomplete and no-snapshot states without a fabricated total', async () => {
    const incomplete = { ...completeOverview, snapshot: { ...completeOverview.snapshot, state: 'incomplete' as const, complete: false, reporting_total: undefined } }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => incomplete } as Response)))
    render(<App />)
    await waitFor(() => expect(screen.getByText(/valuation is incomplete/i)).toBeTruthy())
    expect(screen.queryByText('100.5 MYR')).toBeNull()
    const noSnapshot = { ...completeOverview, snapshot: { state: 'no_snapshot' as const, complete: false, subtotals: [] }, holdings: [] }
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => noSnapshot } as Response)))
    cleanup()
    render(<App />)
    await waitFor(() => expect(screen.getByText(/no immutable snapshot yet/i)).toBeTruthy())
  })

  it('renders accessible complete instrument and source-currency allocation detail', async () => {
    vi.stubGlobal('fetch', authenticatedFetch(() => Promise.resolve({ ok: true, json: async () => allocationOverview } as Response)))
    render(<App />)
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
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Allocation' })).toBeTruthy())
    expect(screen.getAllByText('80.25 · 80.25%').length).toBeGreaterThan(0)
    fireEvent.change(screen.getByLabelText(/reporting currency/i), { target: { value: 'USD' } })
    await waitFor(() => expect(screen.getAllByText('40.25 · 80.10%').length).toBeGreaterThan(0))
    expect(screen.getAllByText('Value (USD)').length).toBe(2)
    expect(screen.queryByText('80.25 · 80.25%')).toBeNull()
    expect(screen.getAllByText('MYR').length).toBeGreaterThan(0)
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

  it('loads safe authenticated activity with bounded load-more pagination', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/activity') && url.includes('cursor=Y3Vyc29yLW9uZS0xMjM')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-two', portfolio_id: 'portfolio-default', action: 'portfolio.snapshot.created', category: 'portfolio', actor_category: 'system', occurred_at: '2026-01-02T12:00:00Z', summary: 'Immutable snapshot created' }], has_more: false }) } as Response)
      if (url.includes('/activity')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T11:00:00Z', summary: 'Manual quote added', details: 'A safe summary' }], next_cursor: 'Y3Vyc29yLW9uZS0xMjM', has_more: true }) } as Response)
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
        if (activityCalls === 1) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T11:00:00Z', summary: 'Manual quote added' }], next_cursor: cursor, has_more: true }) } as Response)
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
      if (url.includes('/activity') && url.includes('cursor=')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T06:00:00-05:00', summary: 'Duplicate row' }, { id: 'activity-two', portfolio_id: 'portfolio-default', action: 'market.fx.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T10:00:00Z', summary: 'Older row' }], has_more: false }) } as Response)
      if (url.includes('/activity')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{ id: 'activity-one', portfolio_id: 'portfolio-default', action: 'market.quote.added', category: 'market', actor_category: 'administrative', occurred_at: '2026-01-02T12:00:00Z', summary: 'Newest row' }], next_cursor: cursor, has_more: true }) } as Response)
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
})
