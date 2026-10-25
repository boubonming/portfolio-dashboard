import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { FormEvent, KeyboardEvent as ReactKeyboardEvent, ReactNode } from 'react'
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, FormControl, FormControlLabel, FormHelperText, IconButton, InputLabel, Menu, MenuItem, Paper, Radio, RadioGroup, Select, Stack, TextField, Typography } from '@mui/material'
import { Add, Archive, MoreVert } from '@mui/icons-material'
import { apiFetch, APIError } from './api'
import type { Holding } from './App'

const identifierPattern = /^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$/
const symbolPattern = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/
const currencyPattern = /^[A-Z]{3}$/
const quantityPattern = /^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,18})?$/
const moneyPattern = /^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,18})?$/
const costBasisPattern = /^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,36})?$/
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
  responseETag?: string
}

const emptyDraft = (): Draft => ({ mode: 'inline', instrumentId: '', symbol: '', instrumentCurrency: '', description: '', assetType: '', exchange: '', quantity: '', unitCost: '', costBasis: '', currency: '', accountId: '', acquisitionDate: '' })
const receiptKeys = ['operation', 'result', 'holding', 'portfolio_revision', 'snapshot_state', 'current_snapshot_id', 'actor', 'audit_event_id', 'activity_event_id', 'idempotency_key']
const holdingKeys = ['portfolio_id', 'holding_id', 'instrument_id', 'symbol', 'quantity', 'unit_cost', 'cost_basis', 'currency', 'account_id', 'acquisition_date', 'lifecycle', 'created_at', 'removed_at']
const actorKeys = ['category', 'id', 'permissions']
const isRecord = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === 'object' && !Array.isArray(value)
const exactKeys = (value: Record<string, unknown>, required: string[], optional: string[] = []) => Object.keys(value).length >= required.length && required.every((key) => Object.prototype.hasOwnProperty.call(value, key)) && Object.keys(value).every((key) => required.includes(key) || optional.includes(key))
const boundedIdentifier = (value: unknown) => typeof value === 'string' && identifierPattern.test(value)
const boundedText = (value: unknown, max: number) => typeof value === 'string' && value.length >= 1 && value.length <= max
const nullableText = (value: unknown, max: number) => value === null || (typeof value === 'string' && value.length >= 1 && value.length <= max)
const nullableIdentifier = (value: unknown) => value === null || boundedIdentifier(value)
const optionalText = (value: unknown, max: number) => typeof value === 'string' && value.length >= 1 && value.length <= max
const positiveQuantity = (value: unknown): value is string => typeof value === 'string' && quantityPattern.test(value) && /[1-9]/.test(value)
const canonicalDate = (value: unknown) => {
  if (typeof value !== 'string' || !datePattern.test(value)) return false
  try { return new Date(`${value}T00:00:00.000Z`).toISOString().slice(0, 10) === value } catch { return false }
}
const canonicalTimestamp = (value: unknown) => {
  if (typeof value !== 'string' || !timestampPattern.test(value) || !isFinite(Date.parse(value))) return false
  const year = Number(value.slice(0, 4)); const month = Number(value.slice(5, 7)); const day = Number(value.slice(8, 10))
  return day <= new Date(Date.UTC(year, month, 0)).getUTCDate()
}

export function holdingDisplayLabel(holding: Holding) {
  const description = holding.description && holding.description !== holding.symbol
    ? holding.description
    : holding.name && holding.name !== holding.symbol ? holding.name : undefined
  const context = [
    `${holding.quantity} ${holding.currency}`,
    holding.unit_cost ? `unit cost ${holding.unit_cost} ${holding.currency}` : '',
    holding.cost_basis ? `cost basis ${holding.cost_basis} ${holding.currency}` : '',
  ].filter(Boolean)
  return [holding.symbol, description, ...context].filter(Boolean).join(' · ')
}

function shortenedLotID(lotID: string) {
  return lotID.length > 18 ? `${lotID.slice(0, 8)}…${lotID.slice(-8)}` : lotID
}

export function calculateRowMenuPosition(
  triggerRect: Pick<DOMRect, 'top' | 'bottom' | 'left' | 'right'>,
  menuRect: Pick<DOMRect, 'width' | 'height'>,
  viewportWidth: number,
  viewportHeight: number,
  gutter = 8,
  gap = 4,
) {
  const maxLeft = Math.max(gutter, viewportWidth - gutter - menuRect.width)
  const left = Math.min(Math.max(triggerRect.left, gutter), maxLeft)
  const spaceBelow = viewportHeight - gutter - (triggerRect.bottom + gap)
  const spaceAbove = triggerRect.top - gap - gutter
  const below = spaceBelow >= menuRect.height || spaceBelow >= spaceAbove
  const preferredTop = below ? triggerRect.bottom + gap : triggerRect.top - menuRect.height - gap
  const maxTop = Math.max(gutter, viewportHeight - gutter - menuRect.height)
  const top = Math.min(Math.max(preferredTop, gutter), maxTop)
  return { top, left }
}

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

export function validatePortfolioETag(value: unknown, portfolioId: string): number | null {
  if (typeof value !== 'string' || !portfolioId || !identifierPattern.test(portfolioId)) return null
  const escapedPortfolio = portfolioId.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = value.match(new RegExp(`^"${escapedPortfolio}-([0-9]+)"$`))
  if (!match || (match[1].length > 1 && match[1].startsWith('0'))) return null
  const revision = Number(match[1])
  return Number.isSafeInteger(revision) && revision >= 0 ? revision : null
}

