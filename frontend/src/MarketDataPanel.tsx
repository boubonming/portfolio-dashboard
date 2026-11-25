import { useEffect, useMemo, useRef, useState, type FocusEvent } from 'react'
import { APIError, discoverMarketData, getMarketDataSetup, saveMarketMappings, type MarketDataSetup, type MappingBatchItem, type MarketDiscoveryCandidate } from './api'
import { prefillMappingSuggestion, type MappingDraftSeed } from './marketMappingSuggestions'

type Draft = MappingDraftSeed & { touched: boolean; candidates: MarketDiscoveryCandidate[] }
type SaveConflict = 'stale' | 'uncertain' | null
type ReconciliationState = 'none' | 'pending' | 'failed' | 'ready'
type ReadOptions = { discover?: boolean; adoptDrafts?: boolean; reconcile?: boolean; resetDrafts?: boolean; acknowledge?: boolean }

export type MarketDataWorkflowState = {
  dirty: boolean
  pending: boolean
  uncertain: boolean
}

type MarketDataPanelProps = {
  portfolioID: string
  reportingCurrency: string
  csrfToken?: string
  onUnauthorized: () => void
  onWorkflowStateChange?: (state: MarketDataWorkflowState) => void
}

function canonicalQuoteCurrency(value: string | undefined): string | null {
  const trimmed = value?.trim() || ''
  return /^[A-Z]{3}$/.test(trimmed) ? trimmed : null
}

function quoteCurrencyError(value: string | undefined): string {
  return value?.trim() ? 'Use a three-letter uppercase currency code, such as USD.' : 'Quote currency is required before saving this mapping.'
}

function normalizedSymbol(value: string | undefined): string {
  return (value || '').trim().toUpperCase()
}

function candidateEvidence(candidate: MarketDiscoveryCandidate): string {
  return [
    `Identifier: ${candidate.identifier}`,
    `Symbol: ${candidate.symbol || 'Not supplied'}`,
    `Name: ${candidate.name || 'Not supplied'}`,
    `Description: ${candidate.description || 'Not supplied'}`,
    `Type: ${candidate.type || 'Not supplied'}`,
    `Region: ${candidate.region || 'Not supplied'}`,
    `Exchange: ${candidate.exchange || 'Not supplied'}`,
    `Currency: ${candidate.currency || 'Not supplied'}`,
  ].join(' · ')
}

function draftSeeds(next: MarketDataSetup): Record<string, Draft> {
  return Object.fromEntries(next.instruments.map((instrument) => [instrument.instrument_id, { ...prefillMappingSuggestion(instrument, next.providers), touched: false, candidates: [] }]))
}

