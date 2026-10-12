import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent, RefObject } from 'react'
import { apiFetch, APIError } from './api'
import type { Holding } from './App'

export type ThesisSource = {
  label: string
  url: string
  publication_date?: string | null
}

export type Thesis = {
  id: string
  portfolio_id: string
  holding_id: string
  instrument_id: string
  summary: string
  intended_holding_period: string
  allocation_limit?: string | null
  reasons_to_hold: string[]
  risks: string[]
  reasons_to_sell: string[]
  conditions_to_change: string[]
  sources: ThesisSource[]
  revision: number
  revision_id: string
  updated_at: string
}

export type ThesisRevision = Thesis & { created_at: string }
export type ThesisHistory = { items: ThesisRevision[] }

export type ThesisDraft = {
  summary: string
  intended_holding_period: string
  allocation_limit: string
  reasons_to_hold: string[]
  risks: string[]
  reasons_to_sell: string[]
  conditions_to_change: string[]
  sources: ThesisSource[]
}

type RequestState = 'idle' | 'loading' | 'ready' | 'error'

type Props = {
  portfolioId: string
  holding: Holding
  csrfToken?: string
  onClose: () => void
  onUnauthorized: () => void
  returnFocusRef?: RefObject<HTMLButtonElement | null>
}

const thesisKeys = new Set(['id', 'portfolio_id', 'holding_id', 'instrument_id', 'summary', 'intended_holding_period', 'allocation_limit', 'reasons_to_hold', 'risks', 'reasons_to_sell', 'conditions_to_change', 'sources', 'revision', 'revision_id', 'updated_at'])
const revisionKeys = new Set([...thesisKeys, 'created_at'])
const sourceKeys = new Set(['label', 'url', 'publication_date'])
const allocationPattern = /^(?:0|[1-9]?[0-9](?:\.[0-9]{1,2})?|100(?:\.0{1,2})?)$/

const emptyDraft = (): ThesisDraft => ({ summary: '', intended_holding_period: '', allocation_limit: '', reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [] })

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function validString(value: unknown, max = Infinity): value is string {
  return typeof value === 'string' && value.length <= max
}

function validateSource(value: unknown): ThesisSource {
  if (!isRecord(value) || [...Object.keys(value)].some((key) => !sourceKeys.has(key))) throw new Error('Malformed thesis source')
  if (!validString(value.label, 256) || value.label.length < 1 || !validString(value.url, 2048) || value.url.length < 1) throw new Error('Malformed thesis source')
  try {
    if (!new URL(value.url).protocol) throw new Error('Malformed thesis source')
  } catch {
    throw new Error('Malformed thesis source')
  }
  if (value.publication_date !== undefined && value.publication_date !== null && (typeof value.publication_date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value.publication_date))) throw new Error('Malformed thesis source')
  return { label: value.label, url: value.url, publication_date: value.publication_date as string | null | undefined }
}

