import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Reviews, { validateReview, validateReviewPage, type Review } from './Reviews'
import type { Holding } from './App'

const timestamp = '2026-01-02T12:00:00.000000000Z'
const holdings: Holding[] = [
  { lot_id: 'lot-a', symbol: 'SAME', name: 'First same-symbol lot', quantity: '1', currency: 'USD', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false },
  { lot_id: 'lot-b', symbol: 'SAME', name: 'Second same-symbol lot', quantity: '2', currency: 'USD', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false },
]
const review: Review = {
  review_id: 'review-1', portfolio_id: 'portfolio-default', snapshot_id: 'snap-1', calculation_version: 'calc-v1',
  scope: { type: 'holding', holding_ids: ['lot-b'], instrument_ids: ['instrument-b'] }, status: 'complete', recommendation: 'hold',
  rationale: 'Exact lot rationale', risks: ['One risk'], counterarguments: ['One counterargument'], conditions: ['One condition'],
  metrics: [{ name: 'value', value: '10.50', unit: 'USD', as_of: timestamp, source: 'snapshot', deterministic: true }],
  sourced_facts: [{ statement: 'A sourced fact', evidence_ids: ['evidence-1'] }], model_opinion: 'Model opinion',
  evidence: [{ evidence_id: 'evidence-1', url: 'https://example.test/source', title: 'Source title', published_at: null, accessed_at: timestamp, availability: 'available', freshness: 'current', excerpt: 'Untrusted excerpt' }],
  model: { provider: 'local-ai', model: 'review-model', version: '1', runtime: 'hermes-environment' }, generated_at: timestamp, submitted_at: timestamp,
  submitted_by: { principal_id: 'machine-1', principal_type: 'machine', permissions: ['reviews:write'] }, lifecycle: { historical_status: 'complete', current_snapshot_id: 'snap-1', outdated: false },
}
const emptyResponse = (body: unknown, status = 200) => Promise.resolve({ ok: status >= 200 && status < 300, status, json: async () => body } as Response)

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('Phase D review contract validation', () => {
  it('fails closed for contradictory lifecycle and unsafe evidence URLs', () => {
    expect(() => validateReview({ ...review, lifecycle: { ...review.lifecycle, current_snapshot_id: 'snap-2', outdated: false } })).toThrow(/lifecycle/i)
    expect(() => validateReview({ ...review, evidence: [{ ...review.evidence[0], url: 'http://example.test' }] })).toThrow(/URL/i)
    expect(() => validateReviewPage({ items: [{ ...review, portfolio_id: 'other-portfolio' }], has_more: false }, 'portfolio-default')).toThrow(/portfolio/i)
  })

  it('loads the exact holding filter and keeps same-symbol lot identity', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => emptyResponse({ items: [review], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} holdingId="lot-b" />)
    await waitFor(() => expect(screen.getByText(/SAME · lot lot-b/)).toBeTruthy())
    expect(screen.queryByText(/lot lot-a/)).toBeNull()
    expect(fetchMock.mock.calls[0]?.[0]).toContain('holding_id=lot-b')
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('sends feedback with CSRF, JSON and an opaque key, then validates the receipt', async () => {
    const feedback = { feedback_id: 'feedback-1', portfolio_id: 'portfolio-default', review_id: 'review-1', decision: 'agree', note: null, occurred_at: timestamp, submitted_by: { principal_id: 'local-owner', principal_type: 'browser_user', permissions: ['portfolio:read'] } }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input).includes('/feedback') ? emptyResponse(feedback, 201) : String(input).endsWith('/review-1') ? emptyResponse(review) : emptyResponse({ items: [review], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} csrfToken="csrf-token" onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => screen.getByRole('heading', { name: 'Record human feedback' }))
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => expect(screen.getByText(/Feedback recorded at/)).toBeTruthy())
    const call = fetchMock.mock.calls.find(([input]) => String(input).includes('/feedback'))
    const headers = new Headers(call?.[1]?.headers as HeadersInit)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token')
    expect(headers.get('Idempotency-Key')).toMatch(/^feedback-/)
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ decision: 'agree', note: null })
  })

  it('reuses the same key and draft when an uncertain feedback attempt is retried', async () => {
    const feedback = { feedback_id: 'feedback-2', portfolio_id: 'portfolio-default', review_id: 'review-1', decision: 'agree', note: null, occurred_at: timestamp, submitted_by: { principal_id: 'local-owner', principal_type: 'browser_user', permissions: ['portfolio:read'] } }
    let feedbackAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).includes('/feedback')) {
        feedbackAttempts += 1
        if (feedbackAttempts === 1) return Promise.reject(new Error('connection reset'))
        return emptyResponse(feedback, 201)
      }
      return String(input).endsWith('/review-1') ? emptyResponse(review) : emptyResponse({ items: [review], has_more: false })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} csrfToken="csrf-token" onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => screen.getByRole('heading', { name: 'Record human feedback' }))
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => screen.getByRole('button', { name: 'Retry feedback' }))
    const firstKey = new Headers(fetchMock.mock.calls[2]?.[1]?.headers as HeadersInit).get('Idempotency-Key')
    fireEvent.click(screen.getByRole('button', { name: 'Retry feedback' }))
    await waitFor(() => screen.getByText(/Feedback recorded at/))
    const secondKey = new Headers(fetchMock.mock.calls[3]?.[1]?.headers as HeadersInit).get('Idempotency-Key')
    expect(secondKey).toBe(firstKey)
  })

  it('renders outdated and no-current lifecycle states without claiming current identity', async () => {
    const outdated = { ...review, lifecycle: { historical_status: 'complete' as const, current_snapshot_id: 'snap-2', outdated: true } }
    const noCurrent = { ...review, review_id: 'review-2', lifecycle: { historical_status: 'complete' as const, current_snapshot_id: null, outdated: false } }
    vi.stubGlobal('fetch', vi.fn(() => emptyResponse({ items: [outdated, noCurrent], has_more: false })))
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Outdated review\. Current snapshot: snap-2/)).toBeTruthy())
    expect(screen.getByText('No current snapshot is available.')).toBeTruthy()
    expect(screen.queryByText('Current review snapshot')).toBeNull()
  })
})