export function validateHoldingMutationReceipt(value: unknown, expected: ReceiptExpectation) {
  if (!isRecord(value) || !exactKeys(value, receiptKeys)) throw new Error('The server returned an invalid holding receipt.')
  const receipt = value as Record<string, unknown>
  if (receipt.operation !== expected.operation || (receipt.result !== 'applied' && receipt.result !== 'replayed') || receipt.idempotency_key !== expected.idempotencyKey) throw new Error('The server returned an invalid holding receipt.')
  if (typeof receipt.portfolio_revision !== 'number' || !isFinite(receipt.portfolio_revision) || receipt.portfolio_revision % 1 !== 0 || receipt.portfolio_revision < 1 || receipt.portfolio_revision > 9007199254740991 || !boundedIdentifier(receipt.audit_event_id) || !boundedIdentifier(receipt.activity_event_id)) throw new Error('The server returned an invalid holding receipt.')
  if (receipt.snapshot_state !== 'stale' && receipt.snapshot_state !== 'no_snapshot') throw new Error('The server returned an invalid holding receipt.')
  if (receipt.snapshot_state === 'no_snapshot' ? receipt.current_snapshot_id !== null : !boundedIdentifier(receipt.current_snapshot_id)) throw new Error('The server returned an invalid holding receipt.')

  if (!isRecord(receipt.actor) || !exactKeys(receipt.actor, actorKeys)) throw new Error('The server returned an invalid holding receipt.')
  const actor = receipt.actor as Record<string, unknown>
  if (actor.category !== 'browser_user' || actor.id !== 'local-owner' || !Array.isArray(actor.permissions) || actor.permissions.length !== 1 || actor.permissions[0] !== 'portfolio:read') throw new Error('The server returned an invalid holding receipt.')

  if (!isRecord(receipt.holding) || !exactKeys(receipt.holding, holdingKeys, ['description'])) throw new Error('The server returned an invalid holding receipt.')
  const holding = receipt.holding as Record<string, unknown>
  if (holding.portfolio_id !== expected.portfolioId || !boundedIdentifier(holding.holding_id) || !boundedIdentifier(holding.instrument_id) || !boundedText(holding.symbol, 64) || !symbolPattern.test(holding.symbol as string) || !boundedText(holding.quantity, 40) || !positiveQuantity(holding.quantity) || !boundedText(holding.unit_cost, 40) || !moneyPattern.test(holding.unit_cost as string) || !boundedText(holding.cost_basis, 80) || !costBasisPattern.test(holding.cost_basis as string) || typeof holding.currency !== 'string' || !currencyPattern.test(holding.currency) || (holding.description !== undefined && !optionalText(holding.description, 256)) || !nullableIdentifier(holding.account_id) || !nullableText(holding.acquisition_date, 10) || (holding.acquisition_date !== null && !canonicalDate(holding.acquisition_date)) || (holding.removed_at !== null && !canonicalTimestamp(holding.removed_at)) || !canonicalTimestamp(holding.created_at)) throw new Error('The server returned an invalid holding receipt.')
  if (expected.operation === 'create' && (holding.lifecycle !== 'active' || holding.removed_at !== null)) throw new Error('The server returned an invalid holding receipt.')
  if (expected.operation === 'remove' && (holding.holding_id !== expected.lotId || holding.lifecycle !== 'removed' || holding.removed_at === null)) throw new Error('The server returned an invalid holding receipt.')

  if (expected.responseETag !== undefined && validatePortfolioETag(expected.responseETag, expected.portfolioId) !== receipt.portfolio_revision) throw new Error('The server returned an invalid holding receipt.')
  let body: Record<string, unknown> | null = null
  try { body = JSON.parse(expected.body) as Record<string, unknown> } catch { body = null }
  if (expected.operation === 'create') {
    if (!body || !isRecord(body) || !exactKeys(body, ['instrument', 'quantity', 'unit_cost', 'cost_basis', 'currency'], ['account_id', 'acquisition_date'])) throw new Error('The server returned an invalid holding receipt.')
    if (!positiveQuantity(body.quantity) || body.quantity.length > 40 || typeof body.unit_cost !== 'string' || body.unit_cost.length > 40 || !moneyPattern.test(body.unit_cost) || typeof body.cost_basis !== 'string' || body.cost_basis.length > 80 || !costBasisPattern.test(body.cost_basis) || !exactProductMatches(body.quantity, body.unit_cost, body.cost_basis) || typeof body.currency !== 'string' || !currencyPattern.test(body.currency) || (Object.prototype.hasOwnProperty.call(body, 'account_id') && !nullableIdentifier(body.account_id)) || (Object.prototype.hasOwnProperty.call(body, 'acquisition_date') && body.acquisition_date !== null && !canonicalDate(body.acquisition_date))) throw new Error('The server returned an invalid holding receipt.')
    if (holding.quantity !== body.quantity || holding.unit_cost !== body.unit_cost || holding.cost_basis !== body.cost_basis || holding.currency !== body.currency) throw new Error('The server returned an invalid holding receipt.')
    const bodyAccount = Object.prototype.hasOwnProperty.call(body, 'account_id') ? body.account_id : null
    const bodyDate = Object.prototype.hasOwnProperty.call(body, 'acquisition_date') ? body.acquisition_date : null
    if (holding.account_id !== bodyAccount || holding.acquisition_date !== bodyDate) throw new Error('The server returned an invalid holding receipt.')
    if (!isRecord(body.instrument)) throw new Error('The server returned an invalid holding receipt.')
    if ('instrument_id' in body.instrument) {
      if (!exactKeys(body.instrument, ['instrument_id']) || holding.instrument_id !== body.instrument.instrument_id) throw new Error('The server returned an invalid holding receipt.')
    } else {
      if (!exactKeys(body.instrument, ['symbol', 'currency'], ['description', 'asset_type', 'exchange']) || holding.symbol !== body.instrument.symbol || holding.currency !== body.instrument.currency) throw new Error('The server returned an invalid holding receipt.')
      if ('description' in body.instrument ? holding.description !== body.instrument.description : holding.description !== undefined) throw new Error('The server returned an invalid holding receipt.')
    }
  } else if (expected.body !== '' || !expected.lotId) {
    throw new Error('The server returned an invalid holding receipt.')
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
  sessionKey?: string
  onRefresh: () => Promise<string | void>
  onUnauthorized: () => void
  children?: (renderHoldingActions: (holding: Holding) => ReactNode) => ReactNode
}

type Attempt = { operation: HoldingMutationOperation; sessionKey: string; portfolioId: string; scopeKey: string; targetSignature: string; key: string; body: string; etag: string; lotId?: string }
type Authority = { active: boolean; valid: boolean; sessionKey: string; portfolioId: string; scopeKey: string }
type ModalBackground = { element: HTMLElement; inert: boolean; ariaHidden: string | null }
type ArchiveContext = { sessionKey: string; portfolioId: string; scopeKey: string; lotId: string }
type AddContext = { sessionKey: string; portfolioId: string; scopeKey: string; origin: HTMLButtonElement | null }

export default function HoldingMutations({ portfolioId, overviewETag, csrfToken, holdings, active, scopeKey, sessionKey = scopeKey, onRefresh, onUnauthorized, children }: Props) {
  const [formOpen, setFormOpen] = useState(false)
  const [openMenuLotId, setOpenMenuLotId] = useState<string | null>(null)
  const [menuPosition, setMenuPosition] = useState({ top: 0, left: 0 })
  const [archivePickerOpen, setArchivePickerOpen] = useState(false)
  const [selectedArchiveLotId, setSelectedArchiveLotId] = useState('')
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
  const formHeadingRef = useRef<HTMLElement>(null)
  const addDialogRef = useRef<HTMLDivElement>(null)
  const dialogHeadingRef = useRef<HTMLElement>(null)
  const archiveDialogRef = useRef<HTMLDivElement>(null)
  const addErrorRef = useRef<HTMLDivElement>(null)
  const archiveErrorRef = useRef<HTMLDivElement>(null)
  const addButtonRef = useRef<HTMLButtonElement>(null)
  const statusRef = useRef<HTMLParagraphElement>(null)
  const contentRef = useRef<HTMLElement>(null)
  const archiveTriggerRefs = useRef(new Map<string, HTMLButtonElement>())
  const archiveMenuRefs = useRef(new Map<string, HTMLElement>())
  const archiveKeyboardActivationRef = useRef<string | null>(null)
  const archiveOriginRef = useRef<HTMLButtonElement | null>(null)
  const archiveRestoreRef = useRef<{ lotId: string; success: boolean } | null>(null)
  const modalBackgroundRef = useRef<ModalBackground | null>(null)
  const archiveContextRef = useRef<ArchiveContext | null>(null)
  const addContextRef = useRef<AddContext | null>(null)
  const addRestoreRef = useRef<HTMLButtonElement | null>(null)
  const menuContextRef = useRef<ArchiveContext | null>(null)
  const authorityRef = useRef<Authority>({ active, valid: active, sessionKey, portfolioId, scopeKey })
  const archiveLotRef = useRef<Holding | null>(null)
  const activeRef = useRef(active)
  const portfolioIdRef = useRef(portfolioId)
  const scopeRef = useRef(scopeKey)
  const holdingsRef = useRef(holdings)
  const blockedRef = useRef(false)
  const focusFormHeading = useCallback((node: HTMLElement | null) => { formHeadingRef.current = node; if (node) node.focus() }, [])
  const focusArchiveHeading = useCallback((node: HTMLElement | null) => { dialogHeadingRef.current = node; if (node) node.focus() }, [])

  const restoreModalBackground = () => {
    const previous = modalBackgroundRef.current
    if (!previous) return
    previous.element.inert = previous.inert
    if (previous.ariaHidden === null) previous.element.removeAttribute('aria-hidden')
    else previous.element.setAttribute('aria-hidden', previous.ariaHidden)
    modalBackgroundRef.current = null
  }
  const invalidateAuthority = () => {
    generationRef.current += 1
    controllerRef.current?.abort()
    controllerRef.current = null
    busyRef.current = false
    createAttempt.current = null
    removeAttempt.current = null
    archiveLotRef.current = null
    archiveContextRef.current = null
    addContextRef.current = null
    addRestoreRef.current = null
    menuContextRef.current = null
    archiveOriginRef.current = null
    archiveRestoreRef.current = null
    restoreModalBackground()
  }
  const clearArchiveState = (resetForm = false, block = resetForm) => {
    invalidateAuthority()
    if (block) {
      authorityRef.current = { ...authorityRef.current, valid: false }
      blockedRef.current = true
    }
    setBusy(false)
    setArchiveLot(null)
    setError('')
    setStatus('')
    if (resetForm) { setDraft(emptyDraft()); setFormOpen(false); setOpenMenuLotId(null); setArchivePickerOpen(false); setSelectedArchiveLotId(''); setErrors({}) }
  }
  const nextAuthority = { active, valid: active, sessionKey, portfolioId, scopeKey }
  const authorityChanged = authorityRef.current.active !== active || authorityRef.current.sessionKey !== sessionKey || authorityRef.current.portfolioId !== portfolioId || authorityRef.current.scopeKey !== scopeKey
  if (authorityChanged || !active) {
    invalidateAuthority()
    authorityRef.current = nextAuthority
    blockedRef.current = false
  }
  activeRef.current = active
  portfolioIdRef.current = portfolioId
  scopeRef.current = scopeKey
  holdingsRef.current = holdings
  archiveLotRef.current = archiveContextRef.current ? archiveLot : null

  const addContextMatches = Boolean(addContextRef.current && addContextRef.current.sessionKey === sessionKey && addContextRef.current.portfolioId === portfolioId && addContextRef.current.scopeKey === scopeKey)
  const authorityMatches = authorityRef.current.valid && !blockedRef.current && authorityRef.current.active === active && authorityRef.current.sessionKey === sessionKey && authorityRef.current.portfolioId === portfolioId && authorityRef.current.scopeKey === scopeKey
  const isAuthorityCurrent = () => authorityRef.current.valid && !blockedRef.current && authorityRef.current.active === active && authorityRef.current.sessionKey === sessionKey && authorityRef.current.portfolioId === portfolioId && authorityRef.current.scopeKey === scopeKey
  const menuContextMatches = Boolean(menuContextRef.current && menuContextRef.current.sessionKey === sessionKey && menuContextRef.current.portfolioId === portfolioId && menuContextRef.current.scopeKey === scopeKey && holdings.some((holding) => holding.lot_id === menuContextRef.current?.lotId))
  const effectiveFormOpen = active && !authorityChanged && formOpen && addContextMatches
  const effectiveMenuLotId = active && !authorityChanged && authorityMatches && menuContextMatches ? openMenuLotId : null

  const archiveContextMatches = Boolean(archiveLot && archiveContextRef.current && archiveContextRef.current.sessionKey === sessionKey && archiveContextRef.current.portfolioId === portfolioId && archiveContextRef.current.scopeKey === scopeKey && archiveContextRef.current.lotId === archiveLot.lot_id)
  const archiveVisible = active && authorityMatches && archiveContextMatches

  const abortWork = () => {
    clearArchiveState()
  }
  useEffect(() => {
    if (!active || authorityChanged) clearArchiveState(true, false)
    return () => abortWork()
  }, [active, sessionKey, portfolioId, scopeKey])
  useEffect(() => { etagRef.current = overviewETag; scopeRef.current = scopeKey }, [overviewETag, scopeKey])
  useLayoutEffect(() => { if (effectiveFormOpen) formHeadingRef.current?.focus() }, [effectiveFormOpen])
  useEffect(() => { if (effectiveFormOpen) formHeadingRef.current?.focus() }, [effectiveFormOpen])
  useEffect(() => {
    if (!effectiveFormOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busyRef.current) {
        event.preventDefault()
        closeForm()
        return
      }
      if (event.key !== 'Tab') return
      const dialog = addDialogRef.current
      const focusable = Array.from(dialog?.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])') || [])
      if (!focusable.length) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const activeElement = document.activeElement
      if (formHeadingRef.current === activeElement || formHeadingRef.current === event.target || addErrorRef.current === activeElement || addErrorRef.current === event.target || !dialog?.contains(activeElement)) {
        event.preventDefault()
        ;(event.shiftKey ? last : first).focus()
      } else if (event.shiftKey && activeElement === first) { event.preventDefault(); last.focus() }
      else if (!event.shiftKey && activeElement === last) { event.preventDefault(); first.focus() }
    }
    const onFocusIn = (event: FocusEvent) => {
      if (!addDialogRef.current?.contains(event.target as Node)) formHeadingRef.current?.focus()
    }
    document.addEventListener('keydown', onKeyDown)
    document.addEventListener('focusin', onFocusIn)
    return () => { document.removeEventListener('keydown', onKeyDown); document.removeEventListener('focusin', onFocusIn) }
  }, [effectiveFormOpen])
  useEffect(() => {
    if (!effectiveMenuLotId) return
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (!archiveMenuRefs.current.get(effectiveMenuLotId)?.contains(target) && !archiveTriggerRefs.current.get(effectiveMenuLotId)?.contains(target)) {
        setOpenMenuLotId(null)
        menuContextRef.current = null
      }
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      const trigger = archiveTriggerRefs.current.get(effectiveMenuLotId)
      setOpenMenuLotId(null)
      menuContextRef.current = null
      if (isAuthorityCurrent()) window.setTimeout(() => trigger?.focus(), 0)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => { document.removeEventListener('pointerdown', onPointerDown); document.removeEventListener('keydown', onKeyDown) }
  }, [effectiveMenuLotId])
  useLayoutEffect(() => {
    if (!effectiveMenuLotId) return
    const trigger = archiveTriggerRefs.current.get(effectiveMenuLotId)
    const menu = archiveMenuRefs.current.get(effectiveMenuLotId)
    if (!trigger?.isConnected || !menu?.isConnected) return
    const update = () => {
      const currentTrigger = archiveTriggerRefs.current.get(effectiveMenuLotId)
      const currentMenu = archiveMenuRefs.current.get(effectiveMenuLotId)
      if (!currentTrigger?.isConnected || !currentMenu?.isConnected || !isAuthorityCurrent()) {
        setOpenMenuLotId(null)
        menuContextRef.current = null
        return
      }
      const triggerRect = currentTrigger.getBoundingClientRect()
      const menuRect = currentMenu.getBoundingClientRect()
      const nextPosition = calculateRowMenuPosition(triggerRect, menuRect, window.innerWidth, window.innerHeight)
      currentMenu.style.top = `${nextPosition.top}px`
      currentMenu.style.left = `${nextPosition.left}px`
      setMenuPosition((previous) => previous.top === nextPosition.top && previous.left === nextPosition.left ? previous : nextPosition)
    }
    update()
    menu.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
    const onScroll = () => update()
    const onResize = () => update()
    const scrollTargets: EventTarget[] = [window, document, document.body]
    let ancestor = trigger.parentElement
    while (ancestor) { scrollTargets.push(ancestor); ancestor = ancestor.parentElement }
    scrollTargets.forEach((target) => target.addEventListener('scroll', onScroll, true))
    window.addEventListener('resize', onResize)
    return () => { scrollTargets.forEach((target) => target.removeEventListener('scroll', onScroll, true)); window.removeEventListener('resize', onResize) }
  }, [effectiveMenuLotId, menuPosition])
  useLayoutEffect(() => { if (archiveVisible) dialogHeadingRef.current?.focus() }, [archiveVisible])
  useEffect(() => { if (archiveVisible) dialogHeadingRef.current?.focus() }, [archiveVisible])
  useLayoutEffect(() => {
    const modalVisible = effectiveFormOpen || archiveVisible
    if (!modalVisible) { restoreModalBackground(); return }
    const element = document.getElementById('root') || contentRef.current?.parentElement
    if (!element || modalBackgroundRef.current) return
    modalBackgroundRef.current = { element, inert: element.inert === true, ariaHidden: element.getAttribute('aria-hidden') }
    element.inert = true
    element.setAttribute('aria-hidden', 'true')
    return () => {
      const previous = modalBackgroundRef.current
      if (!previous) return
      previous.element.inert = previous.inert
      if (previous.ariaHidden === null) previous.element.removeAttribute('aria-hidden')
      else previous.element.setAttribute('aria-hidden', previous.ariaHidden)
      modalBackgroundRef.current = null
    }
  }, [effectiveFormOpen, archiveVisible])
  useEffect(() => {
    if (!archiveVisible || !archiveLot) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (!busyRef.current) { event.preventDefault(); archiveRestoreRef.current = { lotId: archiveLot.lot_id, success: false }; setArchiveLot(null) }
        return
      }
      if (event.key !== 'Tab') return
      const dialog = archiveDialogRef.current
      const focusable = Array.from(dialog?.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])') || [])
      if (!focusable.length) {
        event.preventDefault()
        ;(archiveErrorRef.current?.isConnected ? archiveErrorRef.current : dialogHeadingRef.current)?.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const active = document.activeElement
      if (active === dialogHeadingRef.current || dialogHeadingRef.current === event.target || active === archiveErrorRef.current || archiveErrorRef.current === event.target || !dialog?.contains(active)) {
        event.preventDefault()
        ;(event.shiftKey ? last : first).focus()
      } else if (event.shiftKey && active === first) { event.preventDefault(); last.focus() }
      else if (!event.shiftKey && active === last) { event.preventDefault(); first.focus() }
    }
    const onFocusIn = (event: FocusEvent) => {
      if (!archiveDialogRef.current?.contains(event.target as Node)) dialogHeadingRef.current?.focus()
    }
    document.addEventListener('keydown', onKeyDown)
    document.addEventListener('focusin', onFocusIn)
    return () => { document.removeEventListener('keydown', onKeyDown); document.removeEventListener('focusin', onFocusIn) }
  }, [archiveVisible])
  useLayoutEffect(() => {
    if (!archiveLot || archiveVisible) return
    abortWork()
  }, [archiveLot, archiveVisible])
  useLayoutEffect(() => {
    if (archiveLot || !archiveRestoreRef.current) return
    const restore = archiveRestoreRef.current
    if (restore.success && holdings.some((holding) => holding.lot_id === restore.lotId)) return
    archiveRestoreRef.current = null
    const candidates = restore.success
      ? [statusRef.current, ...holdings.filter((holding) => holding.lot_id !== restore.lotId).map((holding) => archiveTriggerRefs.current.get(holding.lot_id)), document.getElementById('holdings-title'), addButtonRef.current]
      : [archiveOriginRef.current, addButtonRef.current]
    const target = candidates.find((candidate): candidate is HTMLElement => Boolean(candidate?.isConnected))
    target?.focus()
    archiveContextRef.current = null
    archiveOriginRef.current = null
  }, [archiveLot, holdings, status])
  useLayoutEffect(() => {
    if (archiveVisible && archiveLot && error && !busy) archiveErrorRef.current?.focus()
  }, [archiveVisible, archiveLot, error, busy])
  useLayoutEffect(() => {
    if (effectiveFormOpen || !addRestoreRef.current) return
    const target = addRestoreRef.current
    addRestoreRef.current = null
    if (target.isConnected) target.focus()
  }, [effectiveFormOpen])
  useLayoutEffect(() => {
    if (effectiveFormOpen && error && !busy) addErrorRef.current?.focus()
  }, [effectiveFormOpen, error, busy])
  const update = (field: keyof Draft, value: string) => {
    if (!isAuthorityCurrent()) return
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
    if (draft.quantity.length > 40 || !positiveQuantity(draft.quantity)) next.quantity = 'Use a positive canonical decimal of at most 40 characters.'
    if (draft.unitCost.length > 40 || !moneyPattern.test(draft.unitCost)) next.unitCost = 'Use a non-negative canonical decimal of at most 40 characters.'
    if (draft.costBasis.length > 80 || !costBasisPattern.test(draft.costBasis)) next.costBasis = 'Use a non-negative canonical decimal of at most 80 characters and 36 fractional digits.'
    if (positiveQuantity(draft.quantity) && moneyPattern.test(draft.unitCost) && costBasisPattern.test(draft.costBasis) && !exactProductMatches(draft.quantity, draft.unitCost, draft.costBasis)) next.costBasis = 'Quantity × unit cost must equal cost basis exactly.'
    if (!currencyPattern.test(draft.currency)) next.currency = 'Use three uppercase currency letters.'
    if (draft.mode === 'inline' && currencyPattern.test(draft.instrumentCurrency) && draft.currency !== draft.instrumentCurrency) next.currency = 'Holding currency must match instrument currency.'
    if (draft.accountId && !identifierPattern.test(draft.accountId)) next.accountId = 'Enter a valid account ID.'
    if (draft.acquisitionDate && (!datePattern.test(draft.acquisitionDate) || !canonicalDate(draft.acquisitionDate))) next.acquisitionDate = 'Use a real date in YYYY-MM-DD form.'
    return next
  }

  const create = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const context = addContextRef.current
    if (busyRef.current || !active || !context || context.sessionKey !== sessionKey || context.portfolioId !== portfolioId || context.scopeKey !== scopeKey) return
    const nextErrors = validateDraft()
    if (Object.keys(nextErrors).length) {
      nextErrors.form = 'Fix the highlighted fields before creating the holding.'
      document.getElementById(Object.keys(nextErrors).find((key) => key !== 'form') || 'holding-form-title')?.focus()
    }
    setErrors(nextErrors)
    setError('')
    if (Object.keys(nextErrors).length) return
    const instrument = draft.mode === 'existing'
      ? { instrument_id: draft.instrumentId }
      : { symbol: draft.symbol, currency: draft.instrumentCurrency, ...(draft.description ? { description: draft.description } : {}), ...(draft.assetType ? { asset_type: draft.assetType } : {}), ...(draft.exchange ? { exchange: draft.exchange } : {}) }
    const payload = { instrument, quantity: draft.quantity, unit_cost: draft.unitCost, cost_basis: draft.costBasis, currency: draft.currency, ...(draft.accountId ? { account_id: draft.accountId } : {}), ...(draft.acquisitionDate ? { acquisition_date: draft.acquisitionDate } : {}) }
    const body = JSON.stringify(payload)
    const targetSignature = `${portfolioId}|${scopeKey}|create|${body}`
    const existingAttempt = createAttempt.current?.targetSignature === targetSignature ? createAttempt.current : null
    const etag = etagRef.current
    if (!existingAttempt && (typeof etag !== 'string' || validatePortfolioETag(etag, portfolioId) === null)) { setError('No current overview revision is available. Reload the overview before adding a holding.'); return }
    const attempt = existingAttempt || { operation: 'create' as const, sessionKey, portfolioId, scopeKey, targetSignature, key: randomIdempotencyKey(), body, etag: etag as string }
    createAttempt.current = attempt
    await submit('create', attempt, undefined, `/api/v1/portfolios/${encodeURIComponent(portfolioId)}/holdings`, 'POST', body)
  }

  const submit = async (operation: HoldingMutationOperation, attempt: Attempt, lotId: string | undefined, url: string, method: 'POST' | 'DELETE', body?: string) => {
    if (busyRef.current || !active || !isAuthorityCurrent() || attempt.sessionKey !== sessionKey || attempt.portfolioId !== portfolioId || attempt.scopeKey !== scopeKey || validatePortfolioETag(attempt.etag, portfolioId) === null) return
    const generation = generationRef.current
    const controller = new AbortController()
    controllerRef.current = controller
    busyRef.current = true; setBusy(true); setError(''); setStatus(operation === 'create' ? 'Creating holding…' : 'Archiving holding…')
    const current = () => generation === generationRef.current && !controller.signal.aborted && activeRef.current && authorityRef.current.valid && !blockedRef.current && authorityRef.current.active === activeRef.current && authorityRef.current.sessionKey === sessionKey && authorityRef.current.sessionKey === attempt.sessionKey && activeRef.current && portfolioIdRef.current === portfolioId && scopeRef.current === scopeKey && authorityRef.current.portfolioId === portfolioId && authorityRef.current.scopeKey === scopeKey
    try {
      const response = await apiFetch(url, { method, body: attempt.body, signal: controller.signal, csrfToken, headers: { ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}), 'If-Match': attempt.etag, 'Idempotency-Key': attempt.key } })
      if (!current()) return
      if (!response.ok) {
        const code = await responseErrorCode(response)
        if (!current()) return
        if (operation === 'create') createAttempt.current = null
        else removeAttempt.current = null
        if (response.status === 409 && code === 'stale_revision') {
          await onRefresh()
          if (!current()) return
        }
        throw new APIError(response.status, errorMessage(response.status, code))
      }
      const receipt = await response.json()
      if (!current()) return
      validateHoldingMutationReceipt(receipt, { operation, portfolioId, idempotencyKey: attempt.key, body: attempt.body, lotId, responseETag: response.headers.get('ETag') || '' })
      const result = (receipt as { result: 'applied' | 'replayed' }).result
      const refreshedETag = await onRefresh()
      if (!current()) return
      if (validatePortfolioETag(refreshedETag, portfolioId) !== (receipt as { portfolio_revision: number }).portfolio_revision) throw new Error('The refreshed overview revision did not match the archive receipt. Reload the overview and try again.')
      etagRef.current = refreshedETag as string
      if (operation === 'create') { createAttempt.current = null; setDraft(emptyDraft()); addRestoreRef.current = addContextRef.current?.origin || addButtonRef.current; addContextRef.current = null; setFormOpen(false) } else { removeAttempt.current = null; archiveRestoreRef.current = { lotId: lotId || '', success: true }; setArchiveLot(null) }
      setStatus(`${operation === 'create' ? 'Holding creation' : 'Holding archive'} ${result === 'replayed' ? 'replayed' : 'applied'} successfully. Overview refreshed; snapshot state is ${receipt.snapshot_state === 'no_snapshot' ? 'no snapshot' : 'stale'}.`)
    } catch (reason) {
      if (!current()) return
      if (reason instanceof APIError && reason.status === 401) {
        clearArchiveState(true)
        onUnauthorized()
        return
      }
      if (reason instanceof TypeError) { setStatus(''); setError('Network uncertainty. Retry with the same safe attempt; the draft is preserved.'); return }
      setStatus(''); setError(reason instanceof Error ? reason.message : 'The holding change could not be applied. Try again.')
    } finally {
      if (controllerRef.current === controller) { controllerRef.current = null; busyRef.current = false; setBusy(false) }
    }
  }

  const confirmArchive = async () => {
    const archiveContext = archiveContextRef.current
    const lot = archiveLotRef.current
    if (!lot || !archiveContext || !activeRef.current || busyRef.current || !isAuthorityCurrent() || archiveContext.sessionKey !== sessionKey || archiveContext.portfolioId !== portfolioIdRef.current || archiveContext.portfolioId !== portfolioId || archiveContext.scopeKey !== scopeRef.current || archiveContext.scopeKey !== scopeKey || archiveContext.lotId !== lot.lot_id) return
    const lotId = lot.lot_id
    const etag = etagRef.current
    const targetSignature = `${portfolioIdRef.current}|${scopeRef.current}|remove|${lotId}`
    const existingAttempt = removeAttempt.current?.targetSignature === targetSignature && removeAttempt.current.sessionKey === sessionKey ? removeAttempt.current : null
    if (!existingAttempt && (typeof etag !== 'string' || validatePortfolioETag(etag, portfolioIdRef.current) === null)) return
    const attempt = existingAttempt || { operation: 'remove' as const, sessionKey, portfolioId: portfolioIdRef.current, scopeKey: scopeRef.current, targetSignature, key: randomIdempotencyKey(), body: '', etag: etag as string, lotId }
    removeAttempt.current = attempt
    await submit('remove', attempt, lotId, `/api/v1/portfolios/${encodeURIComponent(portfolioIdRef.current)}/holdings/${encodeURIComponent(lotId)}`, 'DELETE')
  }

  const openArchive = (lot: Holding, origin: HTMLButtonElement) => {
    const currentLot = holdingsRef.current.find((holding) => holding.lot_id === lot.lot_id)
    if (!currentLot || busy || !active || !isAuthorityCurrent() || (!mutationETagValid && !removeAttempt.current)) return
    archiveOriginRef.current = origin
    archiveContextRef.current = { sessionKey, portfolioId, scopeKey, lotId: currentLot.lot_id }
    archiveLotRef.current = currentLot
    setOpenMenuLotId(null)
    menuContextRef.current = null
    setError('')
    setStatus('')
    setArchiveLot(lot)
  }

  const renderHoldingActions = (holding: Holding) => {
    const menuOpen = effectiveMenuLotId === holding.lot_id
    const openMenu = () => {
      if (busy || !active || !isAuthorityCurrent() || !holdingsRef.current.some((currentHolding) => currentHolding.lot_id === holding.lot_id)) return
      const current = effectiveMenuLotId === holding.lot_id
      menuContextRef.current = current ? null : { sessionKey, portfolioId, scopeKey, lotId: holding.lot_id }
      setOpenMenuLotId(current ? null : holding.lot_id)
    }
    const activateFromKeyboard = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
      if (event.key === 'Enter') {
        event.preventDefault()
        openMenu()
      } else if (event.key === ' ') {
        event.preventDefault()
        archiveKeyboardActivationRef.current = holding.lot_id
      }
    }
    const finishKeyboardActivation = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
      if (event.key !== ' ') return
      event.preventDefault()
      if (archiveKeyboardActivationRef.current !== holding.lot_id) return
      archiveKeyboardActivationRef.current = null
      openMenu()
    }
    return <Box className="holding-row-actions">
      <IconButton ref={(node) => { if (node) archiveTriggerRefs.current.set(holding.lot_id, node); else archiveTriggerRefs.current.delete(holding.lot_id) }} className="row-action-trigger" type="button" aria-label={`Actions for ${holding.symbol} lot ${holding.lot_id}`} aria-expanded={menuOpen} aria-haspopup="menu" data-archive-trigger-lot-id={holding.lot_id} onClick={openMenu} onKeyDown={activateFromKeyboard} onKeyUp={finishKeyboardActivation} disabled={busy} size="small"><MoreVert aria-hidden="true" /></IconButton>
      <Menu
        open={menuOpen}
        onClose={() => {
          const trigger = archiveTriggerRefs.current.get(holding.lot_id)
          setOpenMenuLotId(null)
          menuContextRef.current = null
          if (isAuthorityCurrent()) window.setTimeout(() => trigger?.focus(), 0)
        }}
        anchorReference="anchorPosition"
        anchorPosition={menuPosition}
        marginThreshold={8}
        transformOrigin={{ vertical: 'top', horizontal: 'left' }}
        autoFocus
        disableRestoreFocus
        transitionDuration={0}
        slotProps={{
          root: { className: 'row-action-menu-portal' },
          paper: {
            ref: (node: any) => {
              if (!node) { archiveMenuRefs.current.delete(holding.lot_id); return }
              const element = node as HTMLElement
              archiveMenuRefs.current.set(holding.lot_id, element)
              const trigger = archiveTriggerRefs.current.get(holding.lot_id)
              if (trigger?.isConnected && isAuthorityCurrent()) {
                const triggerRect = trigger.getBoundingClientRect()
                const menuRect = element.getBoundingClientRect()
                const nextPosition = calculateRowMenuPosition(triggerRect, menuRect, window.innerWidth, window.innerHeight)
                element.style.top = `${nextPosition.top}px`
                element.style.left = `${nextPosition.left}px`
                setMenuPosition((previous) => previous.top === nextPosition.top && previous.left === nextPosition.left ? previous : nextPosition)
              }
            },
            className: 'row-action-menu',
            elevation: 8,
            style: { top: menuPosition.top, left: menuPosition.left },
          } as any,
          list: {
            className: 'row-action-menu-list row-action-menu-portal',
            'data-archive-menu-lot-id': holding.lot_id,
          } as any,
        }}
      >
        <MenuItem type="button" onClick={(event) => openArchive(holding, archiveTriggerRefs.current.get(holding.lot_id) || event.currentTarget as unknown as HTMLButtonElement)} disabled={busy || (!mutationETagValid && !removeAttempt.current)}>
          <Archive fontSize="small" aria-hidden="true" sx={{ mr: 1 }} /> Archive holding
        </MenuItem>
      </Menu>
    </Box>
  }

  const closeForm = () => {
    if (busy || !isAuthorityCurrent()) return
    addRestoreRef.current = addContextRef.current?.origin || addButtonRef.current
    addContextRef.current = null
    setFormOpen(false)
    setErrors({})
    setError('')
  }

  const openForm = (origin: HTMLButtonElement) => {
    if (busy || !isAuthorityCurrent()) return
    addContextRef.current = { sessionKey, portfolioId, scopeKey, origin }
    setFormOpen(true)
    setError('')
    setStatus('')
  }

  const cancelArchive = (lotId: string) => {
    const context = archiveContextRef.current
    if (busy || !isAuthorityCurrent() || !context || context.sessionKey !== sessionKey || context.portfolioId !== portfolioId || context.scopeKey !== scopeKey || context.lotId !== lotId) return
    archiveRestoreRef.current = { lotId, success: false }
    setArchiveLot(null)
  }

  const toggleArchivePicker = () => {
    if (busy || !isAuthorityCurrent()) return
    setArchivePickerOpen((open) => !open)
    setError('')
    setStatus('')
  }

  const continueToArchive = () => {
    if (busy || !isAuthorityCurrent() || !selectedArchiveLotId) return
    const lot = holdingsRef.current.find((holding) => holding.lot_id === selectedArchiveLotId)
    if (!lot) return
    archiveOriginRef.current = document.getElementById('archive-continue') as HTMLButtonElement | null
    archiveContextRef.current = { sessionKey, portfolioId, scopeKey, lotId: lot.lot_id }
    archiveLotRef.current = lot
    setError('')
    setStatus('')
    setArchiveLot(lot)
  }

  const repositionMenu = () => {
    if (!effectiveMenuLotId) return
    const trigger = archiveTriggerRefs.current.get(effectiveMenuLotId)
    const menu = archiveMenuRefs.current.get(effectiveMenuLotId)
    if (!trigger?.isConnected || !menu?.isConnected || !isAuthorityCurrent()) return
    const nextPosition = calculateRowMenuPosition(trigger.getBoundingClientRect(), menu.getBoundingClientRect(), window.innerWidth, window.innerHeight)
    menu.style.top = `${nextPosition.top}px`
    menu.style.left = `${nextPosition.left}px`
    setMenuPosition((previous) => previous.top === nextPosition.top && previous.left === nextPosition.left ? previous : nextPosition)
  }

  if (!active) return null
  const mutationETagValid = validatePortfolioETag(overviewETag, portfolioId) !== null
  return <>
    <Paper component="section" ref={contentRef} onScrollCapture={repositionMenu} className="holding-mutation-card" aria-labelledby="manage-holdings-title" variant="outlined" sx={{ mt: 3, borderColor: 'divider', p: { xs: 1.5, sm: 2.5 } }}>
      <Stack className="holding-actions-bar" direction={{ xs: 'column', sm: 'row' }} spacing={1.5} sx={{ alignItems: { xs: 'flex-start', sm: 'center' }, justifyContent: 'space-between' }}><Box><Typography component="span" className="eyebrow">Holdings</Typography><Typography component="h2" className="visually-hidden" id="manage-holdings-title">Manage holdings</Typography></Box><Box className="holding-actions"><Button ref={addButtonRef} type="button" onClick={(event) => openForm(event.currentTarget)} disabled={busy} variant="contained" startIcon={<Add aria-hidden="true" />}>Add holding</Button></Box></Stack>
      {status && <p ref={statusRef} className="notice success" role="status" aria-live="polite" tabIndex={-1}>{status}</p>}
      {error && !archiveLot && !effectiveFormOpen && <p className="notice error" role="alert">{error}</p>}
      {children?.(renderHoldingActions)}
      {!children && <Box sx={{ borderTop: 1, borderColor: 'divider', display: 'grid', gap: 1.25, mt: 3, pt: 2.5 }}>
        <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { xs: 'flex-start', sm: 'center' }, justifyContent: 'space-between' }}>
          <Box><Typography component="h3" variant="h6">Archive a holding</Typography><Typography color="text.secondary">Archive is a logical removal only; history and audit records are preserved.</Typography></Box>
          <Button type="button" onClick={toggleArchivePicker} disabled={busy}>{archivePickerOpen ? 'Close archive controls' : 'Archive a holding'}</Button>
        </Stack>
        {archivePickerOpen && <Box sx={{ display: 'grid', gap: 1, maxWidth: '52rem', pt: .5 }}>
          <FormControl fullWidth size="small" disabled={busy || !holdings.length}>
            <InputLabel id="archive-holding-label">Holding to archive</InputLabel>
            <Select native labelId="archive-holding-label" id="archive-holding" value={selectedArchiveLotId} label="Holding to archive" inputProps={{ 'aria-label': 'Holding to archive' }} onChange={(event) => { if (isAuthorityCurrent()) setSelectedArchiveLotId(event.target.value) }}>
              <option value="">Choose an active holding…</option>
              {holdings.map((holding) => <option key={holding.lot_id} value={holding.lot_id}>{holdingDisplayLabel(holding)}</option>)}
            </Select>
            <FormHelperText>Choose the exact lot. The archive confirmation will preserve its full audit identity.</FormHelperText>
          </FormControl>
          <Button id="archive-continue" type="button" onClick={continueToArchive} disabled={busy || !selectedArchiveLotId || !mutationETagValid} aria-busy={busy} sx={{ justifySelf: 'start' }}>Continue to archive</Button>
        </Box>}
      </Box>}
    </Paper>
    {effectiveFormOpen && <Dialog
      ref={addDialogRef}
      open={effectiveFormOpen}
      onClose={() => closeForm()}
      disableAutoFocus
      disableRestoreFocus
      transitionDuration={0}
      aria-labelledby="holding-form-title"
      aria-describedby={error ? 'holding-form-description holding-form-error' : 'holding-form-description'}
      fullWidth
      maxWidth="md"
      slotProps={{ paper: { sx: { maxHeight: 'calc(100vh - 2rem)' } } }}
    >
      <form onSubmit={create}>
        <DialogTitle id="holding-form-title" ref={focusFormHeading} tabIndex={-1}>Add one holding</DialogTitle>
        <DialogContent dividers sx={{ display: 'grid', gap: 1.5 }}>
          <Typography id="holding-form-description" color="text.secondary">Use an existing portfolio instrument ID or define one new instrument. Financial values stay as exact text.</Typography>
          <FormControl component="fieldset" variant="standard">
            <Typography component="legend" sx={{ fontWeight: 700 }}>Instrument mode</Typography>
            <FormHelperText id="instrument-mode-help">Existing mode uses an immutable instrument ID. Inline mode creates one new instrument identity; Symbol is display and market-data text, never the identity.</FormHelperText>
            <RadioGroup name="instrument-mode" value={draft.mode} onChange={(event) => update('mode', event.target.value as InstrumentMode)} aria-describedby="instrument-mode-help" row>
              <FormControlLabel value="existing" control={<Radio />} label="Existing instrument ID" />
              <FormControlLabel value="inline" control={<Radio />} label="New inline instrument" />
            </RadioGroup>
          </FormControl>
          {draft.mode === 'existing' ? <Field id="instrument-id" label="Instrument ID" help="This immutable ID selects the existing instrument; it is not replaced by its symbol." value={draft.instrumentId} error={errors.instrumentId} onChange={(value) => update('instrumentId', value)} /> : <Box sx={{ display: 'grid', gap: 1.5, gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' } }}><Field id="symbol" label="Symbol" help="Display and market-data symbol only; it does not identify the instrument." value={draft.symbol} error={errors.symbol} onChange={(value) => update('symbol', value)} /><Field id="instrument-currency" label="Instrument currency" value={draft.instrumentCurrency} error={errors.instrumentCurrency} onChange={(value) => update('instrumentCurrency', value)} /><Field id="description" label="Description (optional)" value={draft.description} error={errors.description} onChange={(value) => update('description', value)} /><Field id="asset-type" label="Asset type (optional)" value={draft.assetType} error={errors.assetType} onChange={(value) => update('assetType', value)} /><Field id="exchange" label="Exchange (optional)" value={draft.exchange} error={errors.exchange} onChange={(value) => update('exchange', value)} /></Box>}
          <Box sx={{ display: 'grid', gap: 1.5, gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' } }}><Field id="quantity" label="Quantity" value={draft.quantity} error={errors.quantity} onChange={(value) => update('quantity', value)} inputMode="decimal" maxLength={40} /><Field id="unit-cost" label="Unit cost" value={draft.unitCost} error={errors.unitCost} onChange={(value) => update('unitCost', value)} inputMode="decimal" maxLength={40} /><Field id="cost-basis" label="Cost basis" value={draft.costBasis} error={errors.costBasis} onChange={(value) => update('costBasis', value)} inputMode="decimal" maxLength={80} /><Field id="currency" label="Holding currency" value={draft.currency} error={errors.currency} onChange={(value) => update('currency', value)} /><Field id="account-id" label="Account ID (optional)" value={draft.accountId} error={errors.accountId} onChange={(value) => update('accountId', value)} /><Field id="acquisition-date" label="Acquisition date (optional)" value={draft.acquisitionDate} error={errors.acquisitionDate} onChange={(value) => update('acquisitionDate', value)} inputMode="numeric" /></Box>
          {errors.form && <Alert severity="error" role="alert" aria-live="assertive" tabIndex={-1}>{errors.form}</Alert>}
          {error && <Alert ref={addErrorRef} id="holding-form-error" severity="error" role="alert" aria-live="assertive" tabIndex={-1}>{error}</Alert>}
        </DialogContent>
        <DialogActions><Button type="submit" variant="contained" disabled={busy || (!mutationETagValid && !createAttempt.current)} aria-busy={busy}>{busy ? 'Creating holding…' : 'Create holding'}</Button><Button type="button" onClick={closeForm} disabled={busy}>Cancel</Button></DialogActions>
      </form>
    </Dialog>}
    {archiveVisible && archiveLot && <Dialog
      ref={archiveDialogRef}
      open
      onClose={() => cancelArchive(archiveLot.lot_id)}
      disableAutoFocus
      disableRestoreFocus
      transitionDuration={0}
      aria-labelledby="archive-dialog-title"
      aria-describedby={error ? 'archive-dialog-description archive-dialog-error' : 'archive-dialog-description'}
      fullWidth
      maxWidth="sm"
    >
      <DialogTitle id="archive-dialog-title" ref={focusArchiveHeading} tabIndex={-1}>Archive {archiveLot.symbol} holding?</DialogTitle>
      <DialogContent dividers><Typography id="archive-dialog-description">You are archiving <strong>{holdingDisplayLabel(archiveLot)}</strong>. This is a logical removal only; it does not sell, trade, or permanently delete the holding. Its financial history, reviews, thesis and audit records remain preserved.</Typography><Typography color="text.secondary" sx={{ mt: 1 }} title={archiveLot.lot_id}>Lot ID: {shortenedLotID(archiveLot.lot_id)} <span className="visually-hidden">Full lot ID: {archiveLot.lot_id}</span></Typography>{error && <Alert ref={archiveErrorRef} id="archive-dialog-error" severity="error" role="alert" aria-live="assertive" tabIndex={-1} sx={{ mt: 1 }}>{error}</Alert>}</DialogContent>
      <DialogActions><Button type="button" variant="contained" onClick={confirmArchive} disabled={busy || (!mutationETagValid && !removeAttempt.current)} aria-busy={busy}>{busy ? 'Archiving holding…' : 'Confirm archive holding'}</Button><Button type="button" onClick={() => cancelArchive(archiveLot.lot_id)} disabled={busy}>Cancel archive</Button></DialogActions>
    </Dialog>}
  </>
}

function Field({ id, label, value, error, help, onChange, inputMode, maxLength }: { id: string; label: string; value: string; error?: string; help?: string; onChange: (value: string) => void; inputMode?: 'decimal' | 'numeric'; maxLength?: number }) {
  return <TextField id={id} label={label} value={value} onChange={(event) => onChange(event.target.value)} slotProps={{ htmlInput: { inputMode, maxLength } }} error={Boolean(error)} helperText={error || help || ' '} fullWidth size="small" />
}