function validateThesis(value: unknown, includeCreatedAt = false): Thesis | ThesisRevision {
  if (!isRecord(value)) throw new Error('Malformed thesis response')
  const keys = includeCreatedAt ? revisionKeys : thesisKeys
  if (Object.keys(value).some((key) => !keys.has(key))) throw new Error('Malformed thesis response')
  const required = ['id', 'portfolio_id', 'holding_id', 'instrument_id', 'summary', 'intended_holding_period', 'reasons_to_hold', 'risks', 'reasons_to_sell', 'conditions_to_change', 'sources', 'revision', 'revision_id', 'updated_at']
  if (includeCreatedAt) required.push('created_at')
  if (required.some((key) => !Object.prototype.hasOwnProperty.call(value, key))) throw new Error('Malformed thesis response')
  if (!validString(value.id, 128) || !validString(value.portfolio_id, 128) || !validString(value.holding_id, 128) || !validString(value.instrument_id, 128) || !validString(value.summary, 4000) || value.summary.length < 1 || !validString(value.intended_holding_period, 256) || value.intended_holding_period.length < 1 || !validString(value.revision_id, 128) || !validString(value.updated_at, 128)) throw new Error('Malformed thesis response')
  if (includeCreatedAt && !validString(value.created_at, 128)) throw new Error('Malformed thesis response')
  if (typeof value.revision !== 'number' || Math.floor(value.revision) !== value.revision || value.revision < 1) throw new Error('Malformed thesis response')
  if (value.allocation_limit !== undefined && value.allocation_limit !== null && (!validString(value.allocation_limit) || !allocationPattern.test(value.allocation_limit))) throw new Error('Malformed thesis response')
  const arrays = ['reasons_to_hold', 'risks', 'reasons_to_sell', 'conditions_to_change']
  if (arrays.some((key) => !Array.isArray(value[key]) || (value[key] as unknown[]).some((item) => !validString(item)))) throw new Error('Malformed thesis response')
  if (!Array.isArray(value.sources)) throw new Error('Malformed thesis response')
  const reasonsToHold = value.reasons_to_hold as string[]
  const risks = value.risks as string[]
  const reasonsToSell = value.reasons_to_sell as string[]
  const conditionsToChange = value.conditions_to_change as string[]
  const result = {
    id: value.id,
    portfolio_id: value.portfolio_id,
    holding_id: value.holding_id,
    instrument_id: value.instrument_id,
    summary: value.summary,
    intended_holding_period: value.intended_holding_period,
    allocation_limit: value.allocation_limit as string | null | undefined,
    reasons_to_hold: [...reasonsToHold],
    risks: [...risks],
    reasons_to_sell: [...reasonsToSell],
    conditions_to_change: [...conditionsToChange],
    sources: value.sources.map(validateSource),
    revision: value.revision,
    revision_id: value.revision_id,
    updated_at: value.updated_at,
  }
  return includeCreatedAt ? { ...result, created_at: value.created_at as string } : result
}

function draftFromThesis(thesis: Thesis): ThesisDraft {
  return {
    summary: thesis.summary,
    intended_holding_period: thesis.intended_holding_period,
    allocation_limit: thesis.allocation_limit ?? '',
    reasons_to_hold: [...thesis.reasons_to_hold],
    risks: [...thesis.risks],
    reasons_to_sell: [...thesis.reasons_to_sell],
    conditions_to_change: [...thesis.conditions_to_change],
    sources: thesis.sources.map((source) => ({ ...source })),
  }
}

function requestMessage(reason: unknown, action: string): string {
  if (reason instanceof APIError) {
    if (reason.status === 400) return `The ${action} was rejected because one or more fields are invalid.`
    if (reason.status === 403) return `You are not allowed to ${action}.`
    if (reason.status === 404) return 'This lot or thesis is no longer available.'
  }
  return reason instanceof Error ? reason.message : `Unable to ${action}.`
}

function ListEditor({ label, values, onChange }: { label: string; values: string[]; onChange: (values: string[]) => void }) {
  const update = (index: number, value: string) => onChange(values.map((item, itemIndex) => itemIndex === index ? value : item))
  const move = (index: number, direction: -1 | 1) => {
    const target = index + direction
    if (target < 0 || target >= values.length) return
    const next = [...values]
    const [item] = next.splice(index, 1)
    next.splice(target, 0, item)
    onChange(next)
  }
  return <fieldset className="thesis-list-fieldset"><legend>{label}</legend>
    {values.map((value, index) => <div className="thesis-list-row" key={`${label}-${index}`}>
      <label htmlFor={`${label}-${index}`}>{label} {index + 1}</label>
      <textarea id={`${label}-${index}`} value={value} onChange={(event) => update(index, event.target.value)} required maxLength={1000} />
      <div className="thesis-row-actions">
        <button type="button" onClick={() => move(index, -1)} disabled={index === 0} aria-label={`Move ${label} ${index + 1} up`}>Move up</button>
        <button type="button" onClick={() => move(index, 1)} disabled={index === values.length - 1} aria-label={`Move ${label} ${index + 1} down`}>Move down</button>
        <button type="button" onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))} aria-label={`Remove ${label} ${index + 1}`}>Remove</button>
      </div>
    </div>)}
    <button type="button" onClick={() => onChange([...values, ''])}>Add {label.toLowerCase()}</button>
  </fieldset>
}

