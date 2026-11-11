import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ThesisEditor, { validateSource, type Thesis } from './ThesisEditor'
import type { Holding } from './App'
import { hasMatchingDeclaration, installCommittedStyles } from './cssTestUtils'

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
  it('accepts UTC RFC3339Nano timestamps with zero through nine fractional digits on the runtime response path', async () => {
    for (const updatedAt of [
      '2026-01-02T12:00:00Z',
      '2026-01-02T12:00:00.1Z',
      '2026-01-02T12:00:00.1234Z',
      '2026-01-02T12:00:00.123456789Z',
    ]) {
      cleanup()
      const fetchMock = vi.fn(() => response(thesis({ updated_at: updatedAt })))
      renderEditor(fetchMock)
      await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
      expect(screen.queryByRole('alert')).toBeNull()
    }
  })

  it('rejects invalid UTC RFC3339Nano timestamps on the runtime response path', async () => {
    for (const updatedAt of [
      '2026-02-29T12:00:00Z',
      '2026-01-02T12:00:00+00:00',
      '2026-01-02T12:00:00.1234567890Z',
      ' 2026-01-02T12:00:00.123Z',
      '2026-01-02T12:00:60.123Z',
    ]) {
      cleanup()
      const fetchMock = vi.fn(() => response(thesis({ updated_at: updatedAt })))
      renderEditor(fetchMock)
      await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis response/i))
    }
  })

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

  it('restores an interrupted history read to an explicit retry after protected exit', async () => {
    let resolveHistory: ((value: Response) => void) | undefined
    let resolveSave: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/history')) return new Promise<Response>((resolve) => { resolveHistory = resolve })
      if (init?.method === 'PUT') return new Promise<Response>((resolve) => { resolveSave = resolve })
      return response(thesis())
    })
    const onClose = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    render(<ThesisEditor portfolioId="portfolio-default" holding={holding} csrfToken="csrf" onClose={onClose} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Loading history…' })).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Pending draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(resolveSave).toBeTypeOf('function'))
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    expect(screen.getByRole('button', { name: 'Keep editing' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }))
    expect(screen.getByRole('button', { name: 'View revision history' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Loading history…' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/history'))).toHaveLength(2))
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

  it.each([
    [400, 'The save was rejected because one or more fields are invalid.'],
    [403, 'You are not allowed to save the thesis.'],
    [404, 'This lot or thesis is no longer available.'],
  ])('preserves every edited field and retries a %s save', async (status, message) => {
    let putAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        putAttempts += 1
        return putAttempts === 1 ? response({}, status) : response(thesis({ revision: 2, revision_id: 'revision-b', summary: 'Canonical summary' }))
      }
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Edited summary' } })
    fireEvent.change(screen.getByLabelText('Intended holding period'), { target: { value: 'One year' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain(message.replace('The save was', 'The save the thesis was')))
    expect(screen.getByDisplayValue('Edited summary')).toBeTruthy()
    expect(screen.getByDisplayValue('One year')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry save' }))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 2/)).toBeTruthy())
    expect(putAttempts).toBe(2)
    expect(screen.getByDisplayValue('Canonical summary')).toBeTruthy()
  })

  it('preserves an edited draft across a network save failure and successful retry', async () => {
    let putAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        putAttempts += 1
        return putAttempts === 1 ? Promise.reject(new Error('network unavailable')) : response(thesis({ revision: 2, revision_id: 'revision-b', summary: 'Saved after retry' }))
      }
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Network draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/network unavailable/i))
    expect(screen.getByDisplayValue('Network draft')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry save' }))
    await waitFor(() => expect(screen.getByDisplayValue('Saved after retry')).toBeTruthy())
  })

  it('replays an isolated committed thesis save without duplicating history', async () => {
    const requests: RequestInit[] = []
    const baseline = thesis()
    const committed = thesis({ revision: 2, revision_id: 'revision-retry', summary: 'Recovered canonical', updated_at: '2026-01-03T12:00:00.000000000Z' })
    const historyItems: Array<Thesis & { created_at: string }> = []
    let firstBody = ''
    let putAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/history')) return response({ items: historyItems })
      if (init?.method === 'PUT') {
        requests.push(init)
        putAttempts += 1
        const body = String(init.body)
        if (putAttempts === 1) {
          firstBody = body
          historyItems.push({ ...baseline, created_at: '2026-01-02T12:00:00.000000000Z' })
          historyItems.push({ ...committed, created_at: '2026-01-03T12:00:00.000000000Z' })
          return Promise.reject(new Error('response unreadable after commit'))
        }
        expect(body).toBe(firstBody)
        return response(committed)
      }
      return response(baseline)
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Unconfirmed draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/save not confirmed.*unchanged save/i))
    expect(historyItems).toHaveLength(2)
    expect((screen.getByRole('button', { name: 'Retry unchanged save' }) as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Retry unchanged save' }))
    await waitFor(() => expect(screen.getByDisplayValue('Recovered canonical')).toBeTruthy())
    expect(requests).toHaveLength(2)
    expect(requests[1]?.body).toBe(requests[0]?.body)
    expect(JSON.parse(String(requests[0]?.body))).toMatchObject({ summary: 'Unconfirmed draft', expected_revision: 1 })
    expect(new Headers(requests[0]?.headers as HeadersInit).get('X-CSRF-Token')).toBe('csrf')
    expect(new Headers(requests[1]?.headers as HeadersInit).get('X-CSRF-Token')).toBe('csrf')
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Revision 2' })).toBeTruthy())
    expect(screen.getAllByRole('heading', { name: 'Revision 2' })).toHaveLength(1)
    expect(historyItems.map((item) => item.revision)).toEqual([1, 2])
  })

  it('explains uncertain exit without claiming the server write was absent and keeps retry enabled', async () => {
    const onClose = vi.fn()
    let rejectSave: ((reason: unknown) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') return new Promise<Response>((_resolve, reject) => { rejectSave = reject })
      return response(thesis())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ThesisEditor portfolioId="portfolio-default" holding={holding} csrfToken="csrf" onClose={onClose} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Possibly committed' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    rejectSave?.(new Error('response unreadable'))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Retry unchanged save' }) as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    const exitAlert = screen.getByText(/This thesis write is not confirmed/i)
    expect(exitAlert.textContent).toMatch(/not confirmed|may already be recorded/i)
    expect(exitAlert.textContent).toMatch(/local recovery draft|cannot undo/i)
    expect(screen.getByRole('button', { name: 'Discard changes and leave' })).toBeTruthy()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('offers explicit keep-editing and discard choices for dirty close', async () => {
    const onClose = vi.fn()
    vi.stubGlobal('fetch', vi.fn(() => response(thesis())))
    render(<ThesisEditor portfolioId="portfolio-default" holding={holding} csrfToken="csrf" onClose={onClose} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Keep this draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    expect(screen.getByRole('button', { name: 'Keep editing' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Discard changes and leave' })).toBeTruthy()
    const keepEditing = screen.getByRole('button', { name: 'Keep editing' })
    keepEditing.focus()
    expect(document.activeElement).toBe(keepEditing)
    fireEvent.click(keepEditing)
    expect(screen.getByDisplayValue('Keep this draft')).toBeTruthy()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: /Investment thesis/ })))
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Close thesis' }))
    fireEvent.click(screen.getByRole('button', { name: 'Discard changes and leave' }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('aborts old history and save lanes when a replacement lot wins', async () => {
    const lotB: Holding = { ...holding, lot_id: 'lot-b', symbol: 'BBB', name: 'BBB Fund' }
    const historyResolvers: Array<(value: Response) => void> = []
    const saveResolvers: Array<(value: Response) => void> = []
    const currentResolvers: Array<(value: Response) => void> = []
    const currentSignals: AbortSignal[] = []
    const historySignals: AbortSignal[] = []
    const saveSignals: AbortSignal[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/history')) {
        historySignals.push(init?.signal as AbortSignal)
        return new Promise<Response>((resolve) => { historyResolvers.push(resolve) })
      }
      if (init?.method === 'PUT') {
        saveSignals.push(init.signal as AbortSignal)
        return new Promise<Response>((resolve) => { saveResolvers.push(resolve) })
      }
      currentSignals.push(init?.signal as AbortSignal)
      return new Promise<Response>((resolve) => { currentResolvers.push(resolve) })
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ThesisEditor portfolioId="portfolio-default" holding={holding} csrfToken="csrf" onClose={vi.fn()} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(currentResolvers).toHaveLength(1))
    currentResolvers[0]!({ ok: true, status: 200, json: async () => thesis() } as Response)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(historyResolvers).toHaveLength(1))
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Old lot draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(saveResolvers).toHaveLength(1))

    view.unmount()
    render(<ThesisEditor portfolioId="portfolio-default" holding={lotB} csrfToken="csrf" onClose={vi.fn()} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(currentResolvers).toHaveLength(2))
    expect(historyResolvers[0]).toBeTypeOf('function')
    expect(saveResolvers[0]).toBeTypeOf('function')
    expect(currentSignals[0]!.aborted).toBe(true)
    expect(historySignals[0]!.aborted).toBe(true)
    expect(saveSignals[0]!.aborted).toBe(true)
    expect(currentResolvers).toHaveLength(2)
    expect(historyResolvers).toHaveLength(1)
    expect(saveResolvers).toHaveLength(1)

    // The replacement must win before any old lane is allowed to settle.
    currentResolvers[1]!({ ok: true, status: 200, json: async () => thesis({ holding_id: 'lot-b', instrument_id: 'instrument-b', id: 'thesis-b', summary: 'Replacement lot thesis' }) } as Response)
    await waitFor(() => expect(screen.getByDisplayValue('Replacement lot thesis')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Replacement saved draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(saveResolvers).toHaveLength(2))
    saveResolvers[1]!({ ok: true, status: 200, json: async () => thesis({ holding_id: 'lot-b', instrument_id: 'instrument-b', id: 'thesis-b', revision: 2, revision_id: 'revision-b-2', summary: 'Replacement canonical' }) } as Response)
    await waitFor(() => expect(screen.getByDisplayValue('Replacement canonical')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(historyResolvers).toHaveLength(2))
    historyResolvers[1]!({ ok: true, status: 200, json: async () => ({ items: [{ ...thesis({ holding_id: 'lot-b', instrument_id: 'instrument-b', id: 'thesis-b', revision: 2, revision_id: 'history-b-2', summary: 'Replacement history' }), created_at: '2026-01-03T12:00:00.000000000Z' }] }) } as Response)
    await waitFor(() => expect(screen.getByText('Replacement history')).toBeTruthy())

    // Only now settle the original lot's stale lanes; none may change the
    // replacement's current document, saved draft, or history selection.
    historyResolvers[0]!({ ok: true, status: 200, json: async () => ({ items: [{ ...thesis(), created_at: thesis().updated_at }] }) } as Response)
    saveResolvers[0]!({ ok: true, status: 200, json: async () => thesis({ summary: 'Stale old save', holding_id: 'lot-a' }) } as Response)
    await waitFor(() => expect(screen.getByText('Replacement history')).toBeTruthy())
    expect(screen.queryByDisplayValue('Stale old save')).toBeNull()
    expect(screen.getByRole('heading', { name: /Investment thesis · BBB/i })).toBeTruthy()
  })

  it('lets a replacement current read win before the stale original read settles', async () => {
    const lotB: Holding = { ...holding, lot_id: 'lot-b', symbol: 'BBB', name: 'BBB Fund' }
    const currentResolvers: Array<(value: Response) => void> = []
    const currentSignals: AbortSignal[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/holdings/lot-a/thesis') || url.endsWith('/holdings/lot-b/thesis')) {
        currentSignals.push(init?.signal as AbortSignal)
        return new Promise<Response>((resolve) => { currentResolvers.push(resolve) })
      }
      return response(thesis())
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ThesisEditor portfolioId="portfolio-default" holding={holding} csrfToken="csrf" onClose={vi.fn()} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(currentResolvers).toHaveLength(1))
    view.rerender(<ThesisEditor portfolioId="portfolio-default" holding={lotB} csrfToken="csrf" onClose={vi.fn()} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(currentResolvers).toHaveLength(2))
    expect(currentResolvers[0]).toBeTypeOf('function')
    expect(currentResolvers[1]).toBeTypeOf('function')
    expect(currentSignals[0]?.aborted).toBe(true)
    currentResolvers[1]!({ ok: true, status: 200, json: async () => thesis({ holding_id: 'lot-b', instrument_id: 'instrument-b', id: 'thesis-b', summary: 'Replacement current' }) } as Response)
    await waitFor(() => expect(screen.getByDisplayValue('Replacement current')).toBeTruthy())
    currentResolvers[0]!({ ok: true, status: 200, json: async () => thesis({ summary: 'Stale original current' }) } as Response)
    await waitFor(() => expect(screen.getByDisplayValue('Replacement current')).toBeTruthy())
    expect(screen.queryByDisplayValue('Stale original current')).toBeNull()
    expect(screen.getByRole('heading', { name: /Investment thesis · BBB/i })).toBeTruthy()
  })

  it.each([
    ['network', () => Promise.reject(new Error('current network failure'))],
    ['forbidden', () => response({}, 403)],
  ])('retries a %s current-load failure without discarding the next response', async (_label, failure) => {
    let currentAttempts = 0
    const fetchMock = vi.fn(() => {
      currentAttempts += 1
      return currentAttempts === 1 ? failure() : response(thesis({ summary: 'Recovered current thesis' }))
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.getByDisplayValue('Recovered current thesis')).toBeTruthy())
    expect(currentAttempts).toBe(2)
  })

  it.each([
    ['network', () => Promise.reject(new Error('history network failure'))],
    ['forbidden', () => response({}, 403)],
  ])('retries a %s history failure', async (_label, failure) => {
    let historyAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith('/history')) {
        historyAttempts += 1
        return historyAttempts === 1 ? failure() : response({ items: [] })
      }
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.getByText(/No revision history/)).toBeTruthy())
    expect(historyAttempts).toBe(2)
  })

  it('renders a 404 history response as an empty immutable state', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/history') ? response({}, 404) : response(thesis()))
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByText(/No revision history/)).toBeTruthy())
    expect(screen.getByRole('button', { name: 'Refresh history' })).toBeTruthy()
  })

  it('rejects malformed current, save, and history payloads without exposing editable content', async () => {
    const malformedCurrentCases: Array<[string, Record<string, unknown>]> = [
      ['missing identifier', (() => { const value = { ...thesis() } as Record<string, unknown>; delete value.id; return value })()],
      ['extra field', { ...thesis(), unexpected: true }],
      ['empty identifier', { ...thesis(), id: '' }],
      ['empty summary', { ...thesis(), summary: '' }],
      ['empty intended period', { ...thesis(), intended_holding_period: '' }],
      ['overlength identifier', { ...thesis(), id: 'x'.repeat(129) }],
      ['overlength text', { ...thesis(), summary: 'x'.repeat(4001) }],
      ['null list', { ...thesis(), risks: null }],
      ['too many list items', { ...thesis(), risks: Array.from({ length: 33 }, () => 'risk') }],
      ['empty list item', { ...thesis(), risks: [''] }],
      ['overlength list item', { ...thesis(), risks: ['x'.repeat(1001)] }],
      ['too many sources', { ...thesis(), sources: Array.from({ length: 17 }, () => ({ label: 'source', url: 'https://example.com' })) }],
      ['unsafe source URL', { ...thesis(), sources: [{ label: 'unsafe', url: 'javascript:alert(1)' }] }],
      ['impossible source date', { ...thesis(), sources: [{ label: 'bad date', url: 'https://example.com', publication_date: '2023-02-29' }] }],
      ['malformed allocation', { ...thesis(), allocation_limit: '100.01' }],
      ['malformed revision', { ...thesis(), revision: 1.5 }],
    ]
    for (const [label, malformed] of malformedCurrentCases) {
      cleanup()
      const fetchMock = vi.fn(() => response(malformed))
      renderEditor(fetchMock)
      await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis/i), { timeout: 1000 })
      expect(screen.queryByDisplayValue('Existing summary'), label).toBeNull()
    }
  })

  it('rejects malformed save and history documents without replacing the editable draft', async () => {
    const malformedSave = { ...thesis({ summary: 'Malformed save' }), unexpected: true }
    const malformedHistory = { ...thesis(), created_at: thesis().updated_at, unexpected: true }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') return response(malformedSave)
      if (String(input).endsWith('/history')) return response({ items: [malformedHistory] })
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Draft survives malformed save' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis response/i))
    expect(screen.getByDisplayValue('Draft survives malformed save')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis/i))
    expect(screen.queryByText('Malformed save')).toBeNull()
  })

  it.each([
    ['duplicate revisions', [
      { ...thesis(), created_at: thesis().updated_at },
      { ...thesis({ revision_id: 'revision-a' }), created_at: thesis().updated_at },
    ]],
    ['out of order revisions', [
      { ...thesis({ revision: 2, revision_id: 'revision-b' }), created_at: thesis().updated_at },
      { ...thesis(), created_at: thesis().updated_at },
    ]],
    ['inconsistent history identity', [
      { ...thesis(), created_at: thesis().updated_at },
      { ...thesis({ id: 'other-thesis', revision: 2, revision_id: 'revision-b' }), created_at: thesis().updated_at },
    ]],
  ])('rejects %s without entering history state', async (_label, items) => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/history') ? response({ items }) : response(thesis()))
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'View revision history' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/malformed thesis/i))
    expect(screen.queryByRole('heading', { name: /^Revision \d+$/ })).toBeNull()
  })

  it('sends exactly one PUT for rapid double submit and remains recoverable', async () => {
    let resolveSave: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => init?.method === 'PUT'
      ? new Promise<Response>((resolve) => { resolveSave = resolve })
      : response(thesis()))
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    fireEvent.click(screen.getByRole('button', { name: 'Saving thesis…' }))
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1)
    resolveSave?.(await response(thesis({ revision: 2, revision_id: 'revision-b' })))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 2/)).toBeTruthy())
    expect(screen.getByRole('button', { name: 'Save thesis' })).toBeTruthy()
  })

  it('uses the canonical revision on the next save and displays the canonical baseline', async () => {
    const requests: RequestInit[] = []
    let putAttempt = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        requests.push(init)
        putAttempt += 1
        return response(thesis({ revision: putAttempt + 1, revision_id: `revision-${putAttempt + 1}`, summary: putAttempt === 1 ? 'Canonical first' : 'Canonical second' }))
      }
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'First draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByDisplayValue('Canonical first')).toBeTruthy())
    fireEvent.change(screen.getByLabelText('Summary'), { target: { value: 'Second draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByDisplayValue('Canonical second')).toBeTruthy())
    expect(JSON.parse(String(requests[1].body))).toMatchObject({ expected_revision: 2, summary: 'Second draft' })
  })

  it('adds, reorders, removes and submits dynamic lists and sources as exact arrays', async () => {
    const initial = thesis({
      reasons_to_hold: ['Hold first', 'Hold second'],
      sources: [
        { label: 'First source', url: 'https://example.com/first', publication_date: '2024-01-01' },
        { label: 'Second source', url: 'https://example.com/second', publication_date: '2024-02-02' },
      ],
    })
    const savePayloads: Record<string, unknown>[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        const payload = JSON.parse(String(init.body)) as Record<string, unknown>
        savePayloads.push(payload)
        return response(thesis({
          revision: savePayloads.length + 1,
          revision_id: `revision-${savePayloads.length + 1}`,
          summary: 'Canonical dynamic arrays',
          intended_holding_period: payload.intended_holding_period as string,
          allocation_limit: payload.allocation_limit as string | null,
          reasons_to_hold: payload.reasons_to_hold as string[],
          risks: payload.risks as string[],
          reasons_to_sell: payload.reasons_to_sell as string[],
          conditions_to_change: payload.conditions_to_change as string[],
          sources: payload.sources as Thesis['sources'],
        }))
      }
      return response(initial)
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Hold first')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Move Reasons to hold 1 down' }))
    await waitFor(() => expect(document.activeElement?.getAttribute('id')).toMatch(/^reasons-to-hold-/))
    fireEvent.click(screen.getByRole('button', { name: 'Add reasons to hold' }))
    await waitFor(() => expect(document.activeElement?.getAttribute('id')).toMatch(/^reasons-to-hold-/))
    fireEvent.change(document.activeElement as HTMLTextAreaElement, { target: { value: 'Hold third' } })
    fireEvent.click(screen.getByRole('button', { name: 'Move source 1 down' }))
    await waitFor(() => expect(screen.getByDisplayValue('First source')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Add source' }))
    await waitFor(() => expect(document.activeElement?.getAttribute('id')).toMatch(/^source-label-/))
    fireEvent.change(document.activeElement as HTMLInputElement, { target: { value: 'Third source' } })
    fireEvent.change(screen.getByLabelText('Source 3 URL'), { target: { value: 'https://example.com/third' } })
    fireEvent.change(screen.getByLabelText('Source 3 publication date (optional)'), { target: { value: '2024-03-03' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 2/)).toBeTruthy())
    expect(savePayloads[0]).toMatchObject({
      reasons_to_hold: ['Hold second', 'Hold first', 'Hold third'],
      sources: [
        { label: 'Second source', url: 'https://example.com/second', publication_date: '2024-02-02' },
        { label: 'First source', url: 'https://example.com/first', publication_date: '2024-01-01' },
        { label: 'Third source', url: 'https://example.com/third', publication_date: '2024-03-03' },
      ],
    })
    for (const label of ['Remove Reasons to hold 1', 'Remove Reasons to hold 1', 'Remove Reasons to hold 1']) fireEvent.click(screen.getByRole('button', { name: label }))
    for (const label of ['Remove source 1', 'Remove source 1', 'Remove source 1']) fireEvent.click(screen.getByRole('button', { name: label }))
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(screen.getByText(/Thesis saved as revision 3/)).toBeTruthy())
    expect(savePayloads[1]).toMatchObject({ reasons_to_hold: [], sources: [] })
    expect(savePayloads[1]).not.toMatchObject({ reasons_to_hold: null, sources: null })
  })

  it('focuses the heading on open and focuses the conflict alert for an explicit reload', async () => {
    let resolveSave: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') return new Promise<Response>((resolve) => { resolveSave = resolve })
      return response(thesis())
    })
    renderEditor(fetchMock)
    await waitFor(() => expect(screen.getByDisplayValue('Existing summary')).toBeTruthy())
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: /Investment thesis/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Save thesis' }))
    await waitFor(() => expect(resolveSave).toBeTruthy())
    resolveSave?.(await response({}, 409))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/newer revision exists/i))
    expect(document.activeElement).toBe(screen.getByRole('alert'))
    expect(screen.getByRole('button', { name: 'Reload latest revision' })).toBeTruthy()
  })

  it('binds real thesis inputs, textareas, and disabled submit to scoped legacy rules', async () => {
    const removeStyles = installCommittedStyles()
    let resolveSave: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => init?.method === 'PUT'
      ? new Promise<Response>((resolve) => { resolveSave = resolve })
      : response(thesis()))
    try {
      renderEditor(fetchMock)
      const summary = await waitFor(() => screen.getByLabelText('Summary'))
      const period = screen.getByLabelText('Intended holding period')
      const save = screen.getByRole('button', { name: 'Save thesis' })
      expect(hasMatchingDeclaration(summary, 'background', '#172322')).toBe(true)
      expect(hasMatchingDeclaration(period, 'background', '#172322')).toBe(true)
      summary.focus()
      expect(hasMatchingDeclaration(summary, 'outline', '3px', 'focus-visible')).toBe(true)
      fireEvent.click(save)
      await waitFor(() => expect(resolveSave).toBeTypeOf('function'))
      expect((save as HTMLButtonElement).disabled).toBe(true)
      expect(hasMatchingDeclaration(save, 'opacity', '.65', 'disabled')).toBe(true)
      resolveSave?.(await response(thesis({ revision: 2, revision_id: 'revision-b' })))
    } finally {
      removeStyles()
    }
  })

  it('keeps long valid thesis content in shrink-safe semantic regions at the 320px contract boundary', async () => {
    const longThesis = thesis({
      summary: 'S'.repeat(4000),
      intended_holding_period: 'P'.repeat(256),
      reasons_to_hold: ['R'.repeat(1000)],
      risks: ['K'.repeat(1000)],
      reasons_to_sell: ['E'.repeat(1000)],
      conditions_to_change: ['C'.repeat(1000)],
      sources: [{ label: 'L'.repeat(256), url: `https://example.com/${'u'.repeat(2000)}`, publication_date: '2024-02-29' }],
    })
    renderEditor(vi.fn(() => response(longThesis)))
    await waitFor(() => expect(screen.getByDisplayValue(longThesis.summary)).toBeTruthy())
    const surface = document.querySelector('.thesis-surface')
    expect(surface).toBeTruthy()
    expect(surface?.querySelector('.thesis-form')).toBeTruthy()
    expect(surface?.querySelector('.thesis-list-fieldset')).toBeTruthy()
    expect(surface?.querySelector('.source-editor')).toBeTruthy()
    expect(surface?.querySelector('textarea[maxlength="4000"]')).toBeTruthy()
    expect(surface?.querySelector('input[maxlength="2048"]')).toBeTruthy()
    expect(screen.getByRole('heading', { name: /Investment thesis/ }).tabIndex).toBe(-1)
  })
})
