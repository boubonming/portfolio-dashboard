import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Reviews, { validateReview, validateReviewPage, type FeedbackAttemptStore, type Review } from './Reviews'
import type { Holding } from './App'
import { hasMatchingDeclaration, installCommittedStyles } from './cssTestUtils'

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
const cursorOne = 'cursor-1234567890'
const cursorTwo = 'cursor-0987654321'
function reviewFor(reviewId: string, submittedAt = timestamp, overrides: Partial<Review> = {}): Review {
  return { ...review, review_id: reviewId, generated_at: submittedAt, submitted_at: submittedAt, ...overrides }
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('Phase D review contract validation', () => {
  it('rejects portfolio-wide and wrong-lot items when an exact holding filter is active', () => {
    expect(() => validateReviewPage({ items: [{ ...review, scope: { type: 'portfolio', holding_ids: [], instrument_ids: [] } }], has_more: false }, 'portfolio-default', 'lot-b')).toThrow(/holding filter/i)
    expect(() => validateReviewPage({ items: [{ ...review, scope: { type: 'holding', holding_ids: ['lot-a'], instrument_ids: ['instrument-a'] } }], has_more: false }, 'portfolio-default', 'lot-b')).toThrow(/holding filter/i)
  })

  it('accepts a multi-target review when the requested exact lot is present', () => {
    const multiTarget = { ...review, scope: { type: 'holding' as const, holding_ids: ['lot-a', 'lot-b'], instrument_ids: ['instrument-a', 'instrument-b'] } }
    expect(validateReviewPage({ items: [multiTarget], has_more: false }, 'portfolio-default', 'lot-b').items[0]?.scope.holding_ids).toEqual(['lot-a', 'lot-b'])
  })

  it('allows only lifecycle advancement between list and detail', async () => {
    const advanced = { ...review, lifecycle: { historical_status: 'complete' as const, current_snapshot_id: 'snap-2', outdated: true } }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/review-1') ? emptyResponse(advanced) : emptyResponse({ items: [review], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => expect(screen.getByText(/Outdated review\. Current snapshot: snap-2/)).toBeTruthy())
  })

  it('binds the real list and detail controls to baseline, focus, and disabled legacy rules', async () => {
    const removeStyles = installCommittedStyles()
    let resolveFeedback: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/feedback')) return new Promise<Response>((resolve) => { resolveFeedback = resolve })
      return url.endsWith('/review-1') ? emptyResponse(review) : emptyResponse({ items: [review], has_more: false })
    })
    vi.stubGlobal('fetch', fetchMock)
    try {
      render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} csrfToken="csrf-token" onUnauthorized={vi.fn()} />)
      const viewButton = await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
      expect(hasMatchingDeclaration(viewButton, 'background', '#172322')).toBe(true)
      viewButton.focus()
      expect(hasMatchingDeclaration(viewButton, 'outline', '3px', 'focus-visible')).toBe(true)
      fireEvent.click(viewButton)
      await waitFor(() => expect(document.querySelector('.review-detail')).toBeTruthy())
      const note = screen.getByLabelText('Optional note')
      expect(hasMatchingDeclaration(note, 'background', '#172322')).toBe(true)
      fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
      await waitFor(() => expect(resolveFeedback).toBeTypeOf('function'))
      const submit = screen.getByRole('button', { name: 'Recording feedback…' })
      expect((submit as HTMLButtonElement).disabled).toBe(true)
      expect(hasMatchingDeclaration(submit, 'opacity', '.65', 'disabled')).toBe(true)
      resolveFeedback?.(await emptyResponse({}))
    } finally {
      removeStyles()
    }
  })

  it('fails closed for contradictory lifecycle and unsafe evidence URLs', () => {
    expect(() => validateReview({ ...review, lifecycle: { ...review.lifecycle, current_snapshot_id: 'snap-2', outdated: false } })).toThrow(/lifecycle/i)
    expect(() => validateReview({ ...review, lifecycle: { ...review.lifecycle, current_snapshot_id: null, outdated: true } })).toThrow(/lifecycle/i)
    expect(() => validateReview({ ...review, lifecycle: { ...review.lifecycle, current_snapshot_id: 'snap-2', outdated: false } })).toThrow(/lifecycle/i)
    expect(() => validateReview({ ...review, lifecycle: { ...review.lifecycle, current_snapshot_id: 'snap-1', outdated: true } })).toThrow(/lifecycle/i)
    expect(() => validateReview({ ...review, evidence: [{ ...review.evidence[0], url: 'http://example.test' }] })).toThrow(/URL/i)
    expect(() => validateReviewPage({ items: [{ ...review, portfolio_id: 'other-portfolio' }], has_more: false }, 'portfolio-default')).toThrow(/portfolio/i)
  })

  it('accepts portfolio and one-target scopes while rejecting scope duplicates and unknown keys', () => {
    expect(validateReviewPage({ items: [{ ...review, scope: { type: 'portfolio', holding_ids: [], instrument_ids: [] } }], has_more: false }, 'portfolio-default').items).toHaveLength(1)
    expect(validateReviewPage({ items: [{ ...review, scope: { type: 'holding', holding_ids: ['lot-a'], instrument_ids: ['instrument-a'] } }], has_more: false }, 'portfolio-default').items).toHaveLength(1)
    expect(() => validateReview({ ...review, scope: { type: 'holding', holding_ids: ['lot-a', 'lot-a'], instrument_ids: ['instrument-a', 'instrument-a'] } })).toThrow(/review response|scope/i)
    expect(() => validateReview({ ...review, scope: { type: 'holding', holding_ids: ['lot-a'], instrument_ids: ['instrument-a'], extra: true } })).toThrow(/review response|scope/i)
  })

  it('renders an unknown holding target as a warning without attaching it to a visible lot', async () => {
    const unknownTarget = reviewFor('review-unknown', timestamp, { scope: { type: 'holding', holding_ids: ['lot-unknown'], instrument_ids: ['instrument-unknown'] } })
    vi.stubGlobal('fetch', vi.fn(() => emptyResponse({ items: [unknownTarget], has_more: false })))
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Target identity unavailable/)).toBeTruthy())
    expect(screen.getAllByText(/not attached to a holding/i)).toHaveLength(2)
    expect(screen.queryByText(/SAME · lot lot-b/)).toBeNull()
  })

  it('validates complete and incomplete recommendation, evidence, and fact invariants', () => {
    const incomplete = { ...review, status: 'incomplete' as const, recommendation: 'no_recommendation' as const, lifecycle: { historical_status: 'incomplete' as const, current_snapshot_id: 'snap-1', outdated: false } }
    expect(validateReview(incomplete).status).toBe('incomplete')
    expect(() => validateReview({ ...incomplete, recommendation: 'hold' as const })).toThrow(/completeness/i)
    expect(() => validateReview({ ...review, evidence: [], sourced_facts: [] })).toThrow(/complete/i)
    expect(() => validateReview({ ...review, sourced_facts: [{ statement: 'broken', evidence_ids: ['missing-evidence'] }] })).toThrow(/evidence/i)
    expect(() => validateReview({ ...review, sourced_facts: [{ statement: 'duplicate reference', evidence_ids: ['evidence-1', 'evidence-1'] }] })).toThrow(/review response|evidence/i)
  })

  it('rejects duplicate sourced facts while preserving statement and ordered evidence identity', () => {
    const secondEvidence = { ...review.evidence[0], evidence_id: 'evidence-2', title: 'Second source' }
    const duplicate = { ...review, evidence: [review.evidence[0], secondEvidence], sourced_facts: [{ statement: 'same fact', evidence_ids: ['evidence-1'] }, { statement: 'same fact', evidence_ids: ['evidence-1'] }] }
    expect(() => validateReview(duplicate)).toThrow(/duplicate sourced fact/i)

    const distinctStatement = { ...duplicate, sourced_facts: [{ statement: 'same fact', evidence_ids: ['evidence-1'] }, { statement: 'different fact', evidence_ids: ['evidence-1'] }] }
    expect(validateReview(distinctStatement).sourced_facts).toHaveLength(2)

    const distinctOrderedEvidence = { ...duplicate, sourced_facts: [{ statement: 'same fact', evidence_ids: ['evidence-1', 'evidence-2'] }, { statement: 'same fact', evidence_ids: ['evidence-2', 'evidence-1'] }] }
    expect(validateReview(distinctOrderedEvidence).sourced_facts.map((fact) => fact.evidence_ids)).toEqual([['evidence-1', 'evidence-2'], ['evidence-2', 'evidence-1']])
  })

  it('fails closed for nested schema, timestamp, decimal, URL, provenance, attribution, and cross-review violations', () => {
    expect(() => validateReview({ ...review, metrics: [{ ...review.metrics[0], value: '1e3' }] })).toThrow(/metric/i)
    expect(() => validateReview({ ...review, generated_at: '2026-02-29T12:00:00.000000000Z' })).toThrow(/review response/i)
    expect(() => validateReview({ ...review, evidence: [{ ...review.evidence[0], url: 'https://example.test/source#fragment' }] })).toThrow(/URL/i)
    expect(() => validateReview({ ...review, model: { ...review.model, runtime: 'other-runtime' as 'hermes-environment' } })).toThrow(/provenance/i)
    expect(() => validateReview({ ...review, submitted_by: { principal_id: 'local-owner', principal_type: 'browser_user', permissions: ['portfolio:read'] } })).toThrow(/attribution/i)
    expect(() => validateReview({ ...review, model: { ...review.model, extra: 'secret' } as Review['model'] })).toThrow(/review response|provenance/i)
    expect(() => validateReviewPage({ items: [review, reviewFor('review-1')], has_more: false }, 'portfolio-default')).toThrow(/duplicate/i)
    expect(() => validateReviewPage({ items: [review], has_more: true, next_cursor: 'short' }, 'portfolio-default')).toThrow(/pagination/i)
  })

  it('keeps immutable list/detail fields exact while accepting lifecycle-only drift', () => {
    const lifecycleOnly = { ...review, lifecycle: { historical_status: 'complete' as const, current_snapshot_id: 'snap-2', outdated: true } }
    expect(() => validateReview(lifecycleOnly)).not.toThrow()
    expect(() => validateReview({ ...review, lifecycle: { historical_status: 'incomplete' as const, current_snapshot_id: 'snap-1', outdated: false } })).toThrow(/lifecycle/i)
  })

  it('requests the next cursor, deduplicates overlap, and keeps deterministic newest-first order', async () => {
    const first = reviewFor('review-first', '2026-01-02T11:00:00.000000000Z')
    const second = reviewFor('review-second', '2026-01-02T12:00:00.000000000Z')
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes(`cursor=${cursorOne}`)
      ? emptyResponse({ items: [first, second], has_more: false })
      : emptyResponse({ items: [first], has_more: true, next_cursor: cursorOne }))
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'Load more reviews' }))
    fireEvent.click(screen.getByRole('button', { name: 'Load more reviews' }))
    await waitFor(() => expect(screen.getByText(second.rationale)).toBeTruthy())
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain(`cursor=${cursorOne}`)
    expect(screen.getAllByRole('button', { name: 'View review details' }).map((button) => button.getAttribute('data-review-id'))).toEqual(['review-second', 'review-first'])
  })

  it('rejects conflicting duplicates, repeated cursors, and no-progress pages without losing the prior page', async () => {
    const first = reviewFor('review-first', '2026-01-02T11:00:00.000000000Z')
    let mode: 'conflict' | 'repeat' | 'no-progress' = 'conflict'
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (!String(input).includes(`cursor=${cursorOne}`)) return emptyResponse({ items: [first], has_more: true, next_cursor: cursorOne })
      if (mode === 'conflict') return emptyResponse({ items: [{ ...first, rationale: 'Conflicting duplicate' }], has_more: false })
      if (mode === 'repeat') return emptyResponse({ items: [reviewFor('review-second')], has_more: true, next_cursor: cursorOne })
      return emptyResponse({ items: [first], has_more: true, next_cursor: cursorTwo })
    })
    vi.stubGlobal('fetch', fetchMock)
    const renderProps = { portfolioId: 'portfolio-default', holdings, snapshot: { snapshot_id: 'snap-1', calculation_version: 'calc-v1' }, onUnauthorized: vi.fn() }
    render(<Reviews {...renderProps} />)
    await waitFor(() => screen.getByRole('button', { name: 'Load more reviews' }))
    fireEvent.click(screen.getByRole('button', { name: 'Load more reviews' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/Conflicting duplicate/))
    expect(screen.getByText(first.rationale)).toBeTruthy()

    mode = 'repeat'
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading reviews' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/repeated cursor/i))
    expect(screen.getByText(first.rationale)).toBeTruthy()

    mode = 'no-progress'
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading reviews' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/made no progress/i))
    expect(screen.getByText(first.rationale)).toBeTruthy()
  })

  it('preserves the first page on append errors and retries the same cursor', async () => {
    const first = reviewFor('review-first')
    const second = reviewFor('review-second', '2026-01-03T12:00:00.000000000Z')
    let appendAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (!String(input).includes(`cursor=${cursorOne}`)) return emptyResponse({ items: [first], has_more: true, next_cursor: cursorOne })
      appendAttempts += 1
      return appendAttempts === 1 ? Promise.reject(new Error('network down')) : emptyResponse({ items: [second], has_more: false })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'Load more reviews' }))
    fireEvent.click(screen.getByRole('button', { name: 'Load more reviews' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/network down/))
    expect(screen.getByText(first.rationale)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading reviews' }))
    await waitFor(() => expect(screen.getByText(second.rationale)).toBeTruthy())
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain(`cursor=${cursorOne}`)
  })

  it('suppresses a stale response from an abandoned request and focuses the status announcement after keyboard load-more', async () => {
    const stale = reviewFor('review-stale', '2026-01-02T11:00:00.000000000Z', { rationale: 'Stale response rationale' })
    const current = reviewFor('review-current', '2026-01-02T12:00:00.000000000Z', { rationale: 'Current response rationale' })
    const appended = reviewFor('review-appended', '2026-01-02T13:00:00.000000000Z', { rationale: 'Appended response rationale' })
    let resolveStale: ((response: Response) => void) | undefined
    const stalePromise = new Promise<Response>((resolve) => { resolveStale = resolve })
    const fetchMock = vi.fn((input: RequestInfo | URL) => fetchMock.mock.calls.length === 1
      ? stalePromise
      : fetchMock.mock.calls.length === 2
        ? emptyResponse({ items: [current], has_more: true, next_cursor: cursorOne })
        : emptyResponse({ items: [current, appended], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    rendered.rerender(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} holdingId="lot-b" onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Load more reviews' })).toBeTruthy())
    resolveStale?.(await emptyResponse({ items: [stale], has_more: false }))
    await Promise.resolve()
    expect(screen.queryByText(stale.rationale)).toBeNull()

    const loadMore = screen.getByRole('button', { name: 'Load more reviews' })
    loadMore.focus()
    fireEvent.keyDown(loadMore, { key: 'Enter' })
    fireEvent.click(loadMore)
    await waitFor(() => expect(screen.getByRole('status').textContent).toMatch(/Loaded 1 more review/))
    expect(document.activeElement).toBe(screen.getByRole('status'))
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

  it('reports secure-randomness failure accessibly without sending feedback', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/review-1') ? emptyResponse(review) : emptyResponse({ items: [review], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('crypto', {})
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} csrfToken="csrf-token" onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => screen.getByRole('heading', { name: 'Record human feedback' }))
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/secure randomness is unavailable/i))
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/feedback'))).toBe(false)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Retry feedback' }))
  })

  it('isolates retained feedback attempts by portfolio and review identity', async () => {
    const attempts: FeedbackAttemptStore = new Map()
    let feedbackCalls = 0
    const firstReview = { ...review, portfolio_id: 'portfolio-a' }
    const feedback = { feedback_id: 'feedback-isolated', portfolio_id: 'portfolio-b', review_id: 'review-1', decision: 'agree', note: null, occurred_at: timestamp, submitted_by: { principal_id: 'local-owner', principal_type: 'browser_user', permissions: ['portfolio:read'] } }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/feedback')) {
        feedbackCalls += 1
        return feedbackCalls === 1 ? Promise.reject(new Error('connection reset')) : emptyResponse(feedback, 201)
      }
      if (url.endsWith('/review-1')) return emptyResponse(url.includes('/portfolios/portfolio-b/') ? { ...review, portfolio_id: 'portfolio-b' } : firstReview)
      return emptyResponse({ items: [url.includes('/portfolios/portfolio-b/') ? { ...review, portfolio_id: 'portfolio-b' } : firstReview], has_more: false })
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<Reviews portfolioId="portfolio-a" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} csrfToken="csrf-token" feedbackAttempts={attempts} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => screen.getByRole('heading', { name: 'Record human feedback' }))
    fireEvent.change(screen.getByLabelText('Optional note'), { target: { value: 'portfolio A draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => screen.getByRole('button', { name: 'Retry feedback' }))
    const firstKey = new Headers(fetchMock.mock.calls.find(([input]) => String(input).includes('/feedback'))?.[1]?.headers as HeadersInit).get('Idempotency-Key')
    rendered.rerender(<Reviews portfolioId="portfolio-b" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} csrfToken="csrf-token" feedbackAttempts={attempts} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => expect((screen.getByLabelText('Optional note') as HTMLTextAreaElement).value).toBe(''))
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => screen.getByText(/Feedback recorded at/))
    const secondKey = new Headers(fetchMock.mock.calls.filter(([input]) => String(input).includes('/feedback'))[1]?.[1]?.headers as HeadersInit).get('Idempotency-Key')
    expect(secondKey).not.toBe(firstKey)
    expect(JSON.parse(String(fetchMock.mock.calls.filter(([input]) => String(input).includes('/feedback'))[1]?.[1]?.body))).toEqual({ decision: 'agree', note: null })
  })

  it('fails closed when the detail changes immutable list content', async () => {
    const changed = { ...review, rationale: 'Changed after list response' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/review-1') ? emptyResponse(changed) : emptyResponse({ items: [review], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/does not match the selected list review/i))
    expect(screen.queryByRole('heading', { name: 'Record human feedback' })).toBeNull()
  })

  it('rejects every immutable list/detail drift while permitting lifecycle-only drift', async () => {
    const drifts: Array<[string, Partial<Review>]> = [
      ['review identity', { review_id: 'review-other' }],
      ['portfolio identity', { portfolio_id: 'portfolio-other' }],
      ['snapshot identity', { snapshot_id: 'snap-2', lifecycle: { historical_status: 'complete', current_snapshot_id: 'snap-2', outdated: false } }],
      ['calculation version', { calculation_version: 'calc-v2' }],
      ['scope', { scope: { type: 'holding', holding_ids: ['lot-a'], instrument_ids: ['instrument-a'] } }],
      ['status', { status: 'incomplete', recommendation: 'no_recommendation', lifecycle: { historical_status: 'incomplete', current_snapshot_id: 'snap-1', outdated: false } }],
      ['recommendation', { recommendation: 'review' }],
      ['rationale', { rationale: 'changed rationale' }],
      ['arrays', { risks: ['changed risk'], counterarguments: ['changed counterargument'], conditions: ['changed condition'] }],
      ['metrics', { metrics: [{ ...review.metrics[0], value: '11.50' }] }],
      ['facts', { sourced_facts: [{ statement: 'changed fact', evidence_ids: ['evidence-1'] }] }],
      ['evidence', { evidence: [{ ...review.evidence[0], title: 'Changed source' }] }],
      ['model opinion', { model_opinion: 'changed opinion' }],
      ['model provenance', { model: { ...review.model, version: '2' } }],
      ['generation timestamp', { generated_at: '2026-01-02T12:00:01.000000000Z' }],
      ['submission timestamp', { submitted_at: '2026-01-02T12:00:01.000000000Z' }],
      ['attribution', { submitted_by: { principal_id: 'machine-2', principal_type: 'machine', permissions: ['reviews:write'] } }],
    ]
    for (const [name, drift] of drifts) {
      cleanup()
      const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/review-1') ? emptyResponse({ ...review, ...drift }) : emptyResponse({ items: [review], has_more: false }))
      vi.stubGlobal('fetch', fetchMock)
      render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
      await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
      fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
      await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy())
      expect(screen.queryByRole('heading', { name: 'Record human feedback' }), name).toBeNull()
    }
  })

  it('does not report success for a receipt with a different decision or note', async () => {
    const mismatched = { feedback_id: 'feedback-1', portfolio_id: 'portfolio-default', review_id: 'review-1', decision: 'disagree', note: 'other', occurred_at: timestamp, submitted_by: { principal_id: 'local-owner', principal_type: 'browser_user', permissions: ['portfolio:read'] } }
    const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes('/feedback') ? emptyResponse(mismatched, 201) : String(input).endsWith('/review-1') ? emptyResponse(review) : emptyResponse({ items: [review], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => screen.getByRole('heading', { name: 'Record human feedback' }))
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/does not match the submitted attempt/i))
    expect(screen.queryByText(/Feedback recorded at/)).toBeNull()
  })

  it('keeps an uncertain attempt key and canonical draft across detail close and reopen', async () => {
    let feedbackAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).includes('/feedback')) {
        feedbackAttempts += 1
        if (feedbackAttempts === 1) return Promise.reject(new Error('connection reset'))
        return emptyResponse({ feedback_id: 'feedback-2', portfolio_id: 'portfolio-default', review_id: 'review-1', decision: 'agree', note: 'keep this', occurred_at: timestamp, submitted_by: { principal_id: 'local-owner', principal_type: 'browser_user', permissions: ['portfolio:read'] } }, 201)
      }
      return String(input).endsWith('/review-1') ? emptyResponse(review) : emptyResponse({ items: [review], has_more: false })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => screen.getByRole('button', { name: 'View review details' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => screen.getByRole('heading', { name: 'Record human feedback' }))
    fireEvent.change(screen.getByLabelText('Optional note'), { target: { value: '  keep this  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => screen.getByRole('button', { name: 'Retry feedback' }))
    const firstFeedbackCall = fetchMock.mock.calls.find(([input]) => String(input).includes('/feedback'))
    const firstKey = new Headers(firstFeedbackCall?.[1]?.headers as HeadersInit).get('Idempotency-Key')
    fireEvent.click(screen.getByRole('button', { name: 'Close review detail' }))
    fireEvent.click(screen.getByRole('button', { name: 'View review details' }))
    await waitFor(() => expect((screen.getByLabelText('Optional note') as HTMLTextAreaElement).value).toBe('keep this'))
    fireEvent.click(screen.getByRole('button', { name: 'Record feedback' }))
    await waitFor(() => screen.getByText(/Feedback recorded at/), { timeout: 5000 })
    const feedbackCalls = fetchMock.mock.calls.filter(([input]) => String(input).includes('/feedback'))
    const secondKey = new Headers(feedbackCalls[1]?.[1]?.headers as HeadersInit).get('Idempotency-Key')
    expect(secondKey).toBe(firstKey)
    expect(JSON.parse(String(feedbackCalls[1]?.[1]?.body))).toEqual({ decision: 'agree', note: 'keep this' })
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

  it('claims current only when the loaded overview identity matches snapshot and calculation version', async () => {
    const currentReview = reviewFor('review-current', timestamp, { lifecycle: { historical_status: 'complete', current_snapshot_id: 'snap-1', outdated: false } })
    vi.stubGlobal('fetch', vi.fn(() => emptyResponse({ items: [currentReview], has_more: false })))
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-2', calculation_version: 'calc-v2' }} onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('Historical snapshot; current identity could not be confirmed.')).toBeTruthy())
    expect(screen.queryByText('Current review snapshot')).toBeNull()
  })

  it('renders every exact lot in a multi-target scope, including same-symbol lots', async () => {
    const multiTarget = reviewFor('review-multi', timestamp, { scope: { type: 'holding', holding_ids: ['lot-a', 'lot-b'], instrument_ids: ['instrument-a', 'instrument-b'] } })
    vi.stubGlobal('fetch', vi.fn(() => emptyResponse({ items: [multiTarget], has_more: false })))
    render(<Reviews portfolioId="portfolio-default" holdings={holdings} snapshot={{ snapshot_id: 'snap-1', calculation_version: 'calc-v1' }} holdingId="lot-b" onUnauthorized={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('SAME · lot lot-a, SAME · lot lot-b')).toBeTruthy())
    expect(screen.queryByText('Target identity unavailable; review is not attached to a holding.')).toBeNull()
  })
})
