import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import HoldingMutations, { validateHoldingMutationReceipt, validatePortfolioETag } from './HoldingMutations'

const holding = {
  lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', quantity: '2', currency: 'USD', unit_cost: '5', cost_basis: '10',
  freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false,
}
const receipt = {
  operation: 'create', result: 'applied', holding: {
    portfolio_id: 'portfolio-default', holding_id: 'lot-new', instrument_id: 'instrument-new', symbol: 'NEW',
    quantity: '2', unit_cost: '5', cost_basis: '10', currency: 'USD', account_id: null,
    acquisition_date: null, lifecycle: 'active', created_at: '2026-01-02T12:00:00.000000000Z', removed_at: null,
  }, portfolio_revision: 8, snapshot_state: 'no_snapshot', current_snapshot_id: null,
  actor: { category: 'browser_user', id: 'local-owner', permissions: ['portfolio:read'] }, audit_event_id: 'audit-1',
  activity_event_id: 'activity-1', idempotency_key: 'create-key-123456',
}

const baseProps = () => ({
  portfolioId: 'portfolio-default', overviewETag: '"portfolio-default-7"', csrfToken: 'csrf-token',
  holdings: [holding], active: true, scopeKey: 'portfolio-default:MYR:overview', onRefresh: vi.fn(async () => '"portfolio-default-8"'), onUnauthorized: vi.fn(),
})

afterEach(() => cleanup())