function SourcesEditor({ sources, onChange }: { sources: ThesisSource[]; onChange: (sources: ThesisSource[]) => void }) {
  const update = (index: number, field: keyof ThesisSource, value: string) => onChange(sources.map((source, sourceIndex) => sourceIndex === index ? { ...source, [field]: value || (field === 'publication_date' ? null : '') } : source))
  const move = (index: number, direction: -1 | 1) => {
    const target = index + direction
    if (target < 0 || target >= sources.length) return
    const next = [...sources]
    const [source] = next.splice(index, 1)
    next.splice(target, 0, source)
    onChange(next)
  }
  return <fieldset className="thesis-list-fieldset"><legend>Sources</legend>
    {sources.map((source, index) => <div className="source-editor" key={`source-${index}`}>
      <div className="source-fields">
        <label htmlFor={`source-label-${index}`}>Source {index + 1} label</label><input id={`source-label-${index}`} value={source.label} onChange={(event) => update(index, 'label', event.target.value)} required maxLength={256} />
        <label htmlFor={`source-url-${index}`}>Source {index + 1} URL</label><input id={`source-url-${index}`} type="url" value={source.url} onChange={(event) => update(index, 'url', event.target.value)} required maxLength={2048} />
        <label htmlFor={`source-date-${index}`}>Source {index + 1} publication date (optional)</label><input id={`source-date-${index}`} type="date" value={source.publication_date ?? ''} onChange={(event) => update(index, 'publication_date', event.target.value)} />
      </div>
      <div className="thesis-row-actions">
        <button type="button" onClick={() => move(index, -1)} disabled={index === 0} aria-label={`Move source ${index + 1} up`}>Move up</button>
        <button type="button" onClick={() => move(index, 1)} disabled={index === sources.length - 1} aria-label={`Move source ${index + 1} down`}>Move down</button>
        <button type="button" onClick={() => onChange(sources.filter((_, sourceIndex) => sourceIndex !== index))} aria-label={`Remove source ${index + 1}`}>Remove</button>
      </div>
    </div>)}
    <button type="button" onClick={() => onChange([...sources, { label: '', url: '', publication_date: null }])}>Add source</button>
  </fieldset>
}

function ThesisContent({ thesis, heading }: { thesis: Thesis | ThesisRevision; heading: string }) {
  return <section className="thesis-content" aria-labelledby={heading}>
    <h4 id={heading}>{heading}</h4>
    <dl className="thesis-content-grid">
      <div><dt>Summary</dt><dd>{thesis.summary}</dd></div>
      <div><dt>Intended holding period</dt><dd>{thesis.intended_holding_period}</dd></div>
      <div><dt>Allocation limit</dt><dd>{thesis.allocation_limit ?? 'None'}</dd></div>
    </dl>
    {([['Reasons to hold', thesis.reasons_to_hold], ['Risks', thesis.risks], ['Reasons to sell', thesis.reasons_to_sell], ['Conditions to change', thesis.conditions_to_change]] as Array<[string, string[]]>).map(([label, values]) => <div key={label}><h5>{label}</h5>{values.length ? <ul>{values.map((value, index) => <li key={`${label}-${index}`}>{value}</li>)}</ul> : <p className="muted">None recorded.</p>}</div>)}
    <div><h5>Sources</h5>{thesis.sources.length ? <ul>{thesis.sources.map((source, index) => <li key={`${source.url}-${index}`}><a href={source.url} target="_blank" rel="noopener noreferrer">{source.label}</a>{source.publication_date && <span className="subline">Published {source.publication_date}</span>}</li>)}</ul> : <p className="muted">None recorded.</p>}</div>
  </section>
}

