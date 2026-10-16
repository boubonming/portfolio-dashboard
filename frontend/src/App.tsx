import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, RefObject } from 'react'
import { apiFetch, APIError, jsonResponse } from './api'
import ThesisEditor from './ThesisEditor'
import Reviews from './Reviews'

export type Freshness = {
  status: 'fresh' | 'stale' | 'missing' | 'future' | 'invalid' | string
  market_at?: string
  age?: string
}

export type Holding = {
  lot_id: string
  symbol: string
  name?: string
  description?: string
  quantity: string
  currency: string
  unit_cost?: string
  cost_basis?: string
  latest_price?: string
  latest_value?: string
  reporting_value?: string
  quote_source?: string
  freshness: Freshness
  data_quality: string[]
  broker?: string
  acquisition_date?: string
  missing_broker: boolean
  missing_acquisition_date: boolean
  source_row?: number
}

export type AllocationCoverage = {
  total_lots: number
  valued_lots: number
  missing_dependencies: number
  stale_dependencies: number
  invalid_dependencies: number
}

export type InstrumentAllocation = {
  instrument_id?: string
  symbol: string
  source_currency?: string
  value: string
  percentage: string
}

export type CurrencyAllocation = {
  currency: string
  value: string
  percentage: string
}

export type Allocation = {
  state: 'complete' | 'partial' | 'unavailable' | string
  coverage: AllocationCoverage
  by_instrument?: InstrumentAllocation[]
  by_source_currency?: CurrencyAllocation[]
}

export type PortfolioOverview = {
  portfolio: {
    id: string
    name: string
    default_reporting_currency: string
  }
  reporting_currency: string
  snapshot: {
    state: 'complete' | 'incomplete' | 'no_snapshot' | string
    snapshot_id?: string
    calculation_version?: string
    created_at?: string
    as_of?: string
    complete: boolean
    reporting_total?: string
    missing_dependencies?: string[]
    stale_dependencies?: string[]
    invalid_dependencies?: string[]
    subtotals: Array<{ currency: string; market_value: string; cost_basis?: string; unrealised_pnl?: string }>
  }
  allocation?: Allocation
  holdings: Holding[]
}

export type ActivityItem = {
  id: string
  portfolio_id: string
  action: string
  category: string
  actor_category: string
  entity_type?: string
  entity_id?: string
  occurred_at: string
  summary: string
  details?: string
}

export type ActivityPage = {
  items: ActivityItem[]
  next_cursor?: string
  has_more: boolean
}

type SortKey = 'symbol' | 'quantity' | 'currency' | 'cost_basis' | 'latest_price' | 'reporting_value' | 'freshness'
type SortDirection = 'ascending' | 'descending'

// Compare canonical decimal strings without Number, parseFloat, or lexical
// ordering. It also safely handles the optional values used by incomplete data.
export function compareDecimalStrings(left: string | undefined, right: string | undefined): number {
  if (left === undefined && right === undefined) return 0
  if (left === undefined) return -1
  if (right === undefined) return 1
  const normalize = (value: string) => {
    const trimmed = value.trim()
    const negative = trimmed.startsWith('-')
    const unsigned = trimmed.replace(/^[+-]/, '')
    const [integerPart = '0', fractionPart = ''] = unsigned.split('.')
    const integer = integerPart.replace(/^0+(?=\d)/, '') || '0'
    const fraction = fractionPart.replace(/0+$/, '')
    const zero = integer === '0' && fraction === ''
    return { negative: negative && !zero, integer, fraction }
  }
  const a = normalize(left)
  const b = normalize(right)
  if (a.negative !== b.negative) return a.negative ? -1 : 1
  const sign = a.negative ? -1 : 1
  if (a.integer.length !== b.integer.length) return sign * (a.integer.length < b.integer.length ? -1 : 1)
  if (a.integer !== b.integer) return sign * (a.integer < b.integer ? -1 : 1)
  const width = Math.max(a.fraction.length, b.fraction.length)
  const af = a.fraction.padEnd(width, '0')
  const bf = b.fraction.padEnd(width, '0')
  if (af === bf) return 0
  return sign * (af < bf ? -1 : 1)
}

function displayDate(value?: string) {
  if (!value) return '—'
  return value.replace('T', ' ').replace(/\.\d+Z$/, 'Z')
}

export function safeCSVFilename(contentDisposition: string | null, currency: string) {
  const fallback = `portfolio-export-${currency}.csv`
  if (!contentDisposition) return fallback
  const match = contentDisposition.match(/(?:^|;)\s*filename="?([^";\r\n]+)"?/i)
  const filename = match?.[1]?.trim()
  return filename && filename.length <= 128 && /^[A-Za-z0-9][A-Za-z0-9._-]*\.csv$/i.test(filename) ? filename : fallback
}