describe('HoldingMutations', () => {
  it('accepts only a strong canonical ETag bound to the requested portfolio', () => {
    expect(validatePortfolioETag('"portfolio-default-0"', 'portfolio-default')).toBe(0)
    expect(validatePortfolioETag('"portfolio-default-12"', 'portfolio-default')).toBe(12)
    expect(validatePortfolioETag('W/"portfolio-default-12"', 'portfolio-default')).toBeNull()
    expect(validatePortfolioETag('"portfolio-default-012"', 'portfolio-default')).toBeNull()
    expect(validatePortfolioETag('"other-12"', 'portfolio-default')).toBeNull()
    expect(validatePortfolioETag('"portfolio-default-1, portfolio-default-2"', 'portfolio-default')).toBeNull()
  })

  it('submits an inline holding with exact decimal strings and the overview witness', async () => {
    const props = baseProps()
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    fireEvent.change(screen.getByLabelText('Symbol'), { target: { value: 'NEW' } })
    fireEvent.change(screen.getByLabelText('Instrument currency'), { target: { value: 'USD' } })
    fireEvent.change(screen.getByLabelText('Quantity'), { target: { value: '2' } })
    fireEvent.change(screen.getByLabelText('Unit cost'), { target: { value: '5' } })
    fireEvent.change(screen.getByLabelText('Cost basis'), { target: { value: '10' } })
    fireEvent.change(screen.getByLabelText('Holding currency'), { target: { value: 'USD' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const [, init] = fetchMock.mock.calls[0]
    expect(init?.method).toBe('POST')
    expect(new Headers(init?.headers as HeadersInit).get('If-Match')).toBe('"portfolio-default-7"')
    const payload = JSON.parse(String(init?.body)) as Record<string, unknown>
    expect(payload.quantity).toBe('2')
    expect(payload.unit_cost).toBe('5')
    expect(payload.cost_basis).toBe('10')
    expect(new Headers(init?.headers as HeadersInit).get('X-CSRF-Token')).toBe('csrf-token')
  })

  it('archives the exact lot only after explicit confirmation', async () => {
    const props = baseProps()
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', result: 'applied', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    expect(screen.getByRole('dialog').textContent).toMatch(/logically archive/i)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel archive' }))
    expect(fetchMock).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(String(fetchMock.mock.calls[0][0])).toContain('/holdings/lot-a')
    expect(fetchMock.mock.calls[0][1]?.method).toBe('DELETE')
  })

  it('invalidates a retained archive handler synchronously after a 401 logout', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    const rendered = render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    const retainedConfirm = screen.getByRole('button', { name: 'Confirm archive lot lot-a' })
    const propsKey = Object.keys(retainedConfirm).find((key) => key.startsWith('__reactProps$'))
    const retainedHandler = propsKey ? (retainedConfirm as unknown as Record<string, { onClick?: () => Promise<void> }>)[propsKey]?.onClick : undefined
    expect(retainedHandler).toBeTypeOf('function')
    fireEvent.click(retainedConfirm)

    await waitFor(() => expect(props.onUnauthorized).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.hasAttribute('aria-hidden')).toBe(false)
    expect(root.inert).toBe(false)
    await retainedHandler?.()
    rendered.rerender(<HoldingMutations {...props} active={false} />)
    rendered.rerender(<HoldingMutations {...props} active />)
    await retainedHandler?.()
    await Promise.resolve()
    expect(fetchMock).toHaveBeenCalledOnce()
    root.remove()
  })

  it('rejects a receipt with unexpected fields', () => {
    expect(() => validateHoldingMutationReceipt({ ...receipt, unexpected: true }, { operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key, body: JSON.stringify({}), lotId: undefined })).toThrow()
  })

  it('reuses the exact uncertain create attempt after the overview ETag changes', async () => {
    const props = baseProps()
    let calls = 0
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_input, init) => {
      calls += 1
      if (calls === 1) return Promise.reject(new TypeError('connection lost'))
      return Promise.resolve({ ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/uncertainty/i))
    const firstInit = fetchMock.mock.calls[0][1]
    const firstKey = new Headers(firstInit?.headers as HeadersInit).get('Idempotency-Key')
    rerender(<HoldingMutations {...props} overviewETag={'"portfolio-default-99"'} />)
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const retryInit = fetchMock.mock.calls[1][1]
    expect(new Headers(retryInit?.headers as HeadersInit).get('Idempotency-Key')).toBe(firstKey)
    expect(new Headers(retryInit?.headers as HeadersInit).get('If-Match')).toBe('"portfolio-default-7"')
    expect(retryInit?.body).toBe(firstInit?.body)
  })

  it('reuses the exact uncertain archive attempt after the overview ETag changes', async () => {
    const props = baseProps()
    let calls = 0
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_input, init) => {
      calls += 1
      if (calls === 1) return Promise.reject(new TypeError('connection lost'))
      return Promise.resolve({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(screen.getByText(/uncertainty/i)).toBeTruthy())
    const firstInit = fetchMock.mock.calls[0][1]
    const firstKey = new Headers(firstInit?.headers as HeadersInit).get('Idempotency-Key')
    rerender(<HoldingMutations {...props} overviewETag={'"portfolio-default-99"'} />)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const retryInit = fetchMock.mock.calls[1][1]
    expect(new Headers(retryInit?.headers as HeadersInit).get('Idempotency-Key')).toBe(firstKey)
    expect(new Headers(retryInit?.headers as HeadersInit).get('If-Match')).toBe('"portfolio-default-7"')
    expect(retryInit?.body).toBe(firstInit?.body)
  })

  it('contains archive focus, supports Escape, and returns focus to the exact duplicate-symbol lot', () => {
    const props = { ...baseProps(), holdings: [{ ...holding, lot_id: 'lot-a', symbol: 'SAME' }, { ...holding, lot_id: 'lot-b', symbol: 'SAME' }] }
    render(<HoldingMutations {...props} />)
    const lotB = screen.getByRole('button', { name: 'Archive lot lot-b' })
    fireEvent.click(lotB)
    expect(screen.getByRole('dialog').contains(screen.getByRole('button', { name: 'Cancel archive' }))).toBe(true)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(lotB)
  })

  it('contains the complete application background and exposes the dialog description', () => {
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    const { unmount } = render(<HoldingMutations {...baseProps()} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    const dialog = screen.getByRole('dialog')
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect((root as HTMLElement).inert).toBe(true)
    expect(dialog.getAttribute('aria-labelledby')).toBe('archive-dialog-title')
    expect(dialog.getAttribute('aria-describedby')).toBe('archive-dialog-description')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel archive' }))
    expect(root.hasAttribute('aria-hidden')).toBe(false)
    expect((root as HTMLElement).inert).toBe(false)
    unmount()
    root.remove()
  })

  it('wraps focus from the programmatically focused heading and unexpected outside focus', () => {
    const props = { ...baseProps(), holdings: [{ ...holding, lot_id: 'lot-a' }, { ...holding, lot_id: 'lot-b' }] }
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-b' }))
    const dialog = screen.getByRole('dialog')
    const heading = screen.getByRole('heading', { name: /archive lot lot-b/i })
    const confirm = screen.getByRole('button', { name: 'Confirm archive lot lot-b' })
    const cancel = screen.getByRole('button', { name: 'Cancel archive' })
    expect(document.activeElement).toBe(heading)
    fireEvent.keyDown(heading, { key: 'Tab' })
    expect(document.activeElement).toBe(confirm)
    fireEvent.keyDown(heading, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(cancel)
    const outside = document.createElement('button')
    root.appendChild(outside)
    outside.focus()
    fireEvent.keyDown(outside, { key: 'Tab' })
    expect(dialog.contains(document.activeElement)).toBe(true)
    root.remove()
  })

  it('keeps archive failures visible and focused inside the active dialog', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async () => ({ ok: false, status: 415, headers: new Headers(), clone: () => ({ json: async () => ({}) }) } as unknown as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/unsupported request format/i))
    const dialog = screen.getByRole('dialog')
    const alert = screen.getByRole('alert')
    expect(dialog.contains(alert)).toBe(true)
    expect(document.activeElement).toBe(alert)
    expect((screen.getByRole('button', { name: 'Confirm archive lot lot-a' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('wraps forward and reverse Tab from a focused archive alert', async () => {
    const props = { ...baseProps(), holdings: [{ ...holding, lot_id: 'lot-a' }, { ...holding, lot_id: 'lot-b' }] }
    const fetchMock = vi.fn(async () => ({ ok: false, status: 415, headers: new Headers(), clone: () => ({ json: async () => ({}) }) } as unknown as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy())
    const alert = screen.getByRole('alert')
    const confirm = screen.getByRole('button', { name: 'Confirm archive lot lot-a' })
    const cancel = screen.getByRole('button', { name: 'Cancel archive' })
    expect(document.activeElement).toBe(alert)
    fireEvent.keyDown(alert, { key: 'Tab' })
    expect(document.activeElement).toBe(confirm)
    fireEvent.keyDown(alert, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(cancel)
  })

  it('tears down an archive modal before a changed portfolio and scope can reuse it', () => {
    const root = document.createElement('div')
    root.id = 'root'
    root.setAttribute('aria-hidden', 'false')
    root.inert = true
    document.body.appendChild(root)
    const props = baseProps()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    expect(screen.getByRole('dialog')).toBeTruthy()

    rerender(<HoldingMutations {...props} portfolioId="portfolio-next" overviewETag={'"portfolio-next-7"'} scopeKey="portfolio-next:SGD:overview" />)

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.getAttribute('aria-hidden')).toBe('false')
    expect(root.inert).toBe(true)
    expect(fetchMock).not.toHaveBeenCalled()
    root.remove()
  })

  it('aborts an in-flight archive and restores the background on scope replacement', async () => {
    const root = document.createElement('div')
    root.id = 'root'
    root.setAttribute('aria-hidden', 'previous')
    root.inert = false
    document.body.appendChild(root)
    const props = baseProps()
    let resolveFetch: (response: Response) => void = () => undefined
    const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) => new Promise<Response>((resolve) => { resolveFetch = resolve }))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    const request = fetchMock.mock.calls[0][1]

    rerender(<HoldingMutations {...props} scopeKey="portfolio-default:SGD:overview" />)

    expect((request?.signal as AbortSignal).aborted).toBe(true)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.getAttribute('aria-hidden')).toBe('previous')
    expect(root.inert).toBe(false)
    resolveFetch({ ok: true, status: 200, headers: new Headers(), json: async () => ({}) } as Response)
    await Promise.resolve()
    expect(props.onRefresh).not.toHaveBeenCalled()
    root.remove()
  })

  it('restores modal containment for active logout, unmount, and StrictMode effect cycles', () => {
    const root = document.createElement('div')
    root.id = 'root'
    root.setAttribute('aria-hidden', 'previous')
    root.inert = true
    document.body.appendChild(root)
    const props = baseProps()
    const { rerender, unmount } = render(<StrictMode><HoldingMutations {...props} /></StrictMode>, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    expect(screen.getByRole('dialog')).toBeTruthy()
    rerender(<StrictMode><HoldingMutations {...props} active={false} /></StrictMode>)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.getAttribute('aria-hidden')).toBe('previous')
    expect(root.inert).toBe(true)
    rerender(<StrictMode><HoldingMutations {...props} /></StrictMode>)
    fireEvent.click(screen.getByRole('button', { name: 'Archive lot lot-a' }))
    expect(screen.getByRole('dialog')).toBeTruthy()
    unmount()
    expect(root.getAttribute('aria-hidden')).toBe('previous')
    expect(root.inert).toBe(true)
    root.remove()
  })

  it('does not restore focus to a detached trigger after a successful single-lot archive', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />)
    const origin = screen.getByRole('button', { name: 'Archive lot lot-a' })
    fireEvent.click(origin)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive lot lot-a' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    rerender(<HoldingMutations {...props} holdings={[]} overviewETag={'"portfolio-default-8"'} />)
    await waitFor(() => expect(document.activeElement).not.toBe(origin))
    expect((document.activeElement as HTMLElement).isConnected).toBe(true)
    expect(document.activeElement).toBe(screen.getByRole('status'))
  })

  it('focuses the first invalid field and announces the form error', () => {
    const props = baseProps()
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    expect(screen.getByRole('alert').textContent).toMatch(/fix the highlighted/i)
    expect(document.activeElement).toBe(screen.getByLabelText('Symbol'))
    expect(screen.getByLabelText('Symbol').getAttribute('aria-describedby')).toContain('symbol-error')
  })
})
