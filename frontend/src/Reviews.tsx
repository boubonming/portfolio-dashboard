import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, RefObject } from 'react'
import { apiFetch, APIError, jsonResponse } from './api'
import type { Holding } from './App'

export type ReviewScope = { type: 'portfolio' | 'holding'; holding_ids: string[]; instrument_ids: string[] }
export type ReviewMetric = { name: string; value: string; unit: string; as_of: string; source: 'snapshot' | 'calculation' | 'submitted'; deterministic: true }
export type ReviewEvidence = { evidence_id: string; url: string; title: string; published_at?: string | null; accessed_at: string; availability: 'available' | 'unavailable' | 'blocked' | 'invalid'; freshness: 'current' | 'stale' | 'unknown'; excerpt: string }
export type SourcedFact = { statement: string; evidence_ids: string[] }
export type Review = {
  review_id: string; portfolio_id: string; snapshot_id: string; calculation_version: string; scope: ReviewScope
  status: 'complete' | 'incomplete'; recommendation: 'hold' | 'review' | 'trim_candidate' | 'exit_candidate' | 'no_recommendation'
  rationale: string; risks: string[]; counterarguments: string[]; conditions: string[]; metrics: ReviewMetric[]
  sourced_facts: SourcedFact[]; model_opinion: string; evidence: ReviewEvidence[]
  model: { provider: string; model: string; version: string; runtime: 'hermes-environment' }
  generated_at: string; submitted_at: string
  submitted_by: { principal_id: string; principal_type: 'browser_user' | 'machine'; permissions: Array<'portfolio:read' | 'reviews:write'> }
  lifecycle: { historical_status: 'complete' | 'incomplete'; current_snapshot_id: string | null; outdated: boolean }
}
export type ReviewPage = { items: Review[]; has_more: boolean; next_cursor?: string }
export type ReviewFeedback = { feedback_id: string; portfolio_id: string; review_id: string; decision: FeedbackDecision; note: string | null; occurred_at: string; submitted_by: Review['submitted_by'] }
export type FeedbackDecision = 'agree' | 'disagree' | 'defer' | 'request_another_review'

type ReviewState = 'loading' | 'ready' | 'error'
type OverviewSnapshot = { snapshot_id?: string; calculation_version?: string; state?: string }
type ReviewProps = {
  portfolioId: string
  holdings: Holding[]
  snapshot: OverviewSnapshot
  csrfToken?: string
  holdingId?: string
  onUnauthorized: () => void
  onBackToOverview?: () => void
  feedbackAttempts?: FeedbackAttemptStore
}

export type FeedbackAttempt = { key: string; body: string }
export type FeedbackAttemptStore = Map<string, FeedbackAttempt>

function feedbackAttemptIdentity(portfolioId: string, reviewId: string) {
  return `${portfolioId}\u0000${reviewId}`
}

const ID = /^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$/
const VERSION = ID
const DECIMAL = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/
const DATE = /^(\d{4})-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])T([01]\d|2[0-3]):([0-5]\d):([0-5]\d)\.\d{9}Z$/
const CURSOR = /^[A-Za-z0-9_-]{16,512}$/
const evidenceUrlPattern = /^https:\/\/[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*(?:[/?][A-Za-z0-9._~:/?$&'()*+,;=%!-]*)?$/
const feedbackDecisions: FeedbackDecision[] = ['agree', 'disagree', 'defer', 'request_another_review']

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid review response')
  return value as Record<string, unknown>
}
function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []) {
  const allowed = new Set([...required, ...optional])
  if (required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) || Object.keys(value).some((key) => !allowed.has(key))) throw new Error('Invalid review response')
}
function bounded(value: unknown, min: number, max: number): value is string { return typeof value === 'string' && value.length >= min && value.length <= max }
function identifier(value: unknown): value is string { return typeof value === 'string' && ID.test(value) }
function canonicalDate(value: unknown): value is string {
  if (typeof value !== 'string') return false
  const match = DATE.exec(value)
  if (!match || !Number.isFinite(Date.parse(value))) return false
  const day = Number(match[3])
  return day <= new Date(Date.UTC(Number(match[1]), Number(match[2]), 0)).getUTCDate()
}
function uniqueStrings(values: unknown, validator: (value: unknown) => boolean, min: number, max: number) {
  if (!Array.isArray(values) || values.length < min || values.length > max || values.some((value) => !validator(value))) throw new Error('Invalid review response')
  if (new Set(values).size !== values.length) throw new Error('Invalid review response')
  return values as string[]
}
function textArray(value: unknown, max: number) {
  if (!Array.isArray(value) || value.length > max || value.some((candidate) => !bounded(candidate, 1, 2000))) throw new Error('Invalid review response')
  return value as string[]
}