export function isCSVContentType(contentType: string | null) {
  if (!contentType) return false
  const [mediaType, ...parameters] = contentType.split(';')
  if (mediaType.trim().toLowerCase() !== 'text/csv') return false
  return parameters.every((parameter) => /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+\s*=\s*(?:[!#$%&'*+\-.^_`|~0-9A-Za-z]+|"(?:[^"\\]|\\.)*")$/.test(parameter.trim()))
}

function validPortfolioID(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= 128 && value === value.trim() && !/[/?#\u0000-\u001f\u007f]/.test(value)
}

function currentPortfolioID(value: PortfolioOverview | null) {
  const candidate = value?.portfolio?.id
  return validPortfolioID(candidate) ? candidate : null
}

function qualityLabel(holding: Holding) {
  const flags = [...holding.data_quality]
  if (holding.freshness.status && holding.freshness.status !== 'fresh' && !flags.includes(holding.freshness.status)) flags.unshift(holding.freshness.status)
  return flags.length ? flags.join(', ') : 'ok'
}

function qualityClass(holding: Holding) {
  if (holding.data_quality.some((flag) => flag.startsWith('invalid') || flag === 'future')) return 'quality-error'
  if (holding.data_quality.length || holding.freshness.status !== 'fresh') return 'quality-warning'
  return 'quality-ok'
}

function CoveragePanel({ allocation, unavailable }: { allocation: Allocation; unavailable: boolean }) {
  const coverage = allocation.coverage
  return (
    <section className="notice warning coverage-panel" aria-labelledby="allocation-coverage-title">
      {unavailable ? (
        <>
          <h2 id="allocation-coverage-title">Allocation unavailable</h2>
          <p>No immutable valuation exists yet, so allocation percentages cannot be calculated.</p>
        </>
      ) : (
        <>
          <h2 id="allocation-coverage-title">Allocation percentages withheld</h2>
          <p>Allocation visuals are unavailable until every holding has a valid reporting value. Partial values are not presented as the whole portfolio.</p>
          <dl className="coverage-grid">
            <div><dt>Valued lots</dt><dd>{coverage.valued_lots} / {coverage.total_lots}</dd></div>
            <div><dt>Missing dependencies</dt><dd>{coverage.missing_dependencies}</dd></div>
            <div><dt>Stale dependencies</dt><dd>{coverage.stale_dependencies}</dd></div>
            <div><dt>Invalid dependencies</dt><dd>{coverage.invalid_dependencies}</dd></div>
          </dl>
        </>
      )}
    </section>
  )
}

function AllocationBars({ title, items, label }: { title: string; items: Array<{ id: string; value: string; percentage: string; label: string }>; label: string }) {
  return (
    <div className="allocation-group">
      <h3>{title}</h3>
      <ol className="allocation-bars" aria-label={label}>
        {items.map((item) => (
          <li key={item.id}>
            <div className="allocation-row">
              <span className="allocation-label">{item.label} allocation</span>
              <span className="allocation-value">{item.value} · {item.percentage}%</span>
            </div>
            <div className="allocation-track" aria-hidden="true"><span style={{ width: item.percentage === '0' ? '0%' : `${item.percentage}%` }} /></div>
          </li>
        ))}
      </ol>
    </div>
  )
}

function AllocationSection({ data }: { data: PortfolioOverview }) {
  const allocation: Allocation = data.allocation || {
    state: data.snapshot.state === 'no_snapshot' ? 'unavailable' : 'partial',
    coverage: { total_lots: data.holdings.length, valued_lots: data.holdings.filter((holding) => holding.reporting_value !== undefined).length, missing_dependencies: 0, stale_dependencies: 0, invalid_dependencies: 0 },
    by_instrument: [],
    by_source_currency: [],
  }
  const snapshotComplete = data.snapshot.complete && data.snapshot.state === 'complete'
  const allocationComplete = snapshotComplete && allocation.state === 'complete' && Array.isArray(allocation.by_instrument) && Array.isArray(allocation.by_source_currency)
  if (!allocationComplete) return <CoveragePanel allocation={allocation} unavailable={data.snapshot.state === 'no_snapshot' || allocation.state === 'unavailable'} />
  const instruments = allocation.by_instrument || []
  const currencies = allocation.by_source_currency || []
  return (
    <section className="allocation-card" aria-labelledby="allocation-title">
      <div className="section-heading">
        <div><span className="eyebrow">Reporting-currency allocation</span><h2 id="allocation-title">Allocation</h2></div>
        <span className="muted">Exact values in {data.reporting_currency} · percentages rounded to 2 decimals</span>
      </div>
      <div className="allocation-visuals">
        <AllocationBars title="By instrument" label="Allocation by instrument" items={instruments.map((item, index) => ({ id: item.instrument_id || `instrument-${index}`, label: item.source_currency ? `${item.symbol} (${item.source_currency})` : item.symbol, value: item.value, percentage: item.percentage }))} />
        <AllocationBars title="By source currency" label="Allocation by original source currency" items={currencies.map((item) => ({ id: `currency-${item.currency}`, label: item.currency, value: item.value, percentage: item.percentage }))} />
      </div>
      <div className="table-scroll allocation-table-scroll" tabIndex={0} role="region" aria-label="Allocation detail tables">
        <table className="allocation-table">
          <caption>Allocation detail, also available without color</caption>
          <thead><tr><th scope="col">Instrument</th><th scope="col">Value ({data.reporting_currency})</th><th scope="col">Percentage</th></tr></thead>
          <tbody>{instruments.map((item, index) => <tr key={`instrument-${item.instrument_id || index}`}><th scope="row">{item.source_currency ? `${item.symbol} (${item.source_currency})` : item.symbol} allocation</th><td className="numeric">{item.value}</td><td className="numeric">{item.percentage}%</td></tr>)}</tbody>
        </table>
        <table className="allocation-table">
          <caption>Source currency allocation detail</caption>
          <thead><tr><th scope="col">Source currency</th><th scope="col">Value ({data.reporting_currency})</th><th scope="col">Percentage</th></tr></thead>
          <tbody>{currencies.map((item) => <tr key={`currency-${item.currency}`}><th scope="row">{item.currency} allocation</th><td className="numeric">{item.value}</td><td className="numeric">{item.percentage}%</td></tr>)}</tbody>
        </table>
      </div>
    </section>
  )
}

function ActivityView({ page, state, error, loadingMore, appendError, announcement, loadMoreRef, statusRef, onLoadMore, onRetry }: { page: ActivityPage | null; state: 'loading' | 'ready' | 'error'; error: string; loadingMore: boolean; appendError: string; announcement: string; loadMoreRef: RefObject<HTMLButtonElement | null>; statusRef: RefObject<HTMLParagraphElement | null>; onLoadMore: (keyboardActivated: boolean) => void; onRetry: () => void }) {
  const keyboardActivationRef = useRef(false)
  return (
    <section className="activity-card" aria-labelledby="activity-title">
      <div className="section-heading"><div><span className="eyebrow">Audit-safe timeline</span><h2 id="activity-title">Activity</h2></div><span className="muted">Newest first</span></div>
      {state === 'loading' && !page && <p role="status" aria-live="polite">Loading activity…</p>}
      {state === 'error' && !page && <p className="notice error" role="alert">{error}<br /><button type="button" onClick={onRetry}>Try again</button></p>}
      {page && page.items.length === 0 && state !== 'loading' && <p className="empty">No activity recorded for this portfolio.</p>}
      {page && page.items.length > 0 && <ol className="activity-list" aria-label="Portfolio activity">
        {page.items.map((item) => <li className="activity-item" tabIndex={-1} data-activity-id={item.id} key={item.id}>
          <div className="activity-heading"><strong>{item.summary}</strong><time dateTime={item.occurred_at}>{displayDate(item.occurred_at)}</time></div>
          <div className="activity-meta"><span>{item.action}</span><span>Actor: {item.actor_category}</span></div>
          {item.details && <p>{item.details}</p>}
        </li>)}
      </ol>}
      <p ref={statusRef} className="visually-hidden" role="status" aria-live="polite" tabIndex={-1}>{announcement}</p>
      {appendError && <p className="notice error" role="alert">{appendError} <button type="button" onClick={onRetry}>Retry loading activity</button></p>}
      {page?.has_more && !appendError && <button ref={loadMoreRef} type="button" onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') keyboardActivationRef.current = true }} onClick={() => { const keyboardActivated = keyboardActivationRef.current; keyboardActivationRef.current = false; onLoadMore(keyboardActivated) }} disabled={loadingMore} aria-busy={loadingMore}>{loadingMore ? 'Loading more activity…' : 'Load more activity'}</button>}
    </section>
  )
}

type AuthSession = { authenticated: boolean; csrf_token?: string; expires_at?: string }

const activityPortfolioID = 'portfolio-default'

function validateActivityPage(value: unknown): ActivityPage {
  if (!value || typeof value !== 'object') throw new Error('Invalid activity pagination response')
  const page = value as ActivityPage & Record<string, unknown>
  const pageKeys = new Set(['items', 'has_more', 'next_cursor'])
  if (Object.keys(page).some((key) => !pageKeys.has(key))) throw new Error('Invalid activity pagination response')
  if (!Array.isArray(page.items) || typeof page.has_more !== 'boolean') throw new Error('Invalid activity pagination response')
  const hasCursor = Object.prototype.hasOwnProperty.call(page, 'next_cursor')
  if (page.has_more) {
    if (!hasCursor || typeof page.next_cursor !== 'string' || page.next_cursor.length < 16 || page.next_cursor.length > 512 || !/^[A-Za-z0-9_-]+$/.test(page.next_cursor)) throw new Error('Invalid activity pagination response')
  } else if (hasCursor) {
    throw new Error('Invalid activity pagination response')
  }
  const canonicalDateTime = (candidate: unknown) => {
    if (typeof candidate !== 'string' || !/^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d\.\d{9}Z$/.test(candidate) || !Number.isFinite(Date.parse(candidate))) return false
    const year = Number(candidate.slice(0, 4)); const month = Number(candidate.slice(5, 7)); const day = Number(candidate.slice(8, 10))
    return day <= new Date(Date.UTC(year, month, 0)).getUTCDate()
  }
  for (const item of page.items) {
    if (!item || typeof item !== 'object') throw new Error('Invalid activity item')
    const candidate = item as ActivityItem & Record<string, unknown>
    const allowed = new Set(['id', 'portfolio_id', 'action', 'category', 'actor_category', 'entity_type', 'entity_id', 'occurred_at', 'summary', 'details'])
    if (Object.keys(candidate).some((key) => !allowed.has(key))) throw new Error('Invalid activity item')
    const bounded = (candidate: unknown, max: number) => typeof candidate === 'string' && candidate.length >= 1 && candidate.length <= max
    if (!bounded(candidate.id, 128) || candidate.portfolio_id !== activityPortfolioID || !bounded(candidate.portfolio_id, 128) || !canonicalDateTime(candidate.occurred_at)) throw new Error('Invalid activity item')
    if (!bounded(candidate.action, 128) || !bounded(candidate.category, 64) || !bounded(candidate.actor_category, 64) || !bounded(candidate.summary, 256)) throw new Error('Invalid activity item')
    if (!bounded(candidate.entity_type, 128) && candidate.entity_type !== undefined) throw new Error('Invalid activity item')
    if (!bounded(candidate.entity_id, 128) && candidate.entity_id !== undefined) throw new Error('Invalid activity item')
    if (!bounded(candidate.details, 2000) && candidate.details !== undefined) throw new Error('Invalid activity item')
  }
  return page
}

function Login({ onLogin, externalError = '' }: { onLogin: (username: string, password: string) => Promise<string | null>; externalError?: string }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [state, setState] = useState<'idle' | 'submitting'>('idle')
  const [error, setError] = useState('')
  const usernameRef = useRef<HTMLInputElement>(null)
  const mountedRef = useRef(true)
  useEffect(() => { usernameRef.current?.focus() }, [])
  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setState('submitting'); setError('')
    try {
      const message = await onLogin(username, password)
      if (!mountedRef.current) return
      if (message) setError(message)
      else setPassword('')
    } catch (reason) {
      if (!mountedRef.current) return
      setError(reason instanceof Error ? reason.message : 'Unable to sign in')
    } finally {
      if (mountedRef.current) { setPassword(''); setState('idle') }
    }
  }
  return <main className="shell login-shell">
    <header className="brand"><span className="eyebrow">Private workspace</span><h1>Portfolio Dashboard</h1><p className="muted">Sign in to view your private portfolio.</p></header>
    <form className="login-card" onSubmit={submit} aria-labelledby="login-title">
      <h2 id="login-title">Sign in</h2>
      <label htmlFor="username">Username</label><input ref={usernameRef} id="username" name="username" autoComplete="username" autoFocus value={username} onChange={(event) => setUsername(event.target.value)} required />
      <label htmlFor="password">Password</label><input id="password" name="password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required />
      {(error || externalError) && <p className="notice error" role="alert">{error || externalError}</p>}
      <button type="submit" disabled={state === 'submitting'}>{state === 'submitting' ? 'Signing in…' : 'Sign in'}</button>
    </form>
  </main>
}

export default function App() {
  const [currency, setCurrency] = useState('MYR')
  const [view, setView] = useState<'overview' | 'activity' | 'reviews'>('overview')
  const [reviewsHoldingId, setReviewsHoldingId] = useState<string | undefined>(undefined)
  const [data, setData] = useState<PortfolioOverview | null>(null)
  const [thesisHolding, setThesisHolding] = useState<Holding | null>(null)
  const [activity, setActivity] = useState<ActivityPage | null>(null)
  const [activityState, setActivityState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [activityError, setActivityError] = useState('')
  const [activityLoadingMore, setActivityLoadingMore] = useState(false)
  const [activityAppendError, setActivityAppendError] = useState('')
  const [activityAnnouncement, setActivityAnnouncement] = useState('')
  const [authState, setAuthState] = useState<'checking' | 'login' | 'authenticated'>('checking')
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [error, setError] = useState('')
  const [exportState, setExportState] = useState<'idle' | 'loading' | 'error'>('idle')
  const [exportError, setExportError] = useState('')
  const [exportAnnouncement, setExportAnnouncement] = useState('')
  const [authError, setAuthError] = useState('')
  const csrfToken = useRef<string | undefined>(undefined)
  const [filter, setFilter] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('symbol')
  const [sortDirection, setSortDirection] = useState<SortDirection>('ascending')
  const authControllerRef = useRef<AbortController | null>(null)
  const loadControllerRef = useRef<AbortController | null>(null)
  const activityControllerRef = useRef<AbortController | null>(null)
  const exportControllerRef = useRef<AbortController | null>(null)
  const activityLoadMoreRef = useRef<HTMLButtonElement>(null)
  const activityStatusRef = useRef<HTMLParagraphElement>(null)
  const thesisReturnRef = useRef<HTMLButtonElement>(null)
  const thesisReturnLotIdRef = useRef<string | null>(null)
  const reviewsReturnRef = useRef<HTMLButtonElement>(null)
  const reviewsReturnLotIdRef = useRef<string | null>(null)
  const filterRef = useRef<HTMLInputElement>(null)
  const activityLoadingMoreRef = useRef(false)
  const activityItemsRef = useRef<ActivityItem[]>([])
  const authGeneration = useRef(0)
  const loadGeneration = useRef(0)
  const activityGeneration = useRef(0)
  const exportGeneration = useRef(0)
  const expiryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearPrivateState = useCallback(() => {
    authGeneration.current += 1
    loadGeneration.current += 1
    activityGeneration.current += 1
    exportGeneration.current += 1
    authControllerRef.current?.abort()
    authControllerRef.current = null
    loadControllerRef.current?.abort()
    loadControllerRef.current = null
    activityControllerRef.current?.abort()
    activityControllerRef.current = null
    exportControllerRef.current?.abort()
    exportControllerRef.current = null
    if (expiryTimerRef.current) { clearTimeout(expiryTimerRef.current); expiryTimerRef.current = null }
    csrfToken.current = undefined
    activityLoadingMoreRef.current = false
    activityItemsRef.current = []
    setData(null); setThesisHolding(null); setActivity(null); setReviewsHoldingId(undefined); setError(''); setExportError(''); setExportAnnouncement(''); setExportState('idle'); setActivityError(''); setActivityAppendError(''); setActivityAnnouncement(''); setActivityLoadingMore(false); setState('loading'); setActivityState('loading')
  }, [])

  const scheduleExpiry = useCallback((expiresAt: string, generation: number, controller: AbortController) => {
    if (controller.signal.aborted || authGeneration.current !== generation) return false
    const expiry = Date.parse(expiresAt)
    if (!Number.isFinite(expiry) || expiry <= Date.now()) return false
    if (controller.signal.aborted || authGeneration.current !== generation) return false
    if (expiryTimerRef.current) clearTimeout(expiryTimerRef.current)
    expiryTimerRef.current = setTimeout(() => {
      if (controller.signal.aborted || authGeneration.current !== generation) return
      clearPrivateState()
      setAuthError('Your session expired. Please sign in again.')
      setAuthState('login')
    }, Math.max(0, expiry - Date.now()))
    return true
  }, [clearPrivateState])

  const load = useCallback(() => {
    if (authState !== 'authenticated') return
    const authGenerationAtStart = authGeneration.current
    const generation = ++loadGeneration.current
    loadControllerRef.current?.abort()
    const controller = new AbortController()
    loadControllerRef.current = controller
    setState('loading')
    setError('')
    const url = `/api/v1/portfolios/${activityPortfolioID}/overview?currency=${encodeURIComponent(currency)}`
    apiFetch(url, { signal: controller.signal })
      .then((response) => jsonResponse<PortfolioOverview>(response))
      .then((overview) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== loadGeneration.current) return
        if (!currentPortfolioID(overview)) throw new Error('Invalid portfolio overview')
        setData(overview)
        setState('ready')
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== loadGeneration.current) return
        if (reason instanceof APIError && reason.status === 401) { clearPrivateState(); setAuthState('login'); return }
        setError(reason instanceof Error ? reason.message : 'Unable to load the portfolio')
        setState('error')
      })
  }, [authState, clearPrivateState, currency])

  const downloadCSV = useCallback(() => {
    if (authState !== 'authenticated' || exportState === 'loading') return
    const portfolioID = currentPortfolioID(data)
    if (state !== 'ready' || !portfolioID) {
      setExportError('Unable to download CSV: no valid portfolio overview is loaded.')
      setExportAnnouncement('CSV download failed. Please try again.')
      setExportState('error')
      return
    }
    const authGenerationAtStart = authGeneration.current
    const generation = ++exportGeneration.current
    exportControllerRef.current?.abort()
    const controller = new AbortController()
    exportControllerRef.current = controller
    setExportState('loading')
    setExportError('')
    setExportAnnouncement('Preparing CSV download…')
    apiFetch(`/api/v1/portfolios/${encodeURIComponent(portfolioID)}/export.csv?currency=${encodeURIComponent(currency)}`, { signal: controller.signal, headers: { Accept: 'text/csv' } })
      .then(async (response) => {
        if (!response.ok) throw new APIError(response.status)
        if (!isCSVContentType(response.headers.get('Content-Type'))) throw new Error('Invalid CSV response MIME type')
        const blob = await response.blob()
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== exportGeneration.current) return
        const objectURL = URL.createObjectURL(blob)
        let link: HTMLAnchorElement | null = null
        try {
          link = document.createElement('a')
          link.href = objectURL
          link.download = safeCSVFilename(response.headers.get('Content-Disposition'), currency)
          link.style.display = 'none'
          document.body.appendChild(link)
          link.click()
        } finally {
          try { link?.remove() } finally { URL.revokeObjectURL(objectURL) }
        }
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== exportGeneration.current) return
        setExportState('idle')
        setExportAnnouncement('CSV download started.')
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== exportGeneration.current) return
        if (reason instanceof APIError && reason.status === 401) { clearPrivateState(); setAuthState('login'); return }
        setExportError(reason instanceof Error ? reason.message : 'Unable to download CSV')
        setExportAnnouncement('CSV download failed. Please try again.')
        setExportState('error')
      })
      .finally(() => {
        if (exportControllerRef.current === controller) exportControllerRef.current = null
      })
  }, [authState, clearPrivateState, currency, data, exportState, state, view])

  useEffect(() => {
    exportGeneration.current += 1
    exportControllerRef.current?.abort()
    exportControllerRef.current = null
    setExportState('idle')
    setExportError('')
    setExportAnnouncement('')
  }, [currency, view])

  useEffect(() => () => {
    exportGeneration.current += 1
    exportControllerRef.current?.abort()
    exportControllerRef.current = null
  }, [])

  const loadActivity = useCallback((cursor: string | undefined, append: boolean, keyboardActivated = false) => {
    if (authState !== 'authenticated' || (append && activityLoadingMoreRef.current)) return
    const hadKeyboardFocus = append && keyboardActivated && document.activeElement === activityLoadMoreRef.current
    const authGenerationAtStart = authGeneration.current
    const generation = ++activityGeneration.current
    activityControllerRef.current?.abort()
    const controller = new AbortController()
    activityControllerRef.current = controller
    if (append) { activityLoadingMoreRef.current = true; setActivityLoadingMore(true); setActivityAppendError('') } else { setActivity(null); setActivityState('loading'); setActivityError(''); setActivityAppendError(''); setActivityAnnouncement('') }
    const query = cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''
    apiFetch(`/api/v1/portfolios/${activityPortfolioID}/activity?limit=20${query}`, { signal: controller.signal })
      .then((response) => jsonResponse<ActivityPage>(response))
      .then((nextPage) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== activityGeneration.current) return
        validateActivityPage(nextPage)
        const previousItems = activityItemsRef.current
        const combined = append ? [...previousItems, ...nextPage.items] : nextPage.items
        const previousIDs = new Set(previousItems.map((item) => item.id))
        const unique = new Map<string, ActivityItem>()
        for (const item of combined) if (!unique.has(item.id)) unique.set(item.id, item)
        const appendedItems = append ? [...unique.values()].filter((item) => !previousIDs.has(item.id)) : []
        const appendedCount = appendedItems.length
        const items = [...unique.values()].sort((left, right) => Date.parse(right.occurred_at) - Date.parse(left.occurred_at) || right.id.localeCompare(left.id))
        activityItemsRef.current = items
        setActivity(() => {
          return { ...nextPage, items }
        })
        activityLoadingMoreRef.current = false
        setActivityLoadingMore(false)
        setActivityAppendError('')
        setActivityState('ready')
        if (append) {
          setActivityAnnouncement(`Loaded ${appendedCount} more activit${appendedCount === 1 ? 'y' : 'ies'}.`)
          window.setTimeout(() => {
            if (!hadKeyboardFocus) return
            if (!nextPage.has_more && appendedItems[0]) {
              Array.from(document.querySelectorAll<HTMLElement>('[data-activity-id]')).find((node) => node.dataset.activityId === appendedItems[0].id)?.focus()
            } else if (!nextPage.has_more) {
              activityStatusRef.current?.focus()
            } else {
              activityLoadMoreRef.current?.focus()
            }
          }, 0)
        }
        if (!append) {
          activityItemsRef.current = items
        }
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== activityGeneration.current) return
        if (reason instanceof APIError && reason.status === 401) { clearPrivateState(); setAuthState('login'); return }
        if (append) { activityLoadingMoreRef.current = false; setActivityLoadingMore(false); setActivityAppendError(reason instanceof Error ? reason.message : 'Unable to load activity') }
        else { setActivityError(reason instanceof Error ? reason.message : 'Unable to load activity'); setActivityState('error') }
      })
  }, [authState, clearPrivateState])

  useEffect(() => {
    if (authState !== 'authenticated' || view !== 'activity') return
    loadActivity(undefined, false)
    return () => {
      activityGeneration.current += 1
      activityControllerRef.current?.abort()
      activityControllerRef.current = null
      activityLoadingMoreRef.current = false
      setActivityLoadingMore(false)
    }
  }, [authState, loadActivity, view])

  useEffect(() => {
    const generation = ++authGeneration.current
    const controller = new AbortController()
    authControllerRef.current = controller
    const current = () => !controller.signal.aborted && generation === authGeneration.current
    apiFetch('/api/v1/auth/session', { signal: controller.signal })
      .then(async (response) => {
        if (!current()) return
        if (response.status === 401) throw new APIError(401)
        const session = await jsonResponse<AuthSession>(response)
        if (!current()) return
        if (session.authenticated !== true || !session.csrf_token || !session.expires_at || !scheduleExpiry(session.expires_at, generation, controller)) throw new APIError(401)
        if (!current()) return
        csrfToken.current = session.csrf_token
        setAuthState('authenticated')
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || generation !== authGeneration.current) return
        if (reason instanceof APIError && reason.status === 401) { clearPrivateState(); setAuthState('login'); return }
        setAuthError(`Unable to load portfolio: ${reason instanceof Error ? reason.message : 'Unable to check your session'}`)
        setAuthState('login')
      })
    return () => {
      controller.abort()
      authControllerRef.current?.abort()
      authControllerRef.current = null
      authGeneration.current += 1
      loadGeneration.current += 1
      activityGeneration.current += 1
      loadControllerRef.current?.abort()
      loadControllerRef.current = null
      activityControllerRef.current?.abort()
      activityControllerRef.current = null
      if (expiryTimerRef.current) { clearTimeout(expiryTimerRef.current); expiryTimerRef.current = null }
    }
  }, [clearPrivateState, scheduleExpiry])

  useEffect(() => {
    if (authState !== 'authenticated') return
    load()
    return () => {
      loadGeneration.current += 1
      loadControllerRef.current?.abort()
      loadControllerRef.current = null
    }
  }, [authState, load])

  const signIn = useCallback(async (username: string, password: string) => {
    setAuthError('')
    clearPrivateState()
    const generation = authGeneration.current
    const controller = new AbortController()
    authControllerRef.current = controller
    const current = () => !controller.signal.aborted && generation === authGeneration.current
    try {
      const response = await apiFetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }), signal: controller.signal })
      if (!response.ok) return 'Unable to sign in right now.'
      const session = await response.json() as AuthSession
      if (!current()) return null
      if (session.authenticated !== true || !session.csrf_token || !session.expires_at || !scheduleExpiry(session.expires_at, generation, controller)) return 'Unable to sign in right now.'
      csrfToken.current = session.csrf_token
      setAuthState('authenticated')
      return null
    } catch (reason) {
      if (!current()) return null
      return reason instanceof APIError && reason.status === 401 ? 'Sign-in failed. Check your username and password.' : 'Unable to sign in right now.'
    } finally {
      if (authControllerRef.current === controller) authControllerRef.current = null
    }
  }, [clearPrivateState, scheduleExpiry])

  const logout = useCallback(async () => {
    const token = csrfToken.current
    clearPrivateState()
    const generation = authGeneration.current
    setAuthState('login')
    const controller = new AbortController()
    authControllerRef.current = controller
    const current = () => !controller.signal.aborted && generation === authGeneration.current
    try {
      await jsonResponse(await apiFetch('/api/v1/auth/logout', { method: 'POST', csrfToken: token, signal: controller.signal }))
      if (current()) setAuthError('')
    } catch (reason) {
      if (!current()) return
      setAuthError(reason instanceof APIError && reason.status === 401
        ? 'Sign-out was not confirmed by the server. Please sign in again if you want to retry.'
        : 'Sign-out was not confirmed by the server. Close this window or try again.')
    } finally {
      if (authControllerRef.current === controller) authControllerRef.current = null
    }
  }, [clearPrivateState])

  const handleThesisUnauthorized = useCallback(() => {
    clearPrivateState()
    setAuthState('login')
  }, [clearPrivateState])

  const closeThesis = useCallback(() => {
    setThesisHolding(null)
    window.setTimeout(() => {
      const target = Array.from(document.querySelectorAll<HTMLButtonElement>('.thesis-open-button'))
        .find((button) => button.dataset.thesisLotId === thesisReturnLotIdRef.current)
      const focusTarget = target || filterRef.current
      focusTarget?.focus()
    }, 0)
  }, [])

  const closeReviews = useCallback(() => {
    setReviewsHoldingId(undefined)
    setView('overview')
    window.setTimeout(() => {
      const lotId = reviewsReturnLotIdRef.current
      const target = lotId ? Array.from(document.querySelectorAll<HTMLButtonElement>('[data-reviews-lot-id]')).find((button) => button.dataset.reviewsLotId === lotId) : null
      ;(target || reviewsReturnRef.current)?.focus()
    }, 0)
  }, [])

  const visibleHoldings = useMemo(() => {
    if (!data) return []
    const needle = filter.trim().toLowerCase()
    const filtered = data.holdings.filter((holding) => {
      if (!needle) return true
      return [holding.symbol, holding.name, holding.description, holding.currency, holding.broker, qualityLabel(holding)].some((value) => value?.toLowerCase().includes(needle))
    })
    return [...filtered].sort((left, right) => {
      let comparison = 0
      if (sortKey === 'symbol' || sortKey === 'currency') comparison = (left[sortKey] || '').localeCompare(right[sortKey] || '')
      else if (sortKey === 'freshness') comparison = (left.freshness.status || '').localeCompare(right.freshness.status || '')
      else comparison = compareDecimalStrings(left[sortKey], right[sortKey])
      return sortDirection === 'ascending' ? comparison : -comparison
    })
  }, [data, filter, sortDirection, sortKey])

  const selectSort = (next: SortKey) => {
    if (sortKey === next) setSortDirection(sortDirection === 'ascending' ? 'descending' : 'ascending')
    else {
      setSortKey(next)
      setSortDirection('ascending')
    }
  }

  const sortButton = (label: string, key: SortKey) => (
    <button className="sort-button" type="button" onClick={() => selectSort(key)} aria-label={`Sort by ${label}`}>
      {label} {sortKey === key ? (sortDirection === 'ascending' ? '↑' : '↓') : '↕'}
    </button>
  )

  const sortHeader = (label: string, key: SortKey) => (
    <th scope="col" aria-sort={sortKey === key ? sortDirection : 'none'}>{sortButton(label, key)}</th>
  )

  if (authState === 'login') return <Login onLogin={signIn} externalError={authError} />
  if (authState === 'checking') return <main className="shell" aria-busy="true"><section className="notice" role="status" aria-live="polite"><h1>Checking your session…</h1><p>Loading portfolio securely.</p></section></main>

  return (
    <main className="shell">
      <header className="brand">
        <span className="eyebrow">Private workspace</span>
        <h1>Portfolio Dashboard</h1>
        <p className="muted">Read-only holdings and immutable valuation snapshots.</p>
        <button type="button" onClick={logout}>Sign out</button>
      </header>
      <nav className="view-nav" aria-label="Portfolio views">
        <button type="button" aria-current={view === 'overview' ? 'page' : undefined} onClick={() => setView('overview')}>Overview</button>
        <button type="button" aria-current={view === 'activity' ? 'page' : undefined} onClick={() => setView('activity')}>Activity</button>
        <button type="button" aria-current={view === 'reviews' ? 'page' : undefined} onClick={() => { setReviewsHoldingId(undefined); setView('reviews') }}>Reviews</button>
      </nav>

      {thesisHolding ? <ThesisEditor key={thesisHolding.lot_id} portfolioId={data?.portfolio.id || activityPortfolioID} holding={thesisHolding} csrfToken={csrfToken.current} onClose={closeThesis} onUnauthorized={handleThesisUnauthorized} returnFocusRef={thesisReturnRef} /> : <>
      {view === 'overview' && <>
      <section className="toolbar" aria-label="Portfolio controls">
        <label htmlFor="currency">Reporting currency</label>
        <select id="currency" value={currency} onChange={(event) => setCurrency(event.target.value)}>
          <option value="MYR">MYR</option>
          <option value="USD">USD</option>
          <option value="SGD">SGD</option>
          <option value="JPY">JPY</option>
        </select>
        <label htmlFor="filter">Filter holdings</label>
        <input ref={filterRef} id="filter" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Symbol, currency or flag" />
        <button type="button" onClick={downloadCSV} disabled={exportState === 'loading'} aria-busy={exportState === 'loading'}>{exportState === 'loading' ? 'Downloading CSV…' : 'Download CSV'}</button>
        {exportError && <span className="notice error" role="alert">{exportError} <button type="button" onClick={downloadCSV}>Retry download</button></span>}
        <span className="visually-hidden" role="status" aria-live="polite">{exportAnnouncement}</span>
      </section>

      {state === 'loading' && <section className="notice" role="status"><h2>Loading portfolio</h2><p>Reading the latest immutable snapshot…</p></section>}
      {authError && <section className="notice error" role="alert"><h2>Session error</h2><p>{authError}</p></section>}
      {state === 'error' && <section className="notice error" role="alert"><h2>Unable to load portfolio</h2><p>{error}</p><button type="button" onClick={load}>Try again</button></section>}

      {state === 'ready' && data && (
        <>
          <section className="overview" aria-labelledby="overview-title">
            <div>
              <span className="eyebrow">Overview</span>
              <h2 id="overview-title">{data.portfolio.name}</h2>
              <p className="muted">Reporting in {data.reporting_currency}</p>
            </div>
            <div className={`state-pill state-${data.snapshot.state}`}>
              {data.snapshot.state === 'complete' ? 'Complete valuation' : data.snapshot.state === 'incomplete' ? 'Incomplete valuation' : 'No snapshot'}
            </div>
            {data.snapshot.complete && data.snapshot.reporting_total && (
              <div className="total"><span className="muted">Reporting total</span><strong>{data.snapshot.reporting_total} {data.reporting_currency}</strong></div>
            )}
            <dl className="snapshot-meta">
              <div><dt>Snapshot as-of</dt><dd>{displayDate(data.snapshot.as_of)}</dd></div>
              <div><dt>Version</dt><dd>{data.snapshot.calculation_version || 'Not available'}</dd></div>
            </dl>
          </section>

          {data.snapshot.state === 'no_snapshot' && <section className="notice"><h2>No immutable snapshot yet</h2><p>Holdings are shown from the approved import, but prices and reporting values are withheld until a snapshot is created.</p></section>}
          {data.snapshot.state === 'incomplete' && <section className="notice warning"><h2>Valuation is incomplete</h2><p>Reporting total is intentionally withheld. Missing, stale or invalid market dependencies must be resolved before this view can be complete.</p></section>}

          <AllocationSection data={data} />

          <section className="table-card" aria-labelledby="holdings-title">
            <div className="section-heading"><div><span className="eyebrow">Source-traceable lots</span><h2 id="holdings-title">Holdings</h2></div><span className="muted">{visibleHoldings.length} shown · {new Set(data.holdings.map((holding) => holding.symbol)).size} symbols</span></div>
            <div className="table-scroll">
              <table>
                <caption className="visually-hidden">Holdings</caption>
                <thead><tr>
                  {sortHeader('Instrument', 'symbol')}
                  {sortHeader('Quantity', 'quantity')}
                  {sortHeader('Currency', 'currency')}
                  <th scope="col">Source unit cost</th>
                  {sortHeader('Cost basis', 'cost_basis')}
                  {sortHeader('Latest price', 'latest_price')}
                  {sortHeader('Reporting value', 'reporting_value')}
                  {sortHeader('Freshness', 'freshness')}
                  <th scope="col">Broker / date</th>
                </tr></thead>
                <tbody>
                  {visibleHoldings.map((holding) => <tr key={holding.lot_id}>
                    <th scope="row"><strong>{holding.symbol}</strong><span className="subline">{holding.description && holding.description !== holding.symbol ? holding.description : holding.name && holding.name !== holding.symbol ? holding.name : 'Instrument description unavailable'} · lot {holding.lot_id.slice(-8)}</span></th>
                    <td className="numeric">{holding.quantity}</td>
                    <td>{holding.currency}</td>
                    <td className="numeric">{holding.unit_cost || '—'}</td>
                    <td className="numeric">{holding.cost_basis || '—'}</td>
                    <td className="numeric">{holding.latest_price || '—'}<span className="subline">{holding.latest_value || 'value unavailable'}</span></td>
                    <td className="numeric">{data.snapshot.complete ? (holding.reporting_value || '—') : 'Withheld'}</td>
                    <td><span className={`flag ${qualityClass(holding)}`}>{qualityLabel(holding)}</span>{holding.quote_source && <span className="subline">{holding.quote_source}</span>}</td>
                    <td>{holding.broker || 'Missing'}<span className="subline">{holding.acquisition_date || 'Date missing'}</span><button className="thesis-open-button" data-thesis-lot-id={holding.lot_id} type="button" onClick={(event) => { thesisReturnRef.current = event.currentTarget; thesisReturnLotIdRef.current = holding.lot_id; setThesisHolding(holding) }} aria-label={`Open thesis for ${holding.symbol} lot ${holding.lot_id}`}>Open thesis</button><button className="thesis-open-button" data-reviews-lot-id={holding.lot_id} ref={reviewsReturnRef} type="button" onClick={(event) => { reviewsReturnRef.current = event.currentTarget; reviewsReturnLotIdRef.current = holding.lot_id; setReviewsHoldingId(holding.lot_id); setView('reviews') }} aria-label={`View reviews for ${holding.symbol} lot ${holding.lot_id}`}>View reviews</button></td>
                  </tr>)}
                </tbody>
              </table>
              {!visibleHoldings.length && <p className="empty">No holdings match this filter.</p>}
            </div>
          </section>
        </>
      )}
      </>}
      {view === 'activity' && <ActivityView page={activity} state={activityState} error={activityError} loadingMore={activityLoadingMore} appendError={activityAppendError} announcement={activityAnnouncement} loadMoreRef={activityLoadMoreRef} statusRef={activityStatusRef} onLoadMore={(keyboardActivated) => loadActivity(activity?.next_cursor, true, keyboardActivated)} onRetry={() => activityAppendError ? loadActivity(activity?.next_cursor, true) : loadActivity(undefined, false)} />}
      {view === 'reviews' && data && <Reviews portfolioId={data.portfolio.id} holdings={data.holdings} snapshot={data.snapshot} csrfToken={csrfToken.current} holdingId={reviewsHoldingId} onUnauthorized={handleThesisUnauthorized} onBackToOverview={closeReviews} />}
      </>}
    </main>
  )
}
