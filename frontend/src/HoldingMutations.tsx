import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { apiFetch, APIError } from './api'
import type { Holding } from './App'

const identifierPattern = /^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$/
const symbolPattern = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/
const currencyPattern = /^[A-Z]{3}$/
const quantityPattern = /^[1-9][0-9]*(?:\.[0-9]{1,18})?$/
const moneyPattern = /^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,18})?$/
const datePattern = /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/
const timestampPattern = /^[0-9]{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12][0-9]|3[01])T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]\.[0-9]{9}Z$/

export type HoldingMutationOperation = 'create' | 'remove'
type InstrumentMode = 'existing' | 'inline'
type Draft = {
  mode: InstrumentMode
  instrumentId: string
  symbol: string
  instrumentCurrency: string
  description: string
  assetType: string
  exchange: string
  quantity: string
  unitCost: string
  costBasis: string
  currency: string
  accountId: string
  acquisitionDate: string
}
type ReceiptExpectation = {
  operation: HoldingMutationOperation
  portfolioId: string
  idempotencyKey: string
  body: string
  lotId?: string
}

const emptyDraft = (): Draft => ({ mode: 'inline', instrumentId: '', symbol: '', instrumentCurrency: '', description: '', assetType: '', exchange: '', quantity: '', unitCost: '', costBasis: '', currency: '', accountId: '', acquisitionDate: '' })
const receiptKeys = ['operation', 'result', 'holding', 'portfolio_revision', 'snapshot_state', 'current_snapshot_id', 'actor', 'audit_event_id', 'activity_event_id', 'idempotency_key']
const holdingKeys = ['portfolio_id', 'holding_id', 'instrument_id', 'symbol', 'description', 'quantity', 'unit_cost', 'cost_basis', 'currency', 'account_id', 'acquisition_date', 'lifecycle', 'created_at', 'removed_at']
const actorKeys = ['category', 'id', 'permissions']
const isRecord = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === 'object' && !Array.isArray(value)
const keysAre = (value: Record<string, unknown>, allowed: string[]) => Object.keys(value).every((key) => allowed.includes(key))
const boundedIdentifier = (value: unknown) => typeof value === 'string' && identifierPattern.test(value)
const boundedText = (value: unknown, max: number) => typeof value === 'string' && value.length >= 1 && value.length <= max
const nullableText = (value: unknown, max: number) => value === null || (typeof value === 'string' && value.length >= 1 && value.length <= max)
const canonicalDate = (value: unknown) => {
  if (typeof value !== 'string' || !datePattern.test(value)) return false
  try { return new Date(`${value}T00:00:00.000Z`).toISOString().slice(0, 10) === value } catch { return false }
}
const canonicalTimestamp = (value: unknown) => typeof value === 'string' && timestampPattern.test(value) && isFinite(Date.parse(value))

function decimalParts(value: string) {
  const [whole, fraction = ''] = value.split('.')
  return { digits: BigInt(whole + fraction), scale: fraction.length }
}

function exactProductMatches(quantity: string, unitCost: string, costBasis: string) {
  const q = decimalParts(quantity)
  const u = decimalParts(unitCost)
  const c = decimalParts(costBasis)
  const productScale = q.scale + u.scale
  const targetScale = Math.max(productScale, c.scale)
  const product = q.digits * u.digits * (10n ** BigInt(targetScale - productScale))
  const cost = c.digits * (10n ** BigInt(targetScale - c.scale))
  return product === cost
}