function validateEvidence(value: unknown): ReviewEvidence {
  const candidate = object(value)
  exactKeys(candidate, ['evidence_id', 'url', 'title', 'accessed_at', 'availability', 'freshness', 'excerpt'], ['published_at'])
  if (!identifier(candidate.evidence_id) || !bounded(candidate.title, 1, 512) || !bounded(candidate.excerpt, 1, 4000) || !canonicalDate(candidate.accessed_at)) throw new Error('Invalid review evidence')
  if (typeof candidate.url !== 'string' || candidate.url.length > 2048 || !evidenceUrlPattern.test(candidate.url)) throw new Error('Invalid review evidence URL')
  try {
    const parsed = new URL(candidate.url)
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.port || parsed.hash || parsed.hostname !== parsed.hostname.toLowerCase()) throw new Error('bad URL')
    if (parsed.hostname.length > 253 || parsed.hostname.split('.').some((label) => label.length < 1 || label.length > 63 || label.startsWith('-') || label.endsWith('-'))) throw new Error('bad host')
  } catch { throw new Error('Invalid review evidence URL') }
  if (candidate.published_at !== undefined && candidate.published_at !== null && !canonicalDate(candidate.published_at)) throw new Error('Invalid review evidence')
  if (!['available', 'unavailable', 'blocked', 'invalid'].includes(String(candidate.availability)) || !['current', 'stale', 'unknown'].includes(String(candidate.freshness))) throw new Error('Invalid review evidence')
  return candidate as unknown as ReviewEvidence
}
function validateScope(value: unknown): ReviewScope {
  const candidate = object(value)
  exactKeys(candidate, ['type', 'holding_ids', 'instrument_ids'])
  if (candidate.type !== 'portfolio' && candidate.type !== 'holding') throw new Error('Invalid review scope')
  const holdingIds = uniqueStrings(candidate.holding_ids, identifier, candidate.type === 'holding' ? 1 : 0, candidate.type === 'holding' ? 100 : 0)
  const instrumentIds = uniqueStrings(candidate.instrument_ids, identifier, candidate.type === 'holding' ? 1 : 0, candidate.type === 'holding' ? 100 : 0)
  if (candidate.type === 'holding' && holdingIds.length !== instrumentIds.length) throw new Error('Invalid review scope')
  return { type: candidate.type, holding_ids: holdingIds, instrument_ids: instrumentIds }
}
function validateMetric(value: unknown): ReviewMetric {
  const candidate = object(value)
  exactKeys(candidate, ['name', 'value', 'unit', 'as_of', 'source', 'deterministic'])
  if (!bounded(candidate.name, 1, 128) || typeof candidate.value !== 'string' || candidate.value.length > 128 || !DECIMAL.test(candidate.value) || !bounded(candidate.unit, 1, 64) || !canonicalDate(candidate.as_of) || !['snapshot', 'calculation', 'submitted'].includes(String(candidate.source)) || candidate.deterministic !== true) throw new Error('Invalid review metric')
  return candidate as unknown as ReviewMetric
}
function validateAttribution(value: unknown): Review['submitted_by'] {
  const candidate = object(value)
  exactKeys(candidate, ['principal_id', 'principal_type', 'permissions'])
  if (!identifier(candidate.principal_id) || !['browser_user', 'machine'].includes(String(candidate.principal_type))) throw new Error('Invalid review attribution')
  const permissions = uniqueStrings(candidate.permissions, (item) => item === 'portfolio:read' || item === 'reviews:write', 1, 2) as Review['submitted_by']['permissions']
  if ((candidate.principal_type === 'machine' && !permissions.includes('reviews:write')) || (candidate.principal_type === 'browser_user' && (candidate.principal_id !== 'local-owner' || permissions.length !== 1 || permissions[0] !== 'portfolio:read'))) throw new Error('Invalid review attribution')
  return { principal_id: candidate.principal_id, principal_type: candidate.principal_type as Review['submitted_by']['principal_type'], permissions }
}
function validateLifecycle(value: unknown, snapshotId: string, status: Review['status']): Review['lifecycle'] {
  const candidate = object(value)
  exactKeys(candidate, ['historical_status', 'current_snapshot_id', 'outdated'])
  if (candidate.historical_status !== status || (candidate.current_snapshot_id !== null && !identifier(candidate.current_snapshot_id)) || typeof candidate.outdated !== 'boolean') throw new Error('Contradictory review lifecycle')
  const current = candidate.current_snapshot_id as string | null
  if (current === null ? candidate.outdated !== false : current === snapshotId ? candidate.outdated !== false : candidate.outdated !== true) throw new Error('Contradictory review lifecycle')
  return { historical_status: status, current_snapshot_id: current, outdated: candidate.outdated as boolean }
}
export function validateReview(value: unknown): Review {
  const candidate = object(value)
  exactKeys(candidate, ['review_id', 'portfolio_id', 'snapshot_id', 'calculation_version', 'scope', 'status', 'recommendation', 'rationale', 'risks', 'counterarguments', 'conditions', 'metrics', 'sourced_facts', 'model_opinion', 'evidence', 'model', 'generated_at', 'submitted_at', 'submitted_by', 'lifecycle'])
  if (!identifier(candidate.review_id) || !identifier(candidate.portfolio_id) || !identifier(candidate.snapshot_id) || !bounded(candidate.calculation_version, 1, 128) || !VERSION.test(candidate.calculation_version) || !['complete', 'incomplete'].includes(String(candidate.status)) || !bounded(candidate.rationale, 1, 8000) || !bounded(candidate.model_opinion, 1, 8000) || !canonicalDate(candidate.generated_at) || !canonicalDate(candidate.submitted_at)) throw new Error('Invalid review response')
  const status = candidate.status as Review['status']
  const recommendationValues = status === 'complete' ? ['hold', 'review', 'trim_candidate', 'exit_candidate'] : ['no_recommendation']
  if (!recommendationValues.includes(String(candidate.recommendation))) throw new Error('Invalid review completeness')
  const scope = validateScope(candidate.scope)
  const risks = textArray(candidate.risks, 32)
  const counterarguments = textArray(candidate.counterarguments, 32)
  const conditions = textArray(candidate.conditions, 32)
  if (!Array.isArray(candidate.metrics) || candidate.metrics.length > 100) throw new Error('Invalid review metrics')
  const metrics = candidate.metrics.map(validateMetric)
  if (!Array.isArray(candidate.evidence) || candidate.evidence.length > 64) throw new Error('Invalid review evidence')
  const evidence = candidate.evidence.map(validateEvidence)
  if (new Set(evidence.map((item) => item.evidence_id)).size !== evidence.length) throw new Error('Duplicate evidence ID')
  if (!Array.isArray(candidate.sourced_facts) || candidate.sourced_facts.length > 32) throw new Error('Invalid sourced facts')
  const evidenceIds = new Set(evidence.map((item) => item.evidence_id))
  const sourcedFacts = candidate.sourced_facts.map((raw) => {
    const fact = object(raw); exactKeys(fact, ['statement', 'evidence_ids'])
    if (!bounded(fact.statement, 1, 2000)) throw new Error('Invalid sourced fact')
    const ids = uniqueStrings(fact.evidence_ids, identifier, 1, 16)
    if (ids.some((id) => !evidenceIds.has(id))) throw new Error('Broken evidence reference')
    return { statement: fact.statement, evidence_ids: ids }
  })
  if (new Set(sourcedFacts.map((fact) => JSON.stringify([fact.statement, fact.evidence_ids]))).size !== sourcedFacts.length) throw new Error('Duplicate sourced fact')
  const model = object(candidate.model); exactKeys(model, ['provider', 'model', 'version', 'runtime'])
  if (!bounded(model.provider, 1, 128) || !/^[A-Za-z0-9][A-Za-z0-9._~-]*$/.test(model.provider) || !bounded(model.model, 1, 256) || !/^[A-Za-z0-9][A-Za-z0-9._~:/-]*$/.test(model.model) || !bounded(model.version, 1, 128) || model.runtime !== 'hermes-environment') throw new Error('Invalid model provenance')
  if (status === 'complete' && (evidence.length < 1 || evidence.some((item) => item.availability !== 'available' || item.freshness !== 'current') || sourcedFacts.length < 1)) throw new Error('Invalid complete review')
  const submittedBy = validateAttribution(candidate.submitted_by)
  if (submittedBy.principal_type !== 'machine' || !submittedBy.permissions.includes('reviews:write')) throw new Error('Invalid review attribution')
  const lifecycle = validateLifecycle(candidate.lifecycle, candidate.snapshot_id, status)
  return { review_id: candidate.review_id, portfolio_id: candidate.portfolio_id, snapshot_id: candidate.snapshot_id, calculation_version: candidate.calculation_version, scope, status, recommendation: candidate.recommendation as Review['recommendation'], rationale: candidate.rationale, risks, counterarguments, conditions, metrics, sourced_facts: sourcedFacts, model_opinion: candidate.model_opinion, evidence, model: model as Review['model'], generated_at: candidate.generated_at, submitted_at: candidate.submitted_at, submitted_by: submittedBy, lifecycle }
}
function canonicalValue(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalValue).join(',')}]`
  if (value && typeof value === 'object') {
    return `{${Object.entries(value as Record<string, unknown>).sort(([left], [right]) => left.localeCompare(right)).map(([key, item]) => `${JSON.stringify(key)}:${canonicalValue(item)}`).join(',')}}`
  }
  return JSON.stringify(value)
}

function sameReview(left: Review, right: Review) {
  const immutable = (review: Review) => {
    const { lifecycle: _lifecycle, ...content } = review
    return content
  }
  return canonicalValue(immutable(left)) === canonicalValue(immutable(right))
}

function matchesHoldingFilter(review: Review, holdingId: string) {
  return review.scope.type === 'holding' && review.scope.holding_ids.length === review.scope.instrument_ids.length && review.scope.holding_ids.includes(holdingId)
}

export function validateReviewPage(value: unknown, portfolioId: string, holdingId?: string): ReviewPage {
  const candidate = object(value)
  exactKeys(candidate, ['items', 'has_more'], ['next_cursor'])
  if (!Array.isArray(candidate.items) || candidate.items.length > 50 || typeof candidate.has_more !== 'boolean') throw new Error('Invalid review pagination response')
  if (candidate.has_more !== Object.prototype.hasOwnProperty.call(candidate, 'next_cursor') || (candidate.has_more && (typeof candidate.next_cursor !== 'string' || !CURSOR.test(candidate.next_cursor as string)))) throw new Error('Invalid review pagination response')
  if (!candidate.has_more && Object.prototype.hasOwnProperty.call(candidate, 'next_cursor')) throw new Error('Invalid review pagination response')
  const items = candidate.items.map(validateReview)
  if (items.some((review) => review.portfolio_id !== portfolioId) || new Set(items.map((review) => review.review_id)).size !== items.length) throw new Error('Cross-portfolio or duplicate review response')
  if (holdingId && items.some((review) => !matchesHoldingFilter(review, holdingId))) throw new Error('Review target does not match the requested holding filter')
  return candidate.has_more ? { items, has_more: true, next_cursor: candidate.next_cursor as string } : { items, has_more: false }
}
export function validateFeedback(value: unknown, portfolioId: string, reviewId: string, expected?: { decision: FeedbackDecision; note: string | null }): ReviewFeedback {
  const candidate = object(value)
  exactKeys(candidate, ['feedback_id', 'portfolio_id', 'review_id', 'decision', 'note', 'occurred_at', 'submitted_by'])
  if (!identifier(candidate.feedback_id) || candidate.portfolio_id !== portfolioId || candidate.review_id !== reviewId || !feedbackDecisions.includes(candidate.decision as FeedbackDecision) || (candidate.note !== null && !bounded(candidate.note, 1, 2000)) || !canonicalDate(candidate.occurred_at)) throw new Error('Invalid feedback receipt')
  if (expected && (candidate.decision !== expected.decision || candidate.note !== expected.note)) throw new Error('Feedback receipt does not match the submitted attempt')
  const submittedBy = validateAttribution(candidate.submitted_by)
  if (submittedBy.principal_type !== 'browser_user') throw new Error('Invalid feedback receipt')
  return { feedback_id: candidate.feedback_id, portfolio_id: candidate.portfolio_id, review_id: candidate.review_id, decision: candidate.decision as FeedbackDecision, note: candidate.note as string | null, occurred_at: candidate.occurred_at, submitted_by: submittedBy }
}

function errorMessage(reason: unknown, action: string) {
  if (reason instanceof APIError) {
    if (reason.status === 400) return `${action} was rejected because the request was invalid.`
    if (reason.status === 403) return `You are not allowed to ${action.toLowerCase()}. Check your session and try again.`
    if (reason.status === 404) return `The ${action.toLowerCase()} is no longer available.`
    if (reason.status === 409) return 'This feedback key conflicts with a previous event. Change the feedback and submit again.'
    if (reason.status === 413) return 'The feedback note is too large.'
    if (reason.status === 415) return 'The server rejected the feedback format.'
    if (reason.status >= 500) return `The server could not ${action.toLowerCase()}. Retry without changing the draft.`
  }
  return `Unable to ${action.toLowerCase()}. Check your connection and retry without changing the draft.`
}
function displayDate(value: string) { return value.replace('T', ' ').replace(/\.\d{9}Z$/, 'Z') }
function newIdempotencyKey() {
  const cryptoObject = globalThis.crypto
  if (cryptoObject?.randomUUID) return `feedback-${cryptoObject.randomUUID()}`
  if (cryptoObject?.getRandomValues) {
    const bytes = cryptoObject.getRandomValues(new Uint8Array(16))
    return `feedback-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
  }
  throw new Error('Secure randomness is unavailable; feedback cannot be submitted safely.')
}