export default function ThesisEditor({ portfolioId, holding, csrfToken, onClose, onUnauthorized, returnFocusRef }: Props) {
  const [draft, setDraft] = useState<ThesisDraft>(() => emptyDraft())
  const [expectedRevision, setExpectedRevision] = useState(0)
  const [hasThesis, setHasThesis] = useState(false)
  const [loadState, setLoadState] = useState<RequestState>('loading')
  const [loadError, setLoadError] = useState('')
  const [saveError, setSaveError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [announcement, setAnnouncement] = useState('')
  const [historyState, setHistoryState] = useState<RequestState>('idle')
  const [history, setHistory] = useState<ThesisHistory | null>(null)
  const [historyError, setHistoryError] = useState('')
  const [saving, setSaving] = useState(false)
  const requestGeneration = useRef(0)
  const controllerRef = useRef<AbortController | null>(null)
  const savingRef = useRef(false)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const statusRef = useRef<HTMLParagraphElement>(null)
  const conflictRef = useRef<HTMLParagraphElement>(null)
  const formRef = useRef<HTMLFormElement>(null)
  const mountedRef = useRef(true)
  const endpoint = `/api/v1/portfolios/${encodeURIComponent(portfolioId)}/holdings/${encodeURIComponent(holding.lot_id)}/thesis`

  const abortRequest = useCallback(() => {
    requestGeneration.current += 1
    controllerRef.current?.abort()
    controllerRef.current = null
  }, [])

  const handleUnauthorized = useCallback(() => {
    abortRequest()
    onUnauthorized()
  }, [abortRequest, onUnauthorized])

  const loadCurrent = useCallback((replaceDraft: boolean) => {
    const generation = ++requestGeneration.current
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setLoadState('loading')
    setLoadError('')
    setSaveError('')
    if (!replaceDraft) setAnnouncement('Loading thesis…')
    apiFetch(endpoint, { signal: controller.signal })
      .then(async (response) => {
        if (response.status === 404) return null
        if (!response.ok) throw new APIError(response.status)
        return validateThesis(await response.json()) as Thesis
      })
      .then((nextThesis) => {
        if (controller.signal.aborted || generation !== requestGeneration.current || !mountedRef.current) return
        if (nextThesis === null) {
          setHasThesis(false); setExpectedRevision(0); setDraft(emptyDraft()); setLoadState('ready'); setAnnouncement('Thesis editor ready for a new thesis.')
        } else {
          setHasThesis(true); setExpectedRevision(nextThesis.revision); setDraft(draftFromThesis(nextThesis)); setLoadState('ready'); setAnnouncement(`Loaded thesis revision ${nextThesis.revision}.`)
        }
        setConflict(false)
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || generation !== requestGeneration.current || !mountedRef.current) return
        if (reason instanceof APIError && reason.status === 401) { handleUnauthorized(); return }
        setLoadState('error'); setLoadError(requestMessage(reason, 'load the thesis'))
      })
  }, [endpoint, handleUnauthorized])

  useEffect(() => {
    mountedRef.current = true
    headingRef.current?.focus()
    loadCurrent(false)
    return () => { mountedRef.current = false; abortRequest() }
  }, [abortRequest, loadCurrent])

  useEffect(() => () => { returnFocusRef?.current?.focus() }, [returnFocusRef])

  const updateDraft = <K extends keyof ThesisDraft>(field: K, value: ThesisDraft[K]) => setDraft((current) => ({ ...current, [field]: value }))

  const allocationError = draft.allocation_limit !== '' && !allocationPattern.test(draft.allocation_limit) ? 'Enter a percentage from 0 to 100 with at most two decimal places.' : ''

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (savingRef.current || loadState !== 'ready' || allocationError) return
    savingRef.current = true
    setSaving(true); setSaveError(''); setConflict(false); setAnnouncement('Saving thesis…')
    const controller = new AbortController()
    controllerRef.current?.abort()
    controllerRef.current = controller
    const generation = ++requestGeneration.current
    const payload = {
      summary: draft.summary,
      intended_holding_period: draft.intended_holding_period,
      allocation_limit: draft.allocation_limit === '' ? null : draft.allocation_limit,
      reasons_to_hold: [...draft.reasons_to_hold],
      risks: [...draft.risks],
      reasons_to_sell: [...draft.reasons_to_sell],
      conditions_to_change: [...draft.conditions_to_change],
      sources: draft.sources.map((source) => ({ ...source, publication_date: source.publication_date || null })),
      expected_revision: expectedRevision,
    }
    try {
      const response = await apiFetch(endpoint, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload), csrfToken, signal: controller.signal })
      if (response.status === 409) { if (generation === requestGeneration.current && mountedRef.current) { setConflict(true); setSaveError('A newer revision exists. Your unsaved changes were preserved. Reload the latest revision only if you choose to replace this draft.'); setAnnouncement('Save conflict. Unsaved changes preserved.') }; return }
      if (!response.ok) throw new APIError(response.status)
      const canonical = validateThesis(await response.json()) as Thesis
      if (generation !== requestGeneration.current || !mountedRef.current) return
      setHasThesis(true); setExpectedRevision(canonical.revision); setDraft(draftFromThesis(canonical)); setSaveError(''); setConflict(false); setAnnouncement(`Thesis saved as revision ${canonical.revision}.`)
    } catch (reason) {
      if (controller.signal.aborted || generation !== requestGeneration.current || !mountedRef.current) return
      if (reason instanceof APIError && reason.status === 401) { handleUnauthorized(); return }
      setSaveError(requestMessage(reason, 'save the thesis'))
      setAnnouncement('The thesis was not saved. Your draft is preserved.')
    } finally {
      if (generation === requestGeneration.current && mountedRef.current) { savingRef.current = false; setSaving(false) }
    }
  }

  const reloadLatest = () => { setAnnouncement('Reloading the latest thesis…'); loadCurrent(true) }

  useEffect(() => {
    if (conflict) conflictRef.current?.focus()
  }, [conflict])

  const loadHistory = () => {
    if (historyState === 'loading' || savingRef.current) return
    const generation = ++requestGeneration.current
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setHistoryState('loading'); setHistoryError('')
    apiFetch(`${endpoint}/history`, { signal: controller.signal })
      .then(async (response) => {
        if (response.status === 404) return { items: [] } as ThesisHistory
        if (!response.ok) throw new APIError(response.status)
        const value = await response.json()
        if (!isRecord(value) || Object.keys(value).some((key) => key !== 'items') || !Array.isArray(value.items)) throw new Error('Malformed thesis history response')
        return { items: value.items.map((item) => validateThesis(item, true) as ThesisRevision) }
      })
      .then((nextHistory) => {
        if (controller.signal.aborted || generation !== requestGeneration.current || !mountedRef.current) return
        setHistory(nextHistory); setHistoryState('ready'); setAnnouncement(`Loaded ${nextHistory.items.length} thesis revision${nextHistory.items.length === 1 ? '' : 's'}.`)
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || generation !== requestGeneration.current || !mountedRef.current) return
        if (reason instanceof APIError && reason.status === 401) { handleUnauthorized(); return }
        setHistoryState('error'); setHistoryError(requestMessage(reason, 'load thesis history'))
      })
  }

  return <section className="thesis-surface" aria-labelledby="thesis-title">
    <div className="thesis-header">
      <div><span className="eyebrow">Lot detail</span><h2 id="thesis-title" ref={headingRef} tabIndex={-1}>Investment thesis · {holding.symbol}</h2><p className="muted">{holding.name || holding.description || holding.symbol} · lot {holding.lot_id} · {holding.currency}</p><dl className="thesis-lot-context"><div><dt>Quantity</dt><dd>{holding.quantity}</dd></div><div><dt>Cost basis</dt><dd>{holding.cost_basis || 'Unavailable'}</dd></div><div><dt>Latest price</dt><dd>{holding.latest_price || 'Unavailable'}</dd></div></dl></div>
      <button type="button" onClick={onClose}>Close thesis</button>
    </div>
    <p ref={statusRef} className="visually-hidden" role="status" aria-live="polite" tabIndex={-1}>{announcement}</p>
    {loadState === 'loading' && <p className="notice" role="status">Loading thesis…</p>}
    {loadState === 'error' && <div className="notice error" role="alert"><p>{loadError}</p><button type="button" onClick={() => loadCurrent(false)}>Try again</button></div>}
    {loadState === 'ready' && <>
      {!hasThesis && <p className="notice" role="status">No thesis has been reviewed for this lot yet. Create one below.</p>}
      <form ref={formRef} className="thesis-form" onSubmit={save} aria-describedby="thesis-form-help">
        <p id="thesis-form-help" className="muted">Revision expected on save: {expectedRevision}. Empty list fields remain empty arrays.</p>
        <label htmlFor="thesis-summary">Summary</label><textarea id="thesis-summary" value={draft.summary} onChange={(event) => updateDraft('summary', event.target.value)} required maxLength={4000} />
        <label htmlFor="thesis-period">Intended holding period</label><input id="thesis-period" value={draft.intended_holding_period} onChange={(event) => updateDraft('intended_holding_period', event.target.value)} required maxLength={256} />
        <label htmlFor="thesis-allocation-limit">Optional allocation limit (%)</label><input id="thesis-allocation-limit" inputMode="decimal" type="text" value={draft.allocation_limit} onChange={(event) => updateDraft('allocation_limit', event.target.value)} aria-describedby={allocationError ? 'allocation-limit-error' : undefined} />
        {allocationError && <p id="allocation-limit-error" className="field-error" role="alert">{allocationError}</p>}
        <ListEditor label="Reasons to hold" values={draft.reasons_to_hold} onChange={(values) => updateDraft('reasons_to_hold', values)} />
        <ListEditor label="Risks" values={draft.risks} onChange={(values) => updateDraft('risks', values)} />
        <ListEditor label="Reasons to sell" values={draft.reasons_to_sell} onChange={(values) => updateDraft('reasons_to_sell', values)} />
        <ListEditor label="Conditions to change" values={draft.conditions_to_change} onChange={(values) => updateDraft('conditions_to_change', values)} />
        <SourcesEditor sources={draft.sources} onChange={(sources) => updateDraft('sources', sources)} />
        {saveError && <p ref={conflict ? conflictRef : undefined} className="notice error" role="alert" tabIndex={-1}>{saveError}{conflict ? <button type="button" onClick={reloadLatest}>Reload latest revision</button> : <button type="button" onClick={() => formRef.current?.requestSubmit()}>Retry save</button>}</p>}
        <div className="thesis-form-actions"><button type="submit" disabled={saving || Boolean(allocationError)} aria-busy={saving}>{saving ? 'Saving thesis…' : 'Save thesis'}</button><button type="button" onClick={onClose}>Cancel</button></div>
      </form>
      <section className="thesis-history" aria-labelledby="thesis-history-title"><div className="section-heading"><h3 id="thesis-history-title">Revision history</h3><button type="button" onClick={loadHistory} disabled={historyState === 'loading' || saving}>{historyState === 'loading' ? 'Loading history…' : history ? 'Refresh history' : 'View revision history'}</button></div>
        {historyState === 'error' && <p className="notice error" role="alert">{historyError}<br /><button type="button" onClick={loadHistory}>Try again</button></p>}
        {historyState === 'ready' && history && (history.items.length ? <div className="history-list">{history.items.map((item) => <article className="history-item" key={item.revision_id}><h4>Revision {item.revision}</h4><p className="muted"><time dateTime={item.created_at}>{item.created_at}</time> · updated {item.updated_at}</p><ThesisContent thesis={item} heading={`history-revision-${item.revision}`} /></article>)}</div> : <p className="empty">No revision history is available for this lot.</p>)}
      </section>
    </>}
  </section>
}

export { allocationPattern, draftFromThesis, validateThesis }