function randomIdempotencyKey() {
  const bytes = new Uint8Array(16)
  if (!globalThis.crypto?.getRandomValues) throw new Error('Secure retry keys are unavailable in this browser.')
  globalThis.crypto.getRandomValues(bytes)
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

function unexpected(value: unknown, allowed: string[]) {
  return !isRecord(value) || !keysAre(value, allowed)
}

export function validateHoldingMutationReceipt(value: unknown, expected: ReceiptExpectation) {
  if (unexpected(value, receiptKeys)) throw new Error('The server returned an invalid holding receipt.')
  const receipt = value as Record<string, unknown>
  if (receipt.operation !== expected.operation || (receipt.result !== 'applied' && receipt.result !== 'replayed') || receipt.idempotency_key !== expected.idempotencyKey) throw new Error('The server returned an invalid holding receipt.')
  if (typeof receipt.portfolio_revision !== 'number' || !isFinite(receipt.portfolio_revision) || receipt.portfolio_revision % 1 !== 0 || receipt.portfolio_revision < 1 || receipt.portfolio_revision > 9007199254740991 || !boundedIdentifier(receipt.audit_event_id) || !boundedIdentifier(receipt.activity_event_id)) throw new Error('The server returned an invalid holding receipt.')
  if (receipt.snapshot_state !== 'stale' && receipt.snapshot_state !== 'no_snapshot') throw new Error('The server returned an invalid holding receipt.')
  if (receipt.snapshot_state === 'no_snapshot' ? receipt.current_snapshot_id !== null : !boundedIdentifier(receipt.current_snapshot_id)) throw new Error('The server returned an invalid holding receipt.')

  if (unexpected(receipt.actor, actorKeys)) throw new Error('The server returned an invalid holding receipt.')
  const actor = receipt.actor as Record<string, unknown>
  if (actor.category !== 'browser_user' || actor.id !== 'local-owner' || !Array.isArray(actor.permissions) || actor.permissions.length !== 1 || actor.permissions[0] !== 'portfolio:read') throw new Error('The server returned an invalid holding receipt.')

  if (unexpected(receipt.holding, holdingKeys)) throw new Error('The server returned an invalid holding receipt.')
  const holding = receipt.holding as Record<string, unknown>
  if (holding.portfolio_id !== expected.portfolioId || !boundedIdentifier(holding.holding_id) || !boundedIdentifier(holding.instrument_id) || !boundedText(holding.symbol, 64) || !boundedText(holding.quantity, 40) || !quantityPattern.test(holding.quantity as string) || !boundedText(holding.unit_cost, 40) || !moneyPattern.test(holding.unit_cost as string) || !boundedText(holding.cost_basis, 40) || !moneyPattern.test(holding.cost_basis as string) || typeof holding.currency !== 'string' || !currencyPattern.test(holding.currency) || (holding.description !== undefined && !nullableText(holding.description, 256)) || !nullableText(holding.account_id, 128) || !nullableText(holding.acquisition_date, 10) || (holding.acquisition_date !== null && !canonicalDate(holding.acquisition_date)) || (holding.removed_at !== null && !canonicalTimestamp(holding.removed_at)) || !canonicalTimestamp(holding.created_at)) throw new Error('The server returned an invalid holding receipt.')
  if (expected.operation === 'create' && (holding.lifecycle !== 'active' || holding.removed_at !== null)) throw new Error('The server returned an invalid holding receipt.')
  if (expected.operation === 'remove' && (holding.holding_id !== expected.lotId || holding.lifecycle !== 'removed' || holding.removed_at === null)) throw new Error('The server returned an invalid holding receipt.')

  let body: Record<string, unknown> | null = null
  try { body = JSON.parse(expected.body) as Record<string, unknown> } catch { body = null }
  if (expected.operation === 'create' && body && isRecord(body)) {
    if (holding.quantity !== body.quantity || holding.unit_cost !== body.unit_cost || holding.cost_basis !== body.cost_basis || holding.currency !== body.currency) throw new Error('The server returned an invalid holding receipt.')
    if (isRecord(body.instrument)) {
      if ('instrument_id' in body.instrument && holding.instrument_id !== body.instrument.instrument_id) throw new Error('The server returned an invalid holding receipt.')
      if ('symbol' in body.instrument && holding.symbol !== body.instrument.symbol) throw new Error('The server returned an invalid holding receipt.')
      if ('currency' in body.instrument && body.instrument.currency !== holding.currency) throw new Error('The server returned an invalid holding receipt.')
    }
  }
  return receipt
}

function errorMessage(status: number, code?: string) {
  if (status === 400) return 'Check the holding fields and exact decimal/date formats.'
  if (status === 403) return 'This session is not authorized to change holdings.'
  if (status === 404) return 'The requested portfolio or instrument was not found.'
  if (status === 409 && code === 'stale_revision') return 'The overview is stale. Reloaded the latest overview; review the draft and try again.'
  if (status === 409 && code === 'holding_not_active') return 'This lot is already archived or is no longer active.'
  if (status === 409) return 'This retry key conflicts with another request. Try the operation again.'
  if (status === 413) return 'The holding request is too large.'
  if (status === 415) return 'The browser sent an unsupported request format.'
  if (status >= 500) return 'The server could not apply the holding change. Try again.'
  return 'The holding change could not be applied. Try again.'
}

async function responseErrorCode(response: Response) {
  try {
    const body = await response.clone().json() as { error?: { code?: unknown } }
    return typeof body.error?.code === 'string' ? body.error.code : undefined
  } catch { return undefined }
}

type Props = {
  portfolioId: string
  overviewETag?: string
  csrfToken?: string
  holdings: Holding[]
  active: boolean
  scopeKey: string
  onRefresh: () => Promise<string | void>
  onUnauthorized: () => void
}

type Attempt = { signature: string; key: string; body: string }

export default function HoldingMutations({ portfolioId, overviewETag, csrfToken, holdings, active, scopeKey, onRefresh, onUnauthorized }: Props) {
  const [formOpen, setFormOpen] = useState(false)
  const [draft, setDraft] = useState(emptyDraft)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [archiveLot, setArchiveLot] = useState<Holding | null>(null)
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')
  const [error, setError] = useState('')
  const busyRef = useRef(false)
  const createAttempt = useRef<Attempt | null>(null)
  const removeAttempt = useRef<Attempt | null>(null)
  const etagRef = useRef<string | undefined>(overviewETag)
  const controllerRef = useRef<AbortController | null>(null)
  const generationRef = useRef(0)
  const formHeadingRef = useRef<HTMLHeadingElement>(null)
  const dialogHeadingRef = useRef<HTMLHeadingElement>(null)
  const addButtonRef = useRef<HTMLButtonElement>(null)

  const abortWork = () => {
    generationRef.current += 1
    controllerRef.current?.abort()
    controllerRef.current = null
    busyRef.current = false
    setBusy(false)
  }
  useEffect(() => {
    if (!active) abortWork()
    return () => abortWork()
  }, [active, portfolioId, scopeKey])
  useEffect(() => { etagRef.current = overviewETag }, [overviewETag, scopeKey])
  useEffect(() => { if (formOpen) formHeadingRef.current?.focus() }, [formOpen])
  useEffect(() => { if (archiveLot) dialogHeadingRef.current?.focus() }, [archiveLot])

  const update = (field: keyof Draft, value: string) => {
    setDraft((current) => ({ ...current, [field]: value }))
    setErrors((current) => { const next = { ...current }; delete next[field]; delete next.form; return next })
  }

  const validateDraft = () => {
    const next: Record<string, string> = {}
    if (draft.mode === 'existing') {
      if (!identifierPattern.test(draft.instrumentId)) next.instrumentId = 'Enter the immutable instrument ID.'
    } else {
      if (!draft.symbol || draft.symbol.length > 64 || !symbolPattern.test(draft.symbol)) next.symbol = 'Enter a valid instrument symbol.'
      if (!currencyPattern.test(draft.instrumentCurrency)) next.instrumentCurrency = 'Use three uppercase currency letters.'
      for (const [field, label, max] of [['description', 'Description', 256], ['assetType', 'Asset type', 64], ['exchange', 'Exchange', 64] ] as const) {
        if (draft[field].length > max) next[field] = `${label} is too long.`
      }
      if (draft.assetType && !/^[A-Za-z0-9._-]+$/.test(draft.assetType)) next.assetType = 'Use letters, numbers, dot, underscore or hyphen.'
      if (draft.exchange && !/^[A-Za-z0-9._-]+$/.test(draft.exchange)) next.exchange = 'Use letters, numbers, dot, underscore or hyphen.'
    }
    if (!quantityPattern.test(draft.quantity)) next.quantity = 'Use a positive canonical decimal.'
    if (!moneyPattern.test(draft.unitCost)) next.unitCost = 'Use a non-negative canonical decimal.'
    if (!moneyPattern.test(draft.costBasis)) next.costBasis = 'Use a non-negative canonical decimal.'
    if (quantityPattern.test(draft.quantity) && moneyPattern.test(draft.unitCost) && moneyPattern.test(draft.costBasis) && !exactProductMatches(draft.quantity, draft.unitCost, draft.costBasis)) next.costBasis = 'Quantity × unit cost must equal cost basis exactly.'
    if (!currencyPattern.test(draft.currency)) next.currency = 'Use three uppercase currency letters.'
    if (draft.mode === 'inline' && currencyPattern.test(draft.instrumentCurrency) && draft.currency !== draft.instrumentCurrency) next.currency = 'Holding currency must match instrument currency.'
    if (draft.accountId && !identifierPattern.test(draft.accountId)) next.accountId = 'Enter a valid account ID.'
    if (draft.acquisitionDate && (!datePattern.test(draft.acquisitionDate) || !canonicalDate(draft.acquisitionDate))) next.acquisitionDate = 'Use a real date in YYYY-MM-DD form.'
    return next
  }

  const create = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busyRef.current || !active) return
    const nextErrors = validateDraft()
    setErrors(nextErrors)
    setError('')
    if (Object.keys(nextErrors).length) return
    const etag = etagRef.current
    if (!etag) { setError('No current overview revision is available. Reload the overview before adding a holding.'); return }
    const instrument = draft.mode === 'existing'
      ? { instrument_id: draft.instrumentId }
      : { symbol: draft.symbol, currency: draft.instrumentCurrency, ...(draft.description ? { description: draft.description } : {}), ...(draft.assetType ? { asset_type: draft.assetType } : {}), ...(draft.exchange ? { exchange: draft.exchange } : {}) }
    const payload = { instrument, quantity: draft.quantity, unit_cost: draft.unitCost, cost_basis: draft.costBasis, currency: draft.currency, ...(draft.accountId ? { account_id: draft.accountId } : {}), ...(draft.acquisitionDate ? { acquisition_date: draft.acquisitionDate } : {}) }
    const body = JSON.stringify(payload)
    const signature = `${portfolioId}|${etag}|${body}`
    const attempt = createAttempt.current?.signature === signature ? createAttempt.current : { signature, key: randomIdempotencyKey(), body }
    createAttempt.current = attempt
    await submit('create', attempt, undefined, `/api/v1/portfolios/${encodeURIComponent(portfolioId)}/holdings`, 'POST', body)
  }

  const submit = async (operation: HoldingMutationOperation, attempt: Attempt, lotId: string | undefined, url: string, method: 'POST' | 'DELETE', body?: string) => {
    const etag = etagRef.current
    if (busyRef.current || !active || !etag) return
    const generation = generationRef.current
    const controller = new AbortController()
    controllerRef.current = controller
    busyRef.current = true; setBusy(true); setError(''); setStatus(operation === 'create' ? 'Creating holding…' : 'Archiving holding…')
    try {
      const response = await apiFetch(url, { method, body, signal: controller.signal, csrfToken, headers: { ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}), 'If-Match': etag, 'Idempotency-Key': attempt.key } })
      if (generation !== generationRef.current || controller.signal.aborted) return
      if (!response.ok) {
        const code = await responseErrorCode(response)
        if (response.status !== 401) {
          if (operation === 'create') createAttempt.current = null
          else removeAttempt.current = null
        }
        if (response.status === 409 && code === 'stale_revision') await onRefresh()
        throw new APIError(response.status, errorMessage(response.status, code))
      }
      const receipt = await response.json()
      validateHoldingMutationReceipt(receipt, { operation, portfolioId, idempotencyKey: attempt.key, body: attempt.body, lotId })
      const result = (receipt as { result: 'applied' | 'replayed' }).result
      const refreshedETag = await onRefresh()
      if (generation !== generationRef.current || controller.signal.aborted) return
      if (typeof refreshedETag === 'string') etagRef.current = refreshedETag
      if (operation === 'create') { createAttempt.current = null; setDraft(emptyDraft()); setFormOpen(false) } else { removeAttempt.current = null; setArchiveLot(null) }
      setStatus(`${operation === 'create' ? 'Holding creation' : 'Holding archive'} ${result === 'replayed' ? 'replayed' : 'applied'} successfully. Overview refreshed; snapshot state is ${receipt.snapshot_state === 'no_snapshot' ? 'no snapshot' : 'stale'}.`)
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) {
        createAttempt.current = null; removeAttempt.current = null; abortWork(); setDraft(emptyDraft()); setFormOpen(false); setArchiveLot(null); setErrors({}); setStatus(''); setError(''); onUnauthorized(); return
      }
      if (reason instanceof TypeError) { setStatus(''); setError('Network uncertainty. Retry with the same safe attempt; the draft is preserved.'); return }
      setStatus(''); setError(reason instanceof Error ? reason.message : 'The holding change could not be applied. Try again.')
    } finally {
      if (controllerRef.current === controller) { controllerRef.current = null; busyRef.current = false; setBusy(false) }
    }
  }

  const confirmArchive = async () => {
    if (!archiveLot || busyRef.current || !etagRef.current) return
    const lotId = archiveLot.lot_id
    const etag = etagRef.current
    if (!etag) return
    const signature = `${portfolioId}|${etag}|${lotId}|remove`
    const attempt = removeAttempt.current?.signature === signature ? removeAttempt.current : { signature, key: randomIdempotencyKey(), body: '' }
    removeAttempt.current = attempt
    await submit('remove', attempt, lotId, `/api/v1/portfolios/${encodeURIComponent(portfolioId)}/holdings/${encodeURIComponent(lotId)}`, 'DELETE')
  }

  if (!active) return null
  return <>
    <section className="holding-mutation-card" aria-labelledby="holding-mutation-title">
      <div className="section-heading"><div><span className="eyebrow">Authenticated portfolio control</span><h2 id="holding-mutation-title">Holding changes</h2></div><button ref={addButtonRef} type="button" onClick={() => { setFormOpen(true); setError(''); setStatus('') }} disabled={busy}>Add holding</button></div>
      {status && <p className="notice success" role="status" aria-live="polite">{status}</p>}
      {error && <p className="notice error" role="alert">{error}</p>}
      {formOpen && <form className="holding-form" onSubmit={create} aria-labelledby="holding-form-title">
        <h3 id="holding-form-title" ref={formHeadingRef} tabIndex={-1}>Add one holding</h3>
        <p className="muted">Use an existing portfolio instrument ID or define one new instrument. Financial values stay as exact text.</p>
        <fieldset><legend>Instrument mode</legend><label><input type="radio" name="instrument-mode" checked={draft.mode === 'existing'} onChange={() => update('mode', 'existing')} /> Existing instrument ID</label><label><input type="radio" name="instrument-mode" checked={draft.mode === 'inline'} onChange={() => update('mode', 'inline')} /> New inline instrument</label></fieldset>
        {draft.mode === 'existing' ? <Field id="instrument-id" label="Instrument ID" value={draft.instrumentId} error={errors.instrumentId} onChange={(value) => update('instrumentId', value)} /> : <div className="holding-fields"><Field id="symbol" label="Symbol" value={draft.symbol} error={errors.symbol} onChange={(value) => update('symbol', value)} /><Field id="instrument-currency" label="Instrument currency" value={draft.instrumentCurrency} error={errors.instrumentCurrency} onChange={(value) => update('instrumentCurrency', value)} /><Field id="description" label="Description (optional)" value={draft.description} error={errors.description} onChange={(value) => update('description', value)} /><Field id="asset-type" label="Asset type (optional)" value={draft.assetType} error={errors.assetType} onChange={(value) => update('assetType', value)} /><Field id="exchange" label="Exchange (optional)" value={draft.exchange} error={errors.exchange} onChange={(value) => update('exchange', value)} /></div>}
        <div className="holding-fields"><Field id="quantity" label="Quantity" value={draft.quantity} error={errors.quantity} onChange={(value) => update('quantity', value)} inputMode="decimal" /><Field id="unit-cost" label="Unit cost" value={draft.unitCost} error={errors.unitCost} onChange={(value) => update('unitCost', value)} inputMode="decimal" /><Field id="cost-basis" label="Cost basis" value={draft.costBasis} error={errors.costBasis} onChange={(value) => update('costBasis', value)} inputMode="decimal" /><Field id="currency" label="Holding currency" value={draft.currency} error={errors.currency} onChange={(value) => update('currency', value)} /><Field id="account-id" label="Account ID (optional)" value={draft.accountId} error={errors.accountId} onChange={(value) => update('accountId', value)} /><Field id="acquisition-date" label="Acquisition date (optional)" value={draft.acquisitionDate} error={errors.acquisitionDate} onChange={(value) => update('acquisitionDate', value)} inputMode="numeric" /></div>
        {errors.form && <p className="field-error" role="alert">{errors.form}</p>}
        <div className="holding-form-actions"><button type="submit" disabled={busy || !etagRef.current} aria-busy={busy}>{busy ? 'Creating holding…' : 'Create holding'}</button><button type="button" onClick={() => { setFormOpen(false); setErrors({}); setError(''); addButtonRef.current?.focus() }} disabled={busy}>Cancel</button></div>
      </form>}
      <div className="archive-controls"><h3>Archive an active lot</h3><p className="muted">Archive is a logical removal only; history and audit records are preserved.</p>{holdings.map((holding) => <div className="archive-row" key={holding.lot_id}><span><strong>Archive lot {holding.lot_id}</strong><span className="subline">Logical removal; history preserved</span></span><button type="button" onClick={() => setArchiveLot(holding)} disabled={busy || !etagRef.current} aria-label={`Archive lot ${holding.lot_id}`}>Archive lot</button></div>)}</div>
    </section>
    {archiveLot && <div className="confirmation-backdrop"><section className="confirmation-dialog" role="dialog" aria-modal="true" aria-labelledby="archive-dialog-title"><h2 id="archive-dialog-title" ref={dialogHeadingRef} tabIndex={-1}>Archive lot {archiveLot.lot_id}?</h2><p> This will logically archive the exact lot, not sell, trade, or permanently delete it. Its financial history, reviews, thesis and audit records remain preserved.</p><div className="holding-form-actions"><button type="button" onClick={confirmArchive} disabled={busy || !etagRef.current} aria-busy={busy}>{busy ? 'Archiving lot…' : `Confirm archive lot ${archiveLot.lot_id}`}</button><button type="button" onClick={() => { setArchiveLot(null); addButtonRef.current?.focus() }} disabled={busy}>Cancel archive</button></div></section></div>}
  </>
}

function Field({ id, label, value, error, onChange, inputMode }: { id: string; label: string; value: string; error?: string; onChange: (value: string) => void; inputMode?: 'decimal' | 'numeric' }) {
  const errorId = `${id}-error`
  return <div className="field"><label htmlFor={id}>{label}</label><input id={id} value={value} onChange={(event) => onChange(event.target.value)} inputMode={inputMode} aria-invalid={Boolean(error)} aria-describedby={error ? errorId : undefined} />{error && <p id={errorId} className="field-error">{error}</p>}</div>
}