function ReviewDetail({ review, portfolioId, snapshot, csrfToken, onUnauthorized, detailRef, feedbackAttempts }: { review: Review; portfolioId: string; snapshot: OverviewSnapshot; csrfToken?: string; onUnauthorized: () => void; detailRef: RefObject<HTMLElement | null>; feedbackAttempts: FeedbackAttemptStore }) {
  const attemptIdentity = feedbackAttemptIdentity(portfolioId, review.review_id)
  const storedAttempt = feedbackAttempts.get(attemptIdentity)
  const storedDraft = (() => {
    if (!storedAttempt) return null
    try {
      const parsed = JSON.parse(storedAttempt.body) as { decision?: FeedbackDecision; note?: string | null }
      return feedbackDecisions.includes(parsed.decision as FeedbackDecision) && (parsed.note === null || typeof parsed.note === 'string') ? parsed : null
    } catch { return null }
  })()
  const [decision, setDecision] = useState<FeedbackDecision>(storedDraft?.decision || 'agree')
  const [note, setNote] = useState(storedDraft?.note || '')
  const [feedbackState, setFeedbackState] = useState<'idle' | 'submitting' | 'error' | 'success'>('idle')
  const [feedbackError, setFeedbackError] = useState('')
  const [receipt, setReceipt] = useState<ReviewFeedback | null>(null)
  const attemptRef = useRef<FeedbackAttempt | null>(storedAttempt || null)
  const feedbackGeneration = useRef(0)
  const confirmationRef = useRef<HTMLParagraphElement>(null)
  const retryRef = useRef<HTMLButtonElement>(null)
  const feedbackControllerRef = useRef<AbortController | null>(null)
  const discardAttempt = () => {
    attemptRef.current = null
    feedbackAttempts.delete(attemptIdentity)
    setReceipt(null)
  }
  const noteChanged = (value: string) => { setNote(value); discardAttempt(); if (feedbackState !== 'submitting') setFeedbackState('idle'); setFeedbackError('') }
  const changeDecision = (value: FeedbackDecision) => { setDecision(value); discardAttempt(); if (feedbackState !== 'submitting') setFeedbackState('idle'); setFeedbackError('') }
  useLayoutEffect(() => { feedbackGeneration.current += 1; return () => { feedbackGeneration.current += 1; feedbackControllerRef.current?.abort(); feedbackControllerRef.current = null } }, [attemptIdentity])
  useEffect(() => { detailRef.current?.focus() }, [detailRef, review.review_id])
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (feedbackState === 'submitting') return
    const bodyObject: { decision: FeedbackDecision; note: string | null } = { decision, note: note.trim() ? note.trim() : null }
    const body = JSON.stringify(bodyObject)
    setFeedbackState('submitting'); setFeedbackError('')
    let controller: AbortController | null = null
    let generation = 0
    try {
      const attempt = attemptRef.current?.body === body ? attemptRef.current : { key: newIdempotencyKey(), body }
      attemptRef.current = attempt
      feedbackAttempts.set(attemptIdentity, attempt)
      generation = ++feedbackGeneration.current
      feedbackControllerRef.current?.abort()
      controller = new AbortController()
      feedbackControllerRef.current = controller
      const response = await apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioId)}/reviews/${encodeURIComponent(review.review_id)}/feedback`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': attempt.key }, body: attempt.body, csrfToken, signal: controller.signal })
      const parsed = await jsonResponse<unknown>(response)
      const validated = validateFeedback(parsed, portfolioId, review.review_id, bodyObject)
      if (generation !== feedbackGeneration.current) return
      setReceipt(validated); setFeedbackState('success'); attemptRef.current = null
      if (feedbackAttempts.get(attemptIdentity)?.key === attempt.key) feedbackAttempts.delete(attemptIdentity)
      window.setTimeout(() => confirmationRef.current?.focus(), 0)
    } catch (reason) {
      if (controller && generation !== feedbackGeneration.current) return
      if (reason instanceof APIError && reason.status === 401) { onUnauthorized(); return }
      setFeedbackState('error'); setFeedbackError(reason instanceof Error && !(reason instanceof APIError) ? reason.message : errorMessage(reason, 'record feedback'))
      window.setTimeout(() => retryRef.current?.focus(), 0)
    } finally {
      if (controller && feedbackControllerRef.current === controller) feedbackControllerRef.current = null
    }
  }
  return <article className="review-detail" ref={detailRef as RefObject<HTMLElement>} tabIndex={-1} aria-labelledby={`review-detail-${review.review_id}`}>
    <div className="section-heading"><div><span className="eyebrow">Immutable review detail</span><h3 id={`review-detail-${review.review_id}`}>{review.recommendation.replaceAll('_', ' ')}</h3></div><span className={`state-pill state-${review.status}`}>{review.status}</span></div>
    <dl className="review-meta">
      <div><dt>Review ID</dt><dd>{review.review_id}</dd></div><div><dt>Snapshot ID</dt><dd>{review.snapshot_id}</dd></div><div><dt>Calculation version</dt><dd>{review.calculation_version}</dd></div>
      <div><dt>Submitted</dt><dd><time dateTime={review.submitted_at}>{displayDate(review.submitted_at)}</time></dd></div><div><dt>Generated</dt><dd><time dateTime={review.generated_at}>{displayDate(review.generated_at)}</time></dd></div>
      <div><dt>Model</dt><dd>{review.model.provider} / {review.model.model} / {review.model.version}</dd></div><div><dt>Submitted by</dt><dd>{review.submitted_by.principal_type} ({review.submitted_by.permissions.join(', ')})</dd></div>
    </dl>
    <SnapshotBadge review={review} snapshot={snapshot} />
    <ReviewSection title="Scope"><p>{review.scope.type === 'portfolio' ? 'Portfolio-wide; not attached to a holding.' : `Exact holding lot IDs: ${review.scope.holding_ids.join(', ')}`}</p></ReviewSection>
    <ReviewSection title="Rationale"><p>{review.rationale}</p></ReviewSection>
    <ReviewSection title="Model opinion"><p>{review.model_opinion}</p></ReviewSection>
    <ReviewList title="Risks" items={review.risks} /><ReviewList title="Counterarguments" items={review.counterarguments} /><ReviewList title="Conditions" items={review.conditions} />
    {review.metrics.length > 0 && <ReviewSection title="Deterministic metrics"><div className="review-metrics">{review.metrics.map((metric) => <div key={`${metric.name}-${metric.as_of}`}><dt>{metric.name}</dt><dd>{metric.value} {metric.unit}<span className="subline">{metric.source} · {displayDate(metric.as_of)}</span></dd></div>)}</div></ReviewSection>}
    {review.sourced_facts.length > 0 && <ReviewSection title="Sourced facts"><ul>{review.sourced_facts.map((fact, index) => <li key={`${fact.statement}-${index}`}>{fact.statement} <span className="subline">Evidence: {fact.evidence_ids.join(', ')}</span></li>)}</ul></ReviewSection>}
    <ReviewSection title="Evidence"><ul className="evidence-list">{review.evidence.map((evidence) => <li key={evidence.evidence_id}><a href={evidence.url} target="_blank" rel="noopener noreferrer">{evidence.title}</a><span className="subline">{evidence.availability} · {evidence.freshness} · accessed {displayDate(evidence.accessed_at)}</span><p>{evidence.excerpt}</p></li>)}</ul></ReviewSection>
    <section className="feedback-panel" aria-labelledby={`feedback-title-${review.review_id}`}><h4 id={`feedback-title-${review.review_id}`}>Record human feedback</h4><p className="muted">Feedback is an append-only event and does not edit this review. “Request another review” records feedback only; it does not schedule a review.</p><form onSubmit={submit}>
      <fieldset disabled={feedbackState === 'submitting'}><legend>Decision</legend><div className="feedback-options">{feedbackDecisions.map((value) => <label key={value}><input type="radio" name={`feedback-${review.review_id}`} value={value} checked={decision === value} onChange={() => changeDecision(value)} /> {value.replaceAll('_', ' ')}</label>)}</div>
      <label htmlFor={`feedback-note-${review.review_id}`}>Optional note</label><textarea id={`feedback-note-${review.review_id}`} value={note} maxLength={2000} onChange={(event) => noteChanged(event.target.value)} /></fieldset>
      {feedbackError && <p className="notice error" role="alert">{feedbackError} <button ref={retryRef} type="submit" formNoValidate>Retry feedback</button></p>}
      <button type="submit" disabled={feedbackState === 'submitting'} aria-busy={feedbackState === 'submitting'}>{feedbackState === 'submitting' ? 'Recording feedback…' : 'Record feedback'}</button>
      {receipt && <p ref={confirmationRef} className="notice success" role="status" tabIndex={-1}>Feedback recorded at <time dateTime={receipt.occurred_at}>{displayDate(receipt.occurred_at)}</time>. Receipt {receipt.feedback_id}.</p>}
    </form></section>
  </article>
}
function ReviewSection({ title, children }: { title: string; children: React.ReactNode }) { return <section className="review-section"><h4>{title}</h4>{children}</section> }
function ReviewList({ title, items }: { title: string; items: string[] }) { return <ReviewSection title={title}>{items.length ? <ul>{items.map((item, index) => <li key={`${item}-${index}`}>{item}</li>)}</ul> : <p className="muted">None recorded.</p>}</ReviewSection> }
function SnapshotBadge({ review, snapshot }: { review: Review; snapshot: OverviewSnapshot }) {
  if (review.lifecycle.outdated) return <p className="snapshot-badge outdated">Outdated review. Current snapshot: {review.lifecycle.current_snapshot_id}</p>
  if (!review.lifecycle.current_snapshot_id) return <p className="snapshot-badge missing">No current snapshot is available.</p>
  if (review.lifecycle.current_snapshot_id === review.snapshot_id && snapshot.snapshot_id === review.snapshot_id && snapshot.calculation_version === review.calculation_version) return <p className="snapshot-badge current">Current review snapshot</p>
  return <p className="snapshot-badge">Historical snapshot; current identity could not be confirmed.</p>
}

export default function Reviews({ portfolioId, holdings, snapshot, csrfToken, holdingId, onUnauthorized, onBackToOverview, feedbackAttempts }: ReviewProps) {
  const [page, setPage] = useState<ReviewPage | null>(null)
  const [state, setState] = useState<ReviewState>('loading')
  const [error, setError] = useState('')
  const [loadingMore, setLoadingMore] = useState(false)
  const [appendError, setAppendError] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detail, setDetail] = useState<Review | null>(null)
  const [detailState, setDetailState] = useState<ReviewState>('ready')
  const [detailError, setDetailError] = useState('')
  const [listAnnouncement, setListAnnouncement] = useState('')
  const listGeneration = useRef(0)
  const detailGeneration = useRef(0)
  const listController = useRef<AbortController | null>(null)
  const detailController = useRef<AbortController | null>(null)
  const loadingMoreRef = useRef(false)
  const detailRef = useRef<HTMLElement>(null)
  const listStatusRef = useRef<HTMLParagraphElement>(null)
  const selectedButtonRef = useRef<string | null>(null)
  const itemsRef = useRef<Review[]>([])
  const paginationCursorsRef = useRef(new Set<string>())
  const localFeedbackAttempts = useRef<FeedbackAttemptStore>(new Map())
  const feedbackAttemptStore = feedbackAttempts || localFeedbackAttempts.current
  const holdingMap = useMemo(() => new Map(holdings.map((holding) => [holding.lot_id, holding])), [holdings])
  const targetState = useCallback((review: Review) => review.scope.type === 'portfolio' ? { valid: true, label: 'Portfolio-wide' } : review.scope.holding_ids.every((id) => holdingMap.has(id)) ? { valid: true, label: review.scope.holding_ids.map((id) => `${holdingMap.get(id)?.symbol || 'lot'} · lot ${id}`).join(', ') } : { valid: false, label: 'Target identity unavailable; review is not attached to a holding.' }, [holdingMap])
  const load = useCallback((cursor: string | undefined, append: boolean) => {
    if (!portfolioId || (append && loadingMoreRef.current)) return
    if (append && (!cursor || paginationCursorsRef.current.has(cursor))) {
      setAppendError('Unable to load more reviews: the server returned a repeated pagination cursor. Retry from the first page.')
      return
    }
    const generation = ++listGeneration.current
    listController.current?.abort()
    const controller = new AbortController(); listController.current = controller
    if (append) { loadingMoreRef.current = true; setLoadingMore(true); setAppendError('') } else { loadingMoreRef.current = false; setState('loading'); setPage(null); itemsRef.current = []; setError(''); setAppendError(''); setListAnnouncement(''); setSelectedId(null); setDetail(null) }
    if (!append) paginationCursorsRef.current.clear()
    if (cursor) paginationCursorsRef.current.add(cursor)
    const params = new URLSearchParams({ limit: '20' }); if (cursor) params.set('cursor', cursor); if (holdingId) params.set('holding_id', holdingId)
    apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioId)}/reviews?${params.toString()}`, { signal: controller.signal })
      .then((response) => jsonResponse<unknown>(response)).then((raw) => validateReviewPage(raw, portfolioId, holdingId))
      .then((nextPage) => {
        if (controller.signal.aborted || generation !== listGeneration.current) return
        const previous = itemsRef.current; const previousIds = new Set(previous.map((review) => review.review_id)); const merged = new Map(previous.map((review) => [review.review_id, review]))
        for (const review of nextPage.items) {
          const existing = merged.get(review.review_id)
          if (existing && !sameReview(existing, review)) throw new Error('Conflicting duplicate review received across pages')
          if (!existing) merged.set(review.review_id, review)
        }
        const items = [...merged.values()].sort((left, right) => right.submitted_at.localeCompare(left.submitted_at) || right.review_id.localeCompare(left.review_id))
        const appended = items.filter((review) => !previousIds.has(review.review_id)).length
        if (append && nextPage.has_more && appended === 0) throw new Error('Review pagination made no progress')
        if (nextPage.has_more && (!nextPage.next_cursor || paginationCursorsRef.current.has(nextPage.next_cursor))) throw new Error('Review pagination returned a repeated cursor')
        itemsRef.current = items; setPage({ ...nextPage, items }); setState('ready'); loadingMoreRef.current = false; setLoadingMore(false); setAppendError('')
        if (append) { setListAnnouncement(`Loaded ${appended} more review${appended === 1 ? '' : 's'}.`); listStatusRef.current?.focus() }
      }).catch((reason: unknown) => {
        if (controller.signal.aborted || generation !== listGeneration.current) return
        if (reason instanceof APIError && reason.status === 401) { onUnauthorized(); return }
        if (append && cursor) paginationCursorsRef.current.delete(cursor)
        if (append) { loadingMoreRef.current = false; setLoadingMore(false); setAppendError(reason instanceof Error ? reason.message : 'Unable to load more reviews') } else { setState('error'); setError(reason instanceof Error ? reason.message : 'Unable to load reviews') }
      }).finally(() => { if (listController.current === controller) listController.current = null })
  }, [holdingId, onUnauthorized, portfolioId])
  useEffect(() => { load(undefined, false); return () => { listGeneration.current += 1; listController.current?.abort(); listController.current = null; detailGeneration.current += 1; detailController.current?.abort(); detailController.current = null } }, [load])
  const selectReview = (reviewId: string, button?: HTMLButtonElement) => {
    selectedButtonRef.current = reviewId; button?.focus(); setSelectedId(reviewId); setDetail(null); setDetailState('loading'); setDetailError('')
    const generation = ++detailGeneration.current; detailController.current?.abort(); const controller = new AbortController(); detailController.current = controller
    apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioId)}/reviews/${encodeURIComponent(reviewId)}`, { signal: controller.signal }).then((response) => jsonResponse<unknown>(response)).then((raw) => validateReview(raw)).then((review) => {
      if (controller.signal.aborted || generation !== detailGeneration.current) return
      const selectedReview = itemsRef.current.find((candidate) => candidate.review_id === reviewId)
      if (!selectedReview || review.portfolio_id !== portfolioId || review.review_id !== reviewId) throw new Error('Review identity does not match the requested review')
      if (holdingId && !matchesHoldingFilter(review, holdingId)) throw new Error('Review detail target does not match the requested holding filter')
      if (!sameReview(selectedReview, review)) throw new Error('Review detail does not match the selected list review')
      setDetail(review); setDetailState('ready'); window.setTimeout(() => detailRef.current?.focus(), 0)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== detailGeneration.current) return
      if (reason instanceof APIError && reason.status === 401) { onUnauthorized(); return }
      setDetailError(reason instanceof Error ? reason.message : 'Unable to load review'); setDetailState('error')
    }).finally(() => { if (detailController.current === controller) detailController.current = null })
  }
  const closeDetail = () => { detailGeneration.current += 1; detailController.current?.abort(); detailController.current = null; setSelectedId(null); setDetail(null); window.setTimeout(() => { if (selectedButtonRef.current) Array.from(document.querySelectorAll<HTMLButtonElement>('[data-review-id]')).find((button) => button.dataset.reviewId === selectedButtonRef.current)?.focus() }, 0) }
  const renderedItems = page?.items || []
  return <section className="reviews-card" aria-labelledby="reviews-title">
    <div className="section-heading"><div><span className="eyebrow">Immutable advisory analysis</span><h2 id="reviews-title">Reviews</h2></div><div className="review-actions">{holdingId && <span className="filter-chip">Filtered to exact lot {holdingId}</span>}{onBackToOverview && <button type="button" onClick={onBackToOverview}>Back to overview</button>}</div></div>
    <p ref={listStatusRef} className="visually-hidden" role="status" aria-live="polite" tabIndex={-1}>{listAnnouncement}</p>
    {state === 'loading' && <p role="status" aria-live="polite">Loading reviews…</p>}
    {state === 'error' && <p className="notice error" role="alert">Unable to load reviews: {error} <button type="button" onClick={() => load(undefined, false)}>Try again</button></p>}
    {state === 'ready' && renderedItems.length === 0 && <p className="empty">No reviews match this portfolio or holding filter.</p>}
    {state === 'ready' && renderedItems.length > 0 && <div className="review-list" aria-label="Advisory reviews">{renderedItems.map((review) => { const target = targetState(review); return <article className="review-card" key={review.review_id}><div className="review-card-heading"><div><h3>{review.recommendation.replaceAll('_', ' ')}</h3><p className="muted">{target.label}</p></div><span className={`state-pill state-${review.status}`}>{review.status}</span></div><p>{review.rationale}</p><div className="review-card-meta"><span>Snapshot {review.snapshot_id}</span><span>Submitted {displayDate(review.submitted_at)}</span><SnapshotBadge review={review} snapshot={snapshot} /></div>{!target.valid && <p className="notice warning">This review is not attached to a holding because its exact target identity is unavailable.</p>}<button type="button" data-review-id={review.review_id} onClick={(event) => selectReview(review.review_id, event.currentTarget)}>View review details</button></article>})}</div>}
    {appendError && <p className="notice error" role="alert">Unable to load more reviews: {appendError} <button type="button" onClick={() => load(page?.next_cursor, true)}>Retry loading reviews</button></p>}
    {page?.has_more && !appendError && <button type="button" onClick={() => load(page.next_cursor, true)} disabled={loadingMore} aria-busy={loadingMore}>{loadingMore ? 'Loading more reviews…' : 'Load more reviews'}</button>}
    {selectedId && detailState === 'loading' && <p role="status" aria-live="polite">Loading review detail…</p>}
    {selectedId && detailState === 'error' && <p className="notice error" role="alert">Unable to load review detail: {detailError} <button type="button" onClick={() => selectReview(selectedId)}>Retry review detail</button></p>}
    {detail && <><button type="button" onClick={closeDetail}>Close review detail</button><ReviewDetail key={`${portfolioId}:${detail.review_id}`} review={detail} portfolioId={portfolioId} snapshot={snapshot} csrfToken={csrfToken} onUnauthorized={onUnauthorized} detailRef={detailRef} feedbackAttempts={feedbackAttemptStore} /></>}
  </section>
}
