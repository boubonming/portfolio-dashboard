import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ThesisEditor, { validateSource, type Thesis } from './ThesisEditor'
import type { Holding } from './App'

const holding: Holding = {
  lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', quantity: '1', currency: 'USD',
  freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false,
}

const thesis = (overrides: Partial<Thesis> = {}): Thesis => ({
  id: 'thesis-a', portfolio_id: 'portfolio-default', holding_id: 'lot-a', instrument_id: 'instrument-a',
  summary: 'Existing summary', intended_holding_period: 'Long term', allocation_limit: null,
  reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [],
  revision: 1, revision_id: 'revision-a', updated_at: '2026-01-02T12:00:00.000000000Z', ...overrides,
})

const response = (body: unknown, status = 200) => Promise.resolve({ ok: status >= 200 && status < 300, status, json: async () => body } as Response)

function renderEditor(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal('fetch', fetchMock)
  return render(<ThesisEditor portfolioId="portfolio-default" holding={holding} csrfToken="csrf" onClose={vi.fn()} onUnauthorized={vi.fn()} />)
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('ThesisEditor response contracts', () => {
  it('rejects unsafe or malformed source URLs before they can be rendered', () => {
    for (const url of ['javascript:alert(1)', 'data:text/html,unsafe', 'ftp://example.com/file', 'https://', 'https://user:password@example.com', 'https://example.com/a\r\nb']) {
      expect(() => validateSource({ label: 'Source', url })).toThrow()
    }
    expect(validateSource({ label: 'Source', url: 'https://example.com/path', publication_date: '2024-02-29' }).url).toBe('https://example.com/path')
    expect(() => validateSource({ label: 'Source', url: 'https://example.com', publication_date: '2023-02-29' })).toThrow()
  })

  it('fails closed when the current response belongs to another lot', async () => {
    const fetchMock = vi.fn(() => response(thesis({ holding_id: 'lot-b', summary: 'Wrong lot' })))
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis response/i))
    expect(screen.queryByDisplayValue('Wrong lot')).toBeNull()
  })

  it('rejects wrong-scope save and history responses without adopting either document', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/history')) return response({ items: [{ ...thesis(), holding_id: 'lot-b', created_at: thesis().updated_at }] })
      if (init?.method === 'PUT') return response(thesis({ holding_id: 'lot-b', summary: 'Wrong save' }))
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Unsaved draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis response/i))
    expect(screen.getByDisplayValue('Unsaved draft')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis response/i))
    expect(screen.queryByText('Wrong save')).toBeNull()
  })

  it('keeps history in its own async lane while a save is pending', async () => {
    let resolveHistory: ((value: Response) => void) | undefined
    let resolveSave: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/history')) return new Promise<Response>((resolve) => { resolveHistory = resolve })
      if (init?.method === 'PUT') return new Promise<Response>((resolve) => { resolveSave = resolve })
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Loading history…' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(resolveSave).toBeTruthy())
    expect(screen.getByRole('button', { name: 'Loading history…' }).hasAttribute('disabled')).toBe(true)
    resolveSave?.(await response(thesis({ revision: 2, revision_id: 'revision-b', summary: 'Saved summary' })))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 2/i)).toBeTruthy())
    resolveHistory?.(await response({ items: [] }))
    await waitFor(() => expect(screen.getByText(/No revision history/i)).toBeTruthy())
  })

  it('focuses a new row and moves focus to a predictable target after removal', async () => {
    const fetchMock = vi.fn(() => response({ ...thesis(), revision: 1, summary: 'Ready' }))
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Ready')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Add reasons to hold' }))
    await waitFor(() => expect(document.activeElement?.id).toMatch(/^reasons-to-hold-/))
    fireEvent.click(screen.getByRole('button', { name: 'Remove Reasons to hold 1' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add reasons to hold' })).toBe(document.activeElement))
  })

  it('uses human-readable history headings rather than internal DOM IDs', async () => {
    const current = thesis()
    const revision = { ...current, created_at: current.updated_at }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/history') ? response({ items: [revision] }) : response(current))
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Revision 1' })).toBeTruthy())
    expect(screen.queryByText('history-revision-1')).toBeNull()
    expect(screen.getByRole('heading', { name: /Revision 1 content/i })).toBeTruthy()
  })
})