export default function MarketDataPanel({ portfolioID, reportingCurrency, csrfToken, onUnauthorized, onWorkflowStateChange }: MarketDataPanelProps) {
  const [setup, setSetup] = useState<MarketDataSetup | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [saving, setSaving] = useState(false)
  const [discoveryMessage, setDiscoveryMessage] = useState('')
  const [readError, setReadError] = useState('')
  const [saveError, setSaveError] = useState('')
  const [saveReceipt, setSaveReceipt] = useState('')
  const [conflict, setConflict] = useState<SaveConflict>(null)
  const [adoptionChoice, setAdoptionChoice] = useState(false)
  const [reconciliationRequired, setReconciliationRequired] = useState(false)
  const [reconciliation, setReconciliation] = useState<ReconciliationState>('none')
  const [scopeChoice, setScopeChoice] = useState(false)
  const controllerRef = useRef<AbortController | null>(null)
  const discoveryControllerRef = useRef<AbortController | null>(null)
  const mutationControllerRef = useRef<AbortController | null>(null)
  const savingRef = useRef(false)
  const generationRef = useRef(0)
  const setupRef = useRef<MarketDataSetup | null>(null)
  const conflictRef = useRef<SaveConflict>(null)
  const scopeRef = useRef({ portfolioID, reportingCurrency })
  scopeRef.current = { portfolioID, reportingCurrency }
  const unauthorizedRef = useRef(onUnauthorized)
  unauthorizedRef.current = onUnauthorized
  setupRef.current = setup
  conflictRef.current = conflict
  const rowRefs = useRef<Record<string, HTMLTableRowElement | null>>({})
  const draftsRef = useRef<Record<string, Draft>>({})
  const loadedScopeRef = useRef<{ portfolioID: string; reportingCurrency: string } | null>(null)
  const mountedRef = useRef(true)
  draftsRef.current = drafts

  const hasLocalDraftChanges = () => Object.values(draftsRef.current).some((draft) => draft.touched)
  useEffect(() => {
    onWorkflowStateChange?.({ dirty: hasLocalDraftChanges() || conflict !== null || scopeChoice, pending: saving || (reconciliationRequired && conflict !== null), uncertain: conflict === 'uncertain' })
  }, [drafts, saving, reconciliationRequired, conflict, scopeChoice, onWorkflowStateChange])

  const clearPrivateState = () => {
    savingRef.current = false
    mutationControllerRef.current = null
    setSaving(false); setSetup(null); setDrafts({}); setState('loading')
    setDiscoveryMessage(''); setReadError(''); setSaveError(''); setSaveReceipt('')
    setConflict(null); setAdoptionChoice(false); setReconciliationRequired(false); setReconciliation('none'); setScopeChoice(false)
    setupRef.current = null; conflictRef.current = null
  }

  const expireSession = () => {
    generationRef.current += 1
    controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort()
    controllerRef.current = null; discoveryControllerRef.current = null; mutationControllerRef.current = null
    savingRef.current = false
    setSaving(false); setSetup(null); setDrafts({}); setState('loading')
    setDiscoveryMessage(''); setReadError(''); setSaveError(''); setSaveReceipt('')
    setConflict(null); setAdoptionChoice(false); setReconciliationRequired(false)
    setupRef.current = null; conflictRef.current = null
    unauthorizedRef.current()
  }

  const applyDiscovery = (next: MarketDataSetup, generation: number, controller: AbortController) => {
    discoverMarketData(portfolioID, next.etag, csrfToken, controller.signal, next).then((discovery) => {
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID) return
      setDrafts((current) => {
        const updated = { ...current }
        for (const result of discovery.results) {
          const instrument = next.instruments.find((item) => item.instrument_id === result.instrument_id)
          const existing = updated[result.instrument_id]
          if (!instrument || !existing || existing.touched || existing.confirmed || instrument.mappings.length > 0 || result.status === 'committed') continue
          const exact = result.candidates.filter((candidate) => normalizedSymbol(candidate.symbol || candidate.identifier) === normalizedSymbol(instrument.symbol) && (!candidate.currency || candidate.currency === instrument.currency))
          let guidance = result.diagnostic || 'No unique exact provider candidate was returned. Enter and verify the identifier manually.'
          if (result.status === 'no_result') guidance = result.diagnostic || 'No provider candidate was found. Enter and verify the exact identifier manually.'
          if (result.status === 'unavailable' || result.status === 'error') guidance = result.diagnostic || 'Provider discovery failed for this instrument. Enter and verify the exact identifier manually.'
          if (result.status === 'candidates' && exact.length > 1) guidance = 'Multiple exact provider candidates were returned. Choose one explicitly, then verify it.'
          if (result.status === 'candidates' && exact.length === 1) {
            const candidate = exact[0]
            updated[result.instrument_id] = { ...existing, provider: candidate.provider, providerSymbol: candidate.identifier, provenance: `Suggested by ${candidate.provider} — unverified. Provider-reported identity: ${candidateEvidence(candidate)}`, confirmed: false, suggested: true, candidates: result.candidates, guidance: result.diagnostic || 'A unique exact provider candidate was returned; verify it before confirming.' }
          } else updated[result.instrument_id] = { ...existing, candidates: result.candidates, guidance }
        }
        return updated
      })
      setDiscoveryMessage('Provider discovery completed. Suggestions remain unverified until you confirm them.')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      setDiscoveryMessage('Provider discovery was unavailable. Enter and verify exact identifiers manually.')
    })
  }

  const readSetup = (options: ReadOptions = {}) => {
    const discover = options.discover === true
    const adoptDrafts = options.adoptDrafts === true
    const reconcile = options.reconcile === true
    const resetDrafts = options.resetDrafts === true
    const acknowledge = options.acknowledge === true
    const generation = ++generationRef.current
    controllerRef.current?.abort(); discoveryControllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    if (!setup) setState('loading')
    setReadError('')
    if (reconcile) { setReconciliation('pending'); setAdoptionChoice(false) }
    if (adoptDrafts) setAdoptionChoice(false)
    getMarketDataSetup(portfolioID, controller.signal).then((next) => {
      if (controller.signal.aborted || generation !== generationRef.current || next.portfolio_id !== scopeRef.current.portfolioID) return
      const previousSetup = setupRef.current
      setSetup(next)
      setupRef.current = next
      if (resetDrafts || adoptDrafts || previousSetup?.portfolio_id !== next.portfolio_id) setDrafts(draftSeeds(next))
      setState('ready')
      const currentConflict = conflictRef.current
      if (reconcile) {
        setReconciliation(currentConflict === 'stale' || currentConflict === 'uncertain' ? 'ready' : 'none')
        setReconciliationRequired(currentConflict === 'stale' || currentConflict === 'uncertain' ? true : false)
      } else if (adoptDrafts || acknowledge) setReconciliation('none')
      if (adoptDrafts || acknowledge || (reconcile && currentConflict === null)) {
        setConflict(null)
        conflictRef.current = null
      }
      if (adoptDrafts || (acknowledge && currentConflict === null)) { setSaveError(''); setReconciliationRequired(false) }
      if (adoptDrafts) { setSaveReceipt(''); setReconciliation('none'); setReconciliationRequired(false) }
      if (reconcile && (currentConflict === 'stale' || currentConflict === 'uncertain')) setAdoptionChoice(true)
      if (discover && next.instruments.length > 0) {
        const discoveryController = new AbortController()
        discoveryControllerRef.current = discoveryController
        setDiscoveryMessage('Contacting configured providers for unverified candidates…')
        applyDiscovery(next, generation, discoveryController)
      } else if (discover) setDiscoveryMessage('No active instruments require provider discovery.')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      const message = reason instanceof Error ? reason.message : 'Unable to load market-data setup'
      if (reconcile) { setReconciliation('failed'); setReconciliationRequired(true); setAdoptionChoice(false) }
      if (setup) setReadError(`Could not reload Market Data setup: ${message}. Use Reload setup to try again.`)
      else { setReadError(message); setState('error') }
    })
  }

  useEffect(() => {
    const previousScope = loadedScopeRef.current
    loadedScopeRef.current = { portfolioID, reportingCurrency }
    if (previousScope && previousScope.portfolioID !== portfolioID) {
      controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort()
      clearPrivateState()
      readSetup({ discover: true, resetDrafts: true })
      return
    }
    if (previousScope && previousScope.reportingCurrency !== reportingCurrency && (hasLocalDraftChanges() || savingRef.current || conflictRef.current !== null || scopeChoice)) {
      const pendingWrite = savingRef.current
      generationRef.current += 1
      controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort()
      controllerRef.current = null; discoveryControllerRef.current = null; mutationControllerRef.current = null
      savingRef.current = false
      setSaving(false)
      if (pendingWrite || conflictRef.current === 'uncertain') {
        setConflict('uncertain'); conflictRef.current = 'uncertain'; setReconciliationRequired(true)
        setSaveError('The mapping scope changed while a save was pending. The server outcome is unknown; reload setup before trying again.')
      }
      setScopeChoice(true)
      return
    }
    if (previousScope && previousScope.reportingCurrency !== reportingCurrency && reconciliationRequired) {
      controllerRef.current?.abort(); discoveryControllerRef.current?.abort()
      readSetup({ discover: true, resetDrafts: true, reconcile: true })
      return
    }
    readSetup({ discover: true, resetDrafts: true })
  }, [portfolioID, reportingCurrency])

  useEffect(() => () => {
    mountedRef.current = false
    generationRef.current += 1
    controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort()
    controllerRef.current = null; discoveryControllerRef.current = null; mutationControllerRef.current = null
    savingRef.current = false
    clearPrivateState()
  }, [])

  const editDraft = (id: string, field: keyof Draft, value: string | boolean) => {
    setDrafts((current) => {
      const existing = current[id]
      if (!existing) return current
      const identityEdit = field === 'provider' || field === 'providerSymbol' || field === 'quoteCurrency'
      return { ...current, [id]: { ...existing, [field]: value, touched: true, ...(identityEdit ? { confirmed: false, suggested: false } : {}) } }
    })
  }

  const manualUpdate = (id: string, field: 'provider' | 'providerSymbol' | 'provenance', value: string) => {
    setDrafts((current) => {
      const existing = current[id]
      if (!existing) return current
      return { ...current, [id]: { ...existing, [field]: value, touched: true, ...(field === 'provider' || field === 'providerSymbol' ? { suggested: false, confirmed: false, provenance: 'Manual entry — owner must verify the exact provider identity.' } : field === 'provenance' ? { suggested: false, confirmed: false } : {}) } }
    })
  }

  const entries = useMemo(() => setup?.instruments || [], [setup])
  const mappings = entries.map((instrument): MappingBatchItem | null => {
    const draft = drafts[instrument.instrument_id]
    if (!draft?.provider?.trim() && !draft?.providerSymbol?.trim() && !draft?.provenance?.trim()) return null
    const quoteCurrency = canonicalQuoteCurrency(draft?.quoteCurrency)
    if (!draft?.provider?.trim() || !draft.providerSymbol.trim() || !draft.provenance.trim() || !draft.confirmed || !quoteCurrency || quoteCurrency !== instrument.currency.trim().toUpperCase()) return null
    return { instrument_id: instrument.instrument_id, provider: draft.provider, provider_symbol: draft.providerSymbol, quote_currency: quoteCurrency, provenance: draft.provenance, confirm_unverified: draft.confirmed }
  }).filter((item): item is MappingBatchItem => item !== null)
  const confirmedCount = mappings.length
  const firstIncomplete = entries.find((instrument) => !mappings.some((mapping) => mapping.instrument_id === instrument.instrument_id))
  const firstIncompleteLabel = firstIncomplete ? `${firstIncomplete.symbol} (${firstIncomplete.description || firstIncomplete.currency})` : ''
  const firstIncompleteDraft = firstIncomplete ? drafts[firstIncomplete.instrument_id] : undefined
  const firstIncompleteQuote = firstIncompleteDraft ? canonicalQuoteCurrency(firstIncompleteDraft.quoteCurrency) : null
  const quoteCurrencyMismatch = firstIncomplete && firstIncompleteQuote && firstIncompleteQuote !== firstIncomplete.currency.trim().toUpperCase()
  const saveDisabledReason = entries.length === 0 ? 'No active instruments to map. Add a holding in Holdings first.' : firstIncomplete ? quoteCurrencyMismatch ? `Quote currency for ${firstIncompleteLabel} must match the instrument currency ${firstIncomplete.currency}.` : `Finish mapping for ${firstIncompleteLabel}. Finish ${firstIncompleteLabel} before saving: choose a provider, enter the exact identifier, provide provenance, use a valid quote currency, and confirm the mapping.` : ''
  const canSave = entries.length > 0 && mappings.length === entries.length && !saving && !reconciliationRequired && conflict === null
  const focusNextUnfinished = () => {
    const row = firstIncomplete && rowRefs.current[firstIncomplete.instrument_id]
    row?.querySelector<HTMLElement>('select, input:not([type="hidden"]), button')?.focus()
  }
  const reloadForConflict = () => { setAdoptionChoice(false); readSetup({ discover: false, reconcile: true }) }
  const revealFocusedMappingControl = (event: FocusEvent<HTMLElement>) => {
    const target = event.target as HTMLElement
    if (!target.matches('button, input, select, textarea, [tabindex]') || target.closest('.mapping-identity-cell')) return
    const region = event.currentTarget
    const identity = region.querySelector<HTMLElement>('.mapping-identity-cell')
    const reveal = () => {
      if (document.activeElement !== target) return
      const regionRect = region.getBoundingClientRect()
      const targetRect = target.getBoundingClientRect()
      const leftBoundary = regionRect.left + (identity?.getBoundingClientRect().width || 0) + 6
      const rightBoundary = regionRect.right - 6
      if (targetRect.left < leftBoundary) region.scrollLeft -= leftBoundary - targetRect.left
      else if (targetRect.right > rightBoundary) region.scrollLeft += targetRect.right - rightBoundary
    }
    reveal()
    window.requestAnimationFrame(() => { reveal(); window.requestAnimationFrame(reveal) })
    window.setTimeout(reveal, 0)
  }

  const save = async () => {
    if (!setup || savingRef.current || !canSave) return
    const generation = generationRef.current
    const saveMappings = mappings
    const expectedIDs = new Set(entries.map((instrument) => instrument.instrument_id))
    const controller = new AbortController()
    let reloadAfterSave = false
    mutationControllerRef.current?.abort(); mutationControllerRef.current = controller
    savingRef.current = true; setSaving(true); setSaveError(''); setReadError('')
    try {
      const result = await saveMarketMappings(portfolioID, setup.etag, saveMappings, csrfToken, controller.signal)
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID) return
      const outcomeIDs = result.outcomes.map((outcome) => outcome.instrument_id)
      if (outcomeIDs.length !== expectedIDs.size || new Set(outcomeIDs).size !== outcomeIDs.length || outcomeIDs.some((id) => !expectedIDs.has(id))) throw new APIError(200, 'Invalid mapping batch response: stale instrument scope.')
      const savedByInstrument = new Map(result.outcomes.flatMap((outcome) => outcome.mapping ? [[outcome.instrument_id, outcome.mapping] as const] : []))
      setDrafts((current) => Object.fromEntries(Object.entries(current).map(([instrumentID, draft]) => {
        const mapping = savedByInstrument.get(instrumentID)
        return [instrumentID, mapping ? { ...draft, provider: mapping.provider, providerSymbol: mapping.provider_symbol, quoteCurrency: mapping.quote_currency, provenance: mapping.provenance, confirmed: true, suggested: false, touched: false } : draft]
      })))
      setSaveReceipt(`Saved ${result.outcomes.length} mapping${result.outcomes.length === 1 ? '' : 's'} as a new revision.`)
      setConflict(null)
      setReconciliationRequired(true)
      reloadAfterSave = true
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      if (reason instanceof APIError && reason.status === 412) {
        setConflict('stale')
        setSaveError('Mappings were not saved because the setup changed in another window. Reload setup, then choose an explicit draft option.')
      } else {
        const uncertain = !(reason instanceof APIError) || reason.status === 200 || reason.status >= 500
        if (uncertain) {
          setConflict('uncertain')
          conflictRef.current = 'uncertain'
          setReconciliationRequired(true)
          const detail = reason instanceof Error ? ` ${reason.message}` : ''
          setSaveError(`Mapping save outcome is uncertain and not confirmed.${detail} Reload setup to reconcile before trying Save again.`)
          reloadAfterSave = true
        } else {
          setSaveError(reason instanceof Error ? `Mapping save was rejected. Your edits are kept. ${reason.message}` : 'Mapping save was rejected. Your edits are kept.')
        }
      }
    } finally {
      if (generation === generationRef.current) { setSaving(false); savingRef.current = false }
      if (mutationControllerRef.current === controller) mutationControllerRef.current = null
      if (reloadAfterSave && generation === generationRef.current) readSetup({ discover: false, reconcile: true })
    }
  }

  const controlsDisabled = saving || reconciliationRequired || conflict !== null
  const reconciledDraftSetCompatible = entries.length === Object.keys(drafts).length && entries.every((instrument) => Object.prototype.hasOwnProperty.call(drafts, instrument.instrument_id))
  const discardLocalDrafts = () => {
    const scopeChanged = scopeChoice
    if (setup) setDrafts(draftSeeds(setup))
    setScopeChoice(false)
    if (conflict !== null || reconciliationRequired) {
      setAdoptionChoice(false)
      setSaveError('Local mapping values were discarded. Reload setup to validate the current revision before saving again.')
      readSetup({ discover: false, reconcile: true })
      return
    }
    setAdoptionChoice(false); setConflict(null); conflictRef.current = null; setReconciliationRequired(false); setReconciliation('none'); setSaveError('')
    if (scopeChanged) readSetup({ discover: true, resetDrafts: true })
  }
  return <section className="market-data-card" aria-labelledby="market-data-title">
    <div className="section-heading"><div><span className="eyebrow">One-time setup, revisioned</span><h2 id="market-data-title" tabIndex={-1}>Market data</h2></div><span className="muted">Provider keys authorize access; mappings identify exact listings.</span></div>
    <p className="muted">Mappings are explicit and human-reviewed. They are not required again for each refresh. International listings, ADRs and crypto IDs must use the exact provider identifier; the dashboard never guesses from a holding symbol.</p>
    <div className="provider-status" aria-label="Provider readiness">{setup?.providers.map((provider) => <span className={`provider-chip ${provider.configured ? 'ready' : 'not-ready'} `} key={provider.name}>{provider.name}: {provider.configured ? 'ready' : 'not configured'}</span>)}</div>
    {scopeChoice && <div className="notice" role="alertdialog" aria-labelledby="mapping-scope-title"><strong id="mapping-scope-title">Market Data scope changed while you were editing.</strong><span className="subline">Keep editing retains your exact in-memory draft. Discard clears it; if a save was pending, discarding does not undo a server write.</span><div className="market-actions"><button type="button" onClick={() => setScopeChoice(false)}>Keep editing</button><button type="button" onClick={discardLocalDrafts}>Discard mapping edits</button></div></div>}
    {state === 'loading' && <p role="status" aria-live="polite">Loading market-data setup…</p>}
    {state === 'error' && <p className="notice" role="status">Unable to load market-data setup. <button type="button" onClick={() => readSetup({ discover: true })}>Try again</button></p>}
    {readError && <p className="notice error" role="alert" aria-live="polite">{readError} <button type="button" aria-label="Reload setup" onClick={() => readSetup({ discover: false, reconcile: reconciliationRequired || conflict !== null, acknowledge: !reconciliationRequired && conflict === null })}>Retry</button></p>}
    {state === 'ready' && <>
      {entries.length === 0 ? <p className="empty-state"><strong>No active instruments to map.</strong> <span>Add a holding in Holdings first.</span></p> : <>
        <div className="mapping-progress" aria-live="polite"><strong>{confirmedCount} of {entries.length} mappings confirmed.</strong> {!firstIncomplete && <span>All active instruments are ready for one atomic save.</span>} {firstIncomplete && <button type="button" onClick={focusNextUnfinished} disabled={controlsDisabled} aria-label="Next unfinished mapping">Next unfinished mapping</button>}</div>
        <div className="table-scroll horizontal-scroll-region market-data-table-scroll" tabIndex={0} role="region" aria-label="Market data mappings table horizontal scroll region" aria-describedby="market-data-table-scroll-help" onFocusCapture={revealFocusedMappingControl}><p id="market-data-table-scroll-help" className="visually-hidden">Use horizontal scrolling to view all Market Data mapping columns. The sticky Instrument column identifies each mapping while you scan.</p><table className="market-data-table"><caption>Exact provider mapping setup for every active portfolio instrument</caption><thead><tr><th className="mapping-identity-cell" scope="col">Instrument</th><th scope="col">Provider</th><th scope="col">Exact provider symbol or ID</th><th scope="col">Quote currency</th><th scope="col">Provenance / confirmation</th></tr></thead><tbody>{entries.map((instrument) => {
          const draft = drafts[instrument.instrument_id]
          const available = setup?.providers.filter((provider) => provider.name !== 'open_exchange_rates' && (provider.configured || provider.public)) || []
          const instrumentLabel = `${instrument.symbol} (${instrument.description || instrument.currency})`
          const quoteCurrency = draft?.quoteCurrency ?? ''
          const quoteCurrencyID = `quote-currency-${instrument.instrument_id}`
          const quoteCurrencyValid = canonicalQuoteCurrency(quoteCurrency) !== null
          return <tr key={instrument.instrument_id} ref={(row) => { rowRefs.current[instrument.instrument_id] = row }}><th className="mapping-identity-cell" scope="row"><strong>{instrument.symbol}</strong><span className="subline">{instrument.description || 'Description unavailable'} · {instrument.currency}</span>{instrument.mappings.length > 0 && !draft?.touched ? <span className="subline">Current / confirmed: {instrument.mappings.map((mapping) => `${mapping.provider} / ${mapping.provider_symbol}`).join(', ')}</span> : <><span className="subline">{draft?.guidance || 'No mapping suggestion is available.'}</span>{draft?.suggested && <span className="subline">Suggested by {draft.provider} — unverified. Verify provider-reported identity before confirming.</span>}{draft?.candidates && draft.candidates.length > 0 && <fieldset className="candidate-list"><legend>Provider candidates — choose explicitly</legend>{draft.candidates.map((candidate) => <label className="candidate-choice" key={`${candidate.provider}:${candidate.identifier}`}><input type="radio" name={`candidate-${instrument.instrument_id}`} disabled={controlsDisabled} checked={draft.provider === candidate.provider && draft.providerSymbol === candidate.identifier} onChange={() => setDrafts((current) => ({ ...current, [instrument.instrument_id]: { ...current[instrument.instrument_id], provider: candidate.provider, providerSymbol: candidate.identifier, provenance: `Selected ${candidate.provider} candidate — unverified. Provider-reported identity: ${candidateEvidence(candidate)}`, confirmed: false, suggested: false, touched: true } }))} /> <span>{candidate.provider}: {candidateEvidence(candidate)}</span></label>)}</fieldset>}</>}</th><td><select aria-label={`Provider for ${instrumentLabel}`} value={draft?.provider || ''} disabled={controlsDisabled} onChange={(event) => manualUpdate(instrument.instrument_id, 'provider', event.target.value)}><option value="">Choose exact provider</option>{available.map((provider) => <option key={provider.name} value={provider.name}>{provider.name}</option>)}</select></td><td><input aria-label={`Exact provider identifier for ${instrumentLabel}`} placeholder="Exact listing / coin ID" value={draft?.providerSymbol || ''} disabled={controlsDisabled} onChange={(event) => manualUpdate(instrument.instrument_id, 'providerSymbol', event.target.value)} /></td><td><input id={quoteCurrencyID} aria-label={`Quote currency for ${instrumentLabel}`} aria-invalid={!quoteCurrencyValid} aria-describedby={`${quoteCurrencyID}-help`} value={quoteCurrency} disabled={controlsDisabled} onChange={(event) => editDraft(instrument.instrument_id, 'quoteCurrency', event.target.value)} /><span className="subline" id={`${quoteCurrencyID}-help`}>{quoteCurrencyValid ? 'Three-letter uppercase currency code.' : quoteCurrencyError(quoteCurrency)}</span></td><td><input aria-label={`Provenance for ${instrumentLabel}`} placeholder="Where you verified it" value={draft?.provenance || ''} disabled={controlsDisabled} onChange={(event) => manualUpdate(instrument.instrument_id, 'provenance', event.target.value)} /><label className="checkbox-label"><input type="checkbox" disabled={controlsDisabled} checked={draft?.confirmed || false} onChange={(event) => editDraft(instrument.instrument_id, 'confirmed', event.target.checked)} /> <span>I confirmed this exact identifier for {instrumentLabel}</span></label></td></tr>
        })}</tbody></table></div>
        <div className="market-actions"><button type="button" onClick={save} disabled={!canSave} aria-describedby="save-disabled-reason">Save all mappings</button>{saving && <span role="status" aria-live="polite">Saving mappings…</span>}</div>
        <p id="save-disabled-reason" className="muted">{controlsDisabled && saving ? 'Save is paused while the pending mapping request completes.' : controlsDisabled && reconciliation === 'pending' ? 'Save is disabled while the validated GET reconciliation completes; no second PUT will be sent automatically.' : controlsDisabled && reconciliation === 'failed' ? 'Save is disabled because reconciliation failed. Retry Reload setup for a validated current read; no second PUT will be sent automatically.' : controlsDisabled && conflict === 'uncertain' ? 'Save is disabled until the validated GET is complete and you explicitly choose whether to keep or discard the draft; no second PUT will be sent automatically.' : controlsDisabled && conflict === 'stale' ? 'Save is disabled until the changed setup is reconciled and a draft choice is made.' : saveDisabledReason || 'All active mappings are complete and confirmed.'}</p>
        <p className="muted">Save validates the whole batch before committing. Every row needs an exact editable identifier and explicit confirmation; unverified suggestions remain local until you confirm them. A stale setup is rejected rather than overwriting another revision.</p>
      </>}
      {(conflict === 'stale' || conflict === 'uncertain') && adoptionChoice && reconciliation === 'ready' && <div className="market-actions" role="group" aria-label="Reconciled setup choices"><button type="button" disabled={!reconciledDraftSetCompatible} onClick={() => { setAdoptionChoice(false); setConflict(null); conflictRef.current = null; setReconciliationRequired(false); setReconciliation('none'); setSaveError('Mappings remain local and unsaved. The validated current setup revision is active; review the rows before saving again.') }}>Keep editing with current setup</button><button type="button" onClick={() => { setAdoptionChoice(false); readSetup({ discover: false, adoptDrafts: true }) }}>Discard edits and reload</button>{!reconciledDraftSetCompatible && <span className="subline">The active instrument set changed. Discard these edits and reload before saving.</span>}</div>}
    </>}
    {discoveryMessage && <p className="notice" role="status" aria-live="polite">{discoveryMessage}</p>}
    {saveReceipt && <div className="notice success" role="status" aria-live="polite"><strong>Mappings saved</strong><span className="subline">{saveReceipt} Prices have not been refreshed by this setup save.</span></div>}
    {saveError && <p className="notice error" role="alert" aria-live="polite">{saveError}{conflict === 'stale' && <> {' '}<button type="button" onClick={reloadForConflict}>Reload setup</button></>}{conflict === 'uncertain' && <> {' '}<button type="button" onClick={() => readSetup({ discover: false, reconcile: true })}>Reload setup</button></>}</p>}
  </section>
}