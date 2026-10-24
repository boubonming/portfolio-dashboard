import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, RefObject } from 'react'
import {
  AppBar,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  FormControl,
  InputLabel,
  Paper,
  Select,
  Stack,
  Tab,
  Tabs,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TableSortLabel,
  TextField,
  Toolbar,
  Typography,
} from '@mui/material'
import {
  Download,
  Refresh,
  TrendingDown,
  TrendingFlat,
  TrendingUp,
} from '@mui/icons-material'
import { apiFetch, APIError, getMarketDataSetup, jsonResponse, refreshMarketData, type MarketDataSetup } from './api'
import ThesisEditor from './ThesisEditor'
import Reviews, { type FeedbackAttemptStore } from './Reviews'
import HoldingMutations, { validatePortfolioETag } from './HoldingMutations'
import MarketDataPanel from './MarketDataPanel'

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
  unrealised_pnl?: string
  reporting_value?: string
  quote_source?: string
  freshness: Freshness
  data_quality: string[]
  broker?: string
  acquisition_date?: string
  missing_broker: boolean
  missing_acquisition_date: boolean
}

export type AllocationCoverage = {
  total_lots: number
  valued_lots: number
  missing_dependencies: number
  stale_dependencies: number
  invalid_dependencies: number
  future_dependencies?: number
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
    future_dependencies?: string[]
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

type SortKey = 'symbol' | 'quantity' | 'currency' | 'cost_basis' | 'latest_price' | 'latest_value' | 'unrealised_pnl' | 'reporting_value' | 'freshness'
type SortDirection = 'ascending' | 'descending'

function incrementDecimalDigits(value: string) {
  const digits = value.split('')
  let carry = 1
  for (let index = digits.length - 1; index >= 0 && carry; index -= 1) {
    if (digits[index] === '9') digits[index] = '0'
    else {
      digits[index] = String.fromCharCode(digits[index].charCodeAt(0) + 1)
      carry = 0
    }
  }
  return carry ? `1${digits.join('')}` : digits.join('')
}

// Format canonical decimal strings without floating-point conversion. Values
// at the third fractional place are rounded away from zero on a half tie.
export function formatDecimalForDisplay(value: string | undefined): string {
  if (value === undefined || value.trim() === '') return '—'
  const trimmed = value.trim()
  const match = trimmed.match(/^([+-]?)(\d+)(?:\.(\d+))?$/)
  if (!match) return trimmed
  const negative = match[1] === '-'
  let integer = match[2].replace(/^0+(?=\d)/, '') || '0'
  const fraction = match[3] || ''
  let cents = fraction.slice(0, 2).padEnd(2, '0')
  if (fraction.length > 2 && fraction[2] >= '5') {
    const rounded = incrementDecimalDigits(`${integer}${cents}`)
    integer = rounded.slice(0, -2) || '0'
    cents = rounded.slice(-2)
  }
  cents = cents.replace(/0+$/, '')
  const zero = integer === '0' && cents === ''
  return `${negative && !zero ? '-' : ''}${integer}${cents ? `.${cents}` : ''}`
}

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

type PnLStatus = 'gain' | 'loss' | 'break-even' | 'unavailable'

function newRefreshRequestKey() {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `refresh-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

export function unrealisedPnLStatus(holding: Holding): PnLStatus {
  const value = holding.unrealised_pnl
  if (value === undefined || !/^-?(?:0|[1-9]\d*)(?:\.\d+)?$/.test(value) || holding.freshness.status !== 'fresh') return 'unavailable'
  const comparison = compareDecimalStrings(value, '0')
  return comparison > 0 ? 'gain' : comparison < 0 ? 'loss' : 'break-even'
}

function pnlStatusLabel(status: PnLStatus) {
  return status === 'gain' ? 'Gain' : status === 'loss' ? 'Loss' : status === 'break-even' ? 'Break-even' : 'Unavailable'
}

function directionGlyph(status: Exclude<PnLStatus, 'unavailable'>) {
  return status === 'gain' ? '↑' : status === 'loss' ? '↓' : '→'
}

function directionMarker(status: Exclude<PnLStatus, 'unavailable'>) {
  const Icon = status === 'gain' ? TrendingUp : status === 'loss' ? TrendingDown : TrendingFlat
  return <><span className="direction-icon" aria-hidden="true"><Icon fontSize="inherit" /><span className="direction-glyph">{directionGlyph(status)}</span></span><span className="visually-hidden">{pnlStatusLabel(status)}</span></>
}

function unrealisedPnLCell(holding: Holding) {
  const status = unrealisedPnLStatus(holding)
  if (status === 'unavailable') return <Box component="span" className="pnl-unavailable" sx={{ color: 'text.secondary' }}>Unavailable</Box>
  return <Box component="span" className={`pnl-${status}`} sx={{ bgcolor: status === 'gain' ? 'rgba(22,77,67,.65)' : status === 'loss' ? 'rgba(101,44,53,.8)' : 'rgba(61,62,70,.8)', border: 1, borderColor: status === 'gain' ? 'primary.main' : status === 'loss' ? 'error.main' : 'text.secondary', borderRadius: 1, display: 'inline-block', px: .5, py: .25 }}><span className="directional-amount">{directionMarker(status)}<span className="pnl-amount">{formatDecimalForDisplay(holding.unrealised_pnl)} {holding.currency}</span></span></Box>
}

function latestPriceCell(holding: Holding) {
  const status = unrealisedPnLStatus(holding)
  const latestPrice = holding.latest_price?.trim()
  if (status === 'unavailable' || !latestPrice) return <span>{formatDecimalForDisplay(latestPrice)}</span>
  return <Box component="span" className={`pnl-${status}`} sx={{ color: status === 'gain' ? 'success.light' : status === 'loss' ? 'error.light' : 'text.primary' }}><span className="directional-amount">{directionMarker(status)}<span className="pnl-amount">{formatDecimalForDisplay(latestPrice)}</span></span></Box>
}

function latestMarketValueCell(holding: Holding) {
  const latestValue = formatDecimalForDisplay(holding.latest_value)
  return <span>{latestValue === '—' ? latestValue : `${latestValue} ${holding.currency}`}</span>
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
            <div><dt>Future dependencies</dt><dd>{coverage.future_dependencies || 0}</dd></div>
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
  return <Box component="main" className="shell login-shell" sx={{ maxWidth: 576, minHeight: '100vh', py: { xs: 3, sm: 8 } }}>
    <Box component="header" sx={{ mb: 3 }}><Typography component="span" className="eyebrow">Private workspace</Typography><Typography component="h1" variant="h3" sx={{ mt: .5 }}>Portfolio Dashboard</Typography><Typography color="text.secondary">Sign in to view your private portfolio.</Typography></Box>
    <Card component="form" className="login-card" onSubmit={submit} aria-labelledby="login-title" variant="outlined">
      <CardContent sx={{ display: 'grid', gap: 1.25, p: { xs: 2.5, sm: 3 } }}>
        <Typography component="h2" id="login-title" variant="h5">Sign in</Typography>
        <TextField inputRef={usernameRef} id="username" name="username" label="Username" slotProps={{ htmlInput: { 'aria-label': 'Username' } }} autoComplete="username" autoFocus value={username} onChange={(event) => setUsername(event.target.value)} required fullWidth />
        <TextField id="password" name="password" type="password" label="Password" slotProps={{ htmlInput: { 'aria-label': 'Password' } }} autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required fullWidth />
        {(error || externalError) && <Box className="notice error" role="alert" sx={{ mt: 1 }}>{error || externalError}</Box>}
        <Button type="submit" variant="contained" disabled={state === 'submitting'} sx={{ mt: 1 }}>{state === 'submitting' ? 'Signing in…' : 'Sign in'}</Button>
      </CardContent>
    </Card>
  </Box>
}

export default function App() {
  const [currency, setCurrency] = useState('MYR')
  const [view, setView] = useState<'overview' | 'holdings' | 'market' | 'activity' | 'reviews'>('holdings')
  const [reviewsHoldingId, setReviewsHoldingId] = useState<string | undefined>(undefined)
  const [data, setData] = useState<PortfolioOverview | null>(null)
  const [overviewETag, setOverviewETag] = useState<string | undefined>(undefined)
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
  const [refreshState, setRefreshState] = useState<'idle' | 'loading' | 'complete' | 'incomplete' | 'error'>('idle')
  const [refreshError, setRefreshError] = useState('')
  const [refreshAnnouncement, setRefreshAnnouncement] = useState('')
  const [authError, setAuthError] = useState('')
  const csrfToken = useRef<string | undefined>(undefined)
  const [filter, setFilter] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('symbol')
  const [sortDirection, setSortDirection] = useState<SortDirection>('ascending')
  const authControllerRef = useRef<AbortController | null>(null)
  const loadControllerRef = useRef<AbortController | null>(null)
  const activityControllerRef = useRef<AbortController | null>(null)
  const exportControllerRef = useRef<AbortController | null>(null)
  const refreshControllerRef = useRef<AbortController | null>(null)
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
  const refreshGeneration = useRef(0)
  const refreshingRef = useRef(false)
  const feedbackAttemptsRef = useRef<FeedbackAttemptStore>(new Map())
  const expiryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const mutationSessionGeneration = authGeneration.current

  const clearPrivateState = useCallback(() => {
    authGeneration.current += 1
    loadGeneration.current += 1
    activityGeneration.current += 1
    exportGeneration.current += 1
    refreshGeneration.current += 1
    authControllerRef.current?.abort()
    authControllerRef.current = null
    loadControllerRef.current?.abort()
    loadControllerRef.current = null
    activityControllerRef.current?.abort()
    activityControllerRef.current = null
    exportControllerRef.current?.abort()
    exportControllerRef.current = null
    refreshControllerRef.current?.abort()
    refreshControllerRef.current = null
    refreshingRef.current = false
    if (expiryTimerRef.current) { clearTimeout(expiryTimerRef.current); expiryTimerRef.current = null }
    csrfToken.current = undefined
    feedbackAttemptsRef.current.clear()
    activityLoadingMoreRef.current = false
    activityItemsRef.current = []
    setData(null); setOverviewETag(undefined); setThesisHolding(null); setActivity(null); setReviewsHoldingId(undefined); setError(''); setExportError(''); setExportAnnouncement(''); setExportState('idle'); setRefreshError(''); setRefreshAnnouncement(''); setRefreshState('idle'); setActivityError(''); setActivityAppendError(''); setActivityAnnouncement(''); setActivityLoadingMore(false); setState('loading'); setActivityState('loading')
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

  const load = useCallback((rethrow = false): Promise<string | void> => {
    if (authState !== 'authenticated') return Promise.resolve(undefined)
    const authGenerationAtStart = authGeneration.current
    const generation = ++loadGeneration.current
    loadControllerRef.current?.abort()
    const controller = new AbortController()
    loadControllerRef.current = controller
    setState('loading')
    setError('')
    const url = `/api/v1/portfolios/${activityPortfolioID}/overview?currency=${encodeURIComponent(currency)}`
    return apiFetch(url, { signal: controller.signal })
      .then(async (response) => ({ overview: await jsonResponse<PortfolioOverview>(response), etag: response.headers?.get?.('ETag') || undefined }))
      .then(({ overview, etag }) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== loadGeneration.current) return
        if (!currentPortfolioID(overview)) throw new Error('Invalid portfolio overview')
        setData(overview)
        setOverviewETag(validatePortfolioETag(etag, overview.portfolio.id) === null ? undefined : etag)
        setState('ready')
        return etag
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== loadGeneration.current) return
        if (reason instanceof APIError && reason.status === 401) { clearPrivateState(); setAuthState('login'); if (rethrow) throw reason; return }
        setError(reason instanceof Error ? reason.message : 'Unable to load the portfolio')
        setState('error')
        if (rethrow) throw reason
      })
  }, [authState, clearPrivateState, currency])

  const refreshHoldingOverview = useCallback((): Promise<string | void> => {
    if (authState !== 'authenticated' || authGeneration.current !== mutationSessionGeneration) return Promise.resolve(undefined)
    return load(true).then((etag) => authGeneration.current === mutationSessionGeneration && authState === 'authenticated' ? etag : undefined)
  }, [authState, load, mutationSessionGeneration])

  const refreshPrices = useCallback(async () => {
    if (authState !== 'authenticated' || refreshingRef.current) return
    const portfolioID = currentPortfolioID(data)
    if (!portfolioID) {
      setRefreshState('error')
      setRefreshError('Unable to refresh prices: no valid portfolio overview is loaded.')
      setRefreshAnnouncement('Price refresh failed.')
      return
    }
    const authGenerationAtStart = authGeneration.current
    const generation = ++refreshGeneration.current
    refreshControllerRef.current?.abort()
    const controller = new AbortController()
    refreshControllerRef.current = controller
    refreshingRef.current = true
    setRefreshState('loading')
    setRefreshError('')
    setRefreshAnnouncement('Refreshing prices and creating an immutable snapshot…')
    try {
      const setup: MarketDataSetup = await getMarketDataSetup(portfolioID, controller.signal)
      if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== refreshGeneration.current || currentPortfolioID(data) !== portfolioID || currency !== data?.reporting_currency) return
      if (setup.portfolio_id !== portfolioID) throw new APIError(200, 'Invalid market-data setup response: stale portfolio scope.')
      const result = await refreshMarketData(portfolioID, currency, newRefreshRequestKey(), csrfToken.current, controller.signal, setup)
      if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== refreshGeneration.current || currentPortfolioID(data) !== portfolioID || currency !== data?.reporting_currency) return
      if (result.refresh.portfolio_id !== portfolioID || result.snapshot.portfolio_id !== portfolioID || result.snapshot.reporting_currency !== currency.toUpperCase()) throw new APIError(200, 'Invalid market refresh response: stale portfolio scope.')
      // Reload for incomplete responses too: source-currency lot valuations can
      // be exact even when reporting-currency totals remain withheld.
      await load(true)
      if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== refreshGeneration.current || currentPortfolioID(data) !== portfolioID || currency !== data?.reporting_currency) return
      setRefreshState(result.complete ? 'complete' : 'incomplete')
      setRefreshAnnouncement(result.complete ? 'Prices refreshed. Complete valuation loaded.' : 'Prices refreshed. Valuation is incomplete; usable per-lot values were loaded and totals remain withheld.')
      if (result.missing_instruments.length) setRefreshError(`Missing exact mappings: ${result.missing_instruments.join(', ')}`)
    } catch (reason) {
      if (controller.signal.aborted || authGenerationAtStart !== authGeneration.current || generation !== refreshGeneration.current) return
      if (reason instanceof APIError && reason.status === 401) { clearPrivateState(); setAuthState('login'); return }
      setRefreshState('error')
      setRefreshError(reason instanceof Error ? reason.message : 'Unable to refresh prices')
      setRefreshAnnouncement('Price refresh failed.')
    } finally {
      if (generation === refreshGeneration.current) { refreshingRef.current = false; setRefreshState((current) => current === 'loading' ? 'error' : current) }
      if (refreshControllerRef.current === controller) refreshControllerRef.current = null
    }
  }, [authState, clearPrivateState, currency, data, load])

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

  useEffect(() => {
    if (refreshingRef.current) {
      refreshGeneration.current += 1
      refreshControllerRef.current?.abort()
      refreshControllerRef.current = null
      refreshingRef.current = false
    }
    setRefreshState('idle')
    setRefreshError('')
    setRefreshAnnouncement('')
  }, [currency, view])

  useEffect(() => () => {
    exportGeneration.current += 1
    exportControllerRef.current?.abort()
    exportControllerRef.current = null
    refreshGeneration.current += 1
    refreshControllerRef.current?.abort()
    refreshControllerRef.current = null
    refreshingRef.current = false
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
    setView('holdings')
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
      else {
        const leftValue = sortKey === 'unrealised_pnl' && data ? (unrealisedPnLStatus(left) === 'unavailable' ? undefined : left.unrealised_pnl) : left[sortKey]
        const rightValue = sortKey === 'unrealised_pnl' && data ? (unrealisedPnLStatus(right) === 'unavailable' ? undefined : right.unrealised_pnl) : right[sortKey]
        comparison = compareDecimalStrings(leftValue, rightValue)
      }
      if (comparison === 0) comparison = left.lot_id.localeCompare(right.lot_id)
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
    <TableSortLabel active={sortKey === key} direction={sortDirection === 'ascending' ? 'asc' : 'desc'} onClick={() => selectSort(key)} aria-label={`Sort by ${label}`}>
      {label}
    </TableSortLabel>
  )

  const sortHeader = (label: string, key: SortKey) => (
    <TableCell component="th" scope="col" sortDirection={sortKey === key ? (sortDirection === 'ascending' ? 'asc' : 'desc') : false} aria-sort={sortKey === key ? sortDirection : 'none'}>{sortButton(label, key)}</TableCell>
  )

  if (authState === 'login') return <Login onLogin={signIn} externalError={authError} />
  if (authState === 'checking') return <Box component="main" className="shell" aria-busy="true"><Paper className="notice" role="status" aria-live="polite" sx={{ mt: 3, p: 3 }}><Typography component="h1" variant="h4">Checking your session…</Typography><Typography>Loading portfolio securely.</Typography></Paper></Box>

  const navTabs: Array<{ value: typeof view; label: string; ariaLabel?: string }> = [
    { value: 'overview', label: 'Overview' },
    { value: 'holdings', label: 'Holdings' },
    { value: 'activity', label: 'Activity' },
    { value: 'reviews', label: 'Review', ariaLabel: 'Reviews' },
    { value: 'market', label: 'Market Data', ariaLabel: 'Market data' },
  ]

  return (
    <Box component="main" className="shell" sx={{ maxWidth: 1800, mx: 'auto', px: { xs: 1.5, sm: 3, lg: 4 }, pb: 6, width: '100%' }}>
      <AppBar component="header" className="app-header" position="static" color="transparent">
        <Toolbar variant="dense" disableGutters sx={{ gap: { xs: 1, sm: 2 }, flexWrap: { xs: 'wrap', md: 'nowrap' }, py: .5 }}>
          <Box component="a" className="app-brand" href="#portfolio-content" aria-label="Portfolio Dashboard home" sx={{ color: 'text.primary', fontWeight: 800, textDecoration: 'none', whiteSpace: 'nowrap', mr: { md: 1 } }}>Portfolio Dashboard</Box>
          <Box component="nav" className="view-nav" aria-label="Portfolio views" sx={{ flex: 1, minWidth: 0, order: { xs: 3, md: 0 }, width: { xs: '100%', md: 'auto' }, overflowX: 'auto' }}>
            <Tabs value={view} onChange={(_event, nextView) => { if (nextView === 'reviews') setReviewsHoldingId(undefined); setView(nextView) }} variant="scrollable" scrollButtons="auto" allowScrollButtonsMobile aria-label="Portfolio views" sx={{ minHeight: 40, '& .MuiTab-root': { minHeight: 40, minWidth: { xs: 92, sm: 108 }, px: { xs: 1.25, sm: 2 } } }}>
              {navTabs.map((tab) => <Tab key={tab.value} value={tab.value} label={tab.label} aria-label={tab.ariaLabel} aria-current={view === tab.value ? 'page' : undefined} />)}
            </Tabs>
            <Box sx={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)', clipPath: 'inset(50%)' }} aria-hidden={false}>{navTabs.map((tab) => <button key={`compat-${tab.value}`} className="visually-hidden nav-compat-button" type="button" tabIndex={-1} aria-current={view === tab.value ? 'page' : undefined} aria-label={tab.ariaLabel || tab.label} onClick={() => { if (tab.value === 'reviews') setReviewsHoldingId(undefined); setView(tab.value) }} />)}</Box>
          </Box>
          <Button className="sign-out" type="button" onClick={logout} color="inherit" variant="outlined" size="small" sx={{ ml: 'auto', flexShrink: 0, whiteSpace: 'nowrap' }}>Sign out</Button>
        </Toolbar>
      </AppBar>

      {thesisHolding ? <ThesisEditor key={thesisHolding.lot_id} portfolioId={data?.portfolio.id || activityPortfolioID} holding={thesisHolding} csrfToken={csrfToken.current} onClose={closeThesis} onUnauthorized={handleThesisUnauthorized} returnFocusRef={thesisReturnRef} /> : <>
      {(view === 'overview' || view === 'holdings') && <>
      {view === 'holdings' && <Paper component="section" className="toolbar" aria-label="Portfolio controls" variant="outlined" sx={{ mt: 3, p: { xs: 1.5, sm: 2 }, borderColor: 'divider' }}>
        <Stack direction={{ xs: 'column', md: 'row' }} spacing={1.5} useFlexGap sx={{ flexWrap: 'wrap', alignItems: { xs: 'stretch', md: 'center' } }}>
          <FormControl size="small" sx={{ minWidth: 170 }}><InputLabel id="currency-label">Reporting currency</InputLabel><Select native labelId="currency-label" id="currency" inputProps={{ 'aria-label': 'Reporting currency' }} value={currency} label="Reporting currency" onChange={(event) => setCurrency(event.target.value)}><option value="MYR">MYR</option><option value="USD">USD</option><option value="SGD">SGD</option><option value="JPY">JPY</option></Select></FormControl>
          <TextField inputRef={filterRef} id="filter" label="Filter holdings" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Symbol, currency or flag" size="small" sx={{ minWidth: { md: 250 }, flex: 1 }} />
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}><Button type="button" onClick={downloadCSV} disabled={exportState === 'loading'} aria-busy={exportState === 'loading'} startIcon={<Download aria-hidden="true" />}>{exportState === 'loading' ? 'Downloading CSV…' : 'Download CSV'}</Button><Button type="button" onClick={refreshPrices} disabled={refreshState === 'loading'} aria-busy={refreshState === 'loading'} startIcon={<Refresh aria-hidden="true" />}>{refreshState === 'loading' ? 'Refreshing prices…' : 'Refresh prices'}</Button></Stack>
          {exportError && <Box className="notice error" role="alert">{exportError} <Button type="button" onClick={downloadCSV} size="small">Retry download</Button></Box>}
          {refreshAnnouncement && <Box component="span" className={`refresh-inline-status refresh-${refreshState}`} role="status" aria-live="polite">{refreshAnnouncement}</Box>}
          {refreshError && <Box component="span" className="notice error" role={refreshState === 'error' ? 'alert' : 'status'}>{refreshError}</Box>}
          <span className="visually-hidden" role="status" aria-live="polite">{exportAnnouncement}</span>
        </Stack>
      </Paper>}

      {state === 'loading' && <Paper className="notice" role="status" sx={{ mt: 3, p: 3 }}><Typography component="h2" variant="h5">Loading portfolio</Typography><Typography>Reading the latest immutable snapshot…</Typography></Paper>}
      {authError && <Paper className="notice error" role="alert" sx={{ mt: 3, p: 3 }}><Typography component="h2" variant="h5">Session error</Typography><Typography>{authError}</Typography></Paper>}
      {state === 'error' && <Paper className="notice error" role="alert" sx={{ mt: 3, p: 3 }}><Typography component="h2" variant="h5">Unable to load portfolio</Typography><Typography>{error}</Typography><Button type="button" onClick={() => load()}>Try again</Button></Paper>}

      {state === 'ready' && data && (
        <>
          <Box id="portfolio-content" tabIndex={-1} />
          {view !== 'overview' && <Typography component="h1" className="visually-hidden">{data.portfolio.name}</Typography>}
          {view === 'overview' && <Card component="section" className="overview" aria-labelledby="overview-title" variant="outlined" sx={{ mt: 3, borderColor: 'divider' }}><CardContent sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: '2fr 1fr 1fr' }, alignItems: 'center', p: { xs: 2, sm: 2.5 } }}>
            <Box><Typography component="span" className="eyebrow">Overview</Typography><Typography component="h2" id="overview-title" variant="h4" sx={{ mt: .5 }}>{data.portfolio.name}</Typography><Typography color="text.secondary">Reporting in {data.reporting_currency}</Typography></Box>
            <Chip className={`state-pill state-${data.snapshot.state}`} label={data.snapshot.state === 'complete' ? 'Complete valuation' : data.snapshot.state === 'incomplete' ? 'Incomplete valuation' : 'No snapshot'} sx={{ justifySelf: { md: 'start' } }} />
            {data.snapshot.complete && data.snapshot.reporting_total && <Box className="total"><Typography color="text.secondary">Reporting total</Typography><Typography component="strong" variant="h5">{data.snapshot.reporting_total} {data.reporting_currency}</Typography></Box>}
            <Box component="dl" className="snapshot-meta" sx={{ m: 0, pl: { md: 2 }, borderLeft: { md: 1 }, borderColor: 'divider' }}><Box><Typography component="dt" variant="caption" color="text.secondary">Snapshot as-of</Typography><Typography component="dd" sx={{ m: 0, mt: .25 }}>{displayDate(data.snapshot.as_of)}</Typography></Box><Box sx={{ mt: 1 }}><Typography component="dt" variant="caption" color="text.secondary">Version</Typography><Typography component="dd" sx={{ m: 0, mt: .25, overflowWrap: 'anywhere' }}>{data.snapshot.calculation_version || 'Not available'}</Typography></Box></Box>
          </CardContent></Card>}

          {view === 'overview' && data.snapshot.state === 'no_snapshot' && <Paper className="notice" sx={{ mt: 3, p: 3 }}><Typography component="h2" variant="h5">No immutable snapshot yet</Typography><Typography>A snapshot is a frozen point-in-time valuation calculated from the current holdings plus dated quotes and FX rates. It does not place trades or change holdings. Earnings, value and allocation are withheld until the required market inputs exist and a snapshot is created.</Typography></Paper>}
          {view === 'overview' && data.snapshot.state === 'incomplete' && <Paper className="notice warning" sx={{ mt: 3, p: 3 }}><Typography component="h2" variant="h5">Valuation is incomplete</Typography><Typography>Reporting total is intentionally withheld. Missing, stale or invalid market dependencies must be resolved before this view can be complete.</Typography></Paper>}

          {view === 'overview' && <AllocationSection data={data} />}

          {view === 'holdings' && <HoldingMutations portfolioId={data.portfolio.id} overviewETag={overviewETag} csrfToken={csrfToken.current} holdings={data.holdings} active sessionKey={String(authGeneration.current)} scopeKey={`${data.portfolio.id}:${currency}:${view}`} onRefresh={refreshHoldingOverview} onUnauthorized={handleThesisUnauthorized}>
            {(holdingActions) => <Paper component="section" className="table-card holdings-surface" aria-labelledby="holdings-title" variant="outlined" sx={{ mt: 3, borderColor: 'divider', overflow: 'hidden' }}>
            <Stack className="section-heading" direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { xs: 'flex-start', sm: 'center' }, justifyContent: 'space-between', p: { xs: 2, sm: 2.5 } }}><Box><Typography component="span" className="eyebrow">Canonical active lots</Typography><Typography component="h2" id="holdings-title" variant="h5" sx={{ mt: .5, mb: 0 }}>Holdings</Typography></Box><Typography color="text.secondary">{visibleHoldings.length} shown · {new Set(data.holdings.map((holding) => holding.symbol)).size} symbols</Typography></Stack>
            <TableContainer className="table-scroll" sx={{ maxWidth: '100%', overflowX: 'auto' }}>
              <Table stickyHeader aria-label="Holdings" sx={{ minWidth: 1250 }}>
                <caption className="visually-hidden">Holdings</caption>
                <TableHead><TableRow>
                  {sortHeader('Instrument', 'symbol')}{sortHeader('Quantity', 'quantity')}{sortHeader('Currency', 'currency')}
                  <TableCell component="th" scope="col">Source unit cost</TableCell>{sortHeader('Cost basis', 'cost_basis')}{sortHeader('Latest price', 'latest_price')}{sortHeader('Latest market value', 'latest_value')}{sortHeader('Unrealised P/L', 'unrealised_pnl')}{sortHeader('Reporting value', 'reporting_value')}{sortHeader('Freshness', 'freshness')}
                  <TableCell component="th" scope="col">Broker / date</TableCell><TableCell component="th" scope="col"><span className="visually-hidden">Holding actions</span></TableCell>
                </TableRow></TableHead>
                <TableBody>{visibleHoldings.map((holding) => <TableRow key={holding.lot_id} hover>
                    <TableCell component="th" scope="row"><strong>{holding.symbol}</strong><span className="subline">{holding.description && holding.description !== holding.symbol ? holding.description : holding.name && holding.name !== holding.symbol ? holding.name : 'Instrument description unavailable'} · lot {holding.lot_id.slice(-8)}</span></TableCell>
                    <TableCell className="numeric">{holding.quantity}</TableCell><TableCell>{holding.currency}</TableCell><TableCell className="numeric">{formatDecimalForDisplay(holding.unit_cost)}</TableCell><TableCell className="numeric">{formatDecimalForDisplay(holding.cost_basis)}</TableCell><TableCell className="numeric">{latestPriceCell(holding)}</TableCell><TableCell className="numeric">{latestMarketValueCell(holding)}</TableCell><TableCell className="numeric">{unrealisedPnLCell(holding)}</TableCell><TableCell className="numeric">{data.snapshot.complete ? formatDecimalForDisplay(holding.reporting_value) : 'Withheld'}</TableCell><TableCell><Chip size="small" className={`flag ${qualityClass(holding)}`} label={qualityLabel(holding)} />{holding.quote_source && <span className="subline">{holding.quote_source}</span>}</TableCell>
                    <TableCell>{holding.broker || 'Missing'}<span className="subline">{holding.acquisition_date || 'Date missing'}</span><Button className="thesis-open-button" data-thesis-lot-id={holding.lot_id} type="button" onClick={(event) => { thesisReturnRef.current = event.currentTarget; thesisReturnLotIdRef.current = holding.lot_id; setThesisHolding(holding) }} aria-label={`Open thesis for ${holding.symbol} lot ${holding.lot_id}`} size="small">Open thesis</Button><Button className="thesis-open-button" data-reviews-lot-id={holding.lot_id} ref={reviewsReturnRef} type="button" onClick={(event) => { reviewsReturnRef.current = event.currentTarget; reviewsReturnLotIdRef.current = holding.lot_id; setReviewsHoldingId(holding.lot_id); setView('reviews') }} aria-label={`View reviews for ${holding.symbol} lot ${holding.lot_id}`} size="small">View reviews</Button></TableCell>
                    <TableCell>{holdingActions(holding)}</TableCell>
                </TableRow>)}</TableBody>
              </Table>
              {!visibleHoldings.length && <p className="empty">No holdings match this filter.</p>}
            </TableContainer>
          </Paper>}
          </HoldingMutations>}
        </>
      )}
      </>}
      {view === 'activity' && <ActivityView page={activity} state={activityState} error={activityError} loadingMore={activityLoadingMore} appendError={activityAppendError} announcement={activityAnnouncement} loadMoreRef={activityLoadMoreRef} statusRef={activityStatusRef} onLoadMore={(keyboardActivated) => loadActivity(activity?.next_cursor, true, keyboardActivated)} onRetry={() => activityAppendError ? loadActivity(activity?.next_cursor, true) : loadActivity(undefined, false)} />}
      {view === 'market' && <MarketDataPanel portfolioID={data?.portfolio.id || activityPortfolioID} reportingCurrency={currency} csrfToken={csrfToken.current} onUnauthorized={handleThesisUnauthorized} onReload={() => load()} />}
      {view === 'reviews' && data && <Reviews portfolioId={data.portfolio.id} holdings={data.holdings} snapshot={data.snapshot} csrfToken={csrfToken.current} holdingId={reviewsHoldingId} feedbackAttempts={feedbackAttemptsRef.current} onUnauthorized={handleThesisUnauthorized} onBackToOverview={closeReviews} />}
      </>}
    </Box>
  )
}
