import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import HoldingMutations, { validateHoldingMutationReceipt } from './HoldingMutations'

const holding = {
  lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', quantity: '2', currency: 'USD', unit_cost: '5', cost_basis: '10',
  freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false,
}
const receipt = {
  operation: 'create', result: 'applied', holding: {
    portfolio_id: 'portfolio-default', holding_id: 'lot-new', instrument_id: 'instrument-new', symbol: 'NEW',
    description: 'New instrument', quantity: '2', unit_cost: '5', cost_basis: '10', currency: 'USD', account_id: null,
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

  it('rejects a receipt with unexpected fields', () => {
    expect(() => validateHoldingMutationReceipt({ ...receipt, unexpected: true }, { operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key, body: JSON.stringify({}), lotId: undefined })).toThrow()
  })
})
