import { useEffect, useRef, useState } from 'react'
import { APIError, getMarketRefreshSchedule, saveMarketRefreshSchedule, type MarketRefreshSchedule } from './api'

export type SchedulePreset = 'daily' | 'weekdays' | 'custom'

const DEFAULT_SCHEDULE_TIME = '05:00'
const DEFAULT_SCHEDULE_CRON = '0 5 * * *'

export function scheduleCronForPreset(preset: Exclude<SchedulePreset, 'custom'>, time: string): string {
  const [hour, minute] = time.split(':')
  return preset === 'daily' ? `${Number(minute)} ${Number(hour)} * * *` : `${Number(minute)} ${Number(hour)} * * 1-5`
}

/** Only canonical, single minute/hour fields are represented by a preset. */
export function schedulePresetFromCron(cron: string): SchedulePreset {
  const fields = cron.trim().split(/\s+/)
  const simpleTime = fields.length === 5 && /^\d+$/.test(fields[0]) && /^\d+$/.test(fields[1]) && Number(fields[0]) <= 59 && Number(fields[1]) <= 23 && fields[2] === '*' && fields[3] === '*'
  if (simpleTime && fields[4] === '*') return 'daily'
  if (simpleTime && fields[4] === '1-5') return 'weekdays'
  return 'custom'
}

export function validScheduleTime(value: string): boolean {
  return /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value)
}

type SettingsPanelProps = {
  portfolioID: string
  csrfToken?: string
  onUnauthorized: () => void
  onReload: () => void
}

type LoadOptions = {
  preserveDraft?: boolean
  adopt?: boolean
  retainError?: boolean
}

function scheduleTimeFromCron(cron: string): string {
  const [minute, hour] = cron.trim().split(/\s+/)
  return `${String(Number(hour)).padStart(2, '0')}:${String(Number(minute)).padStart(2, '0')}`
}

function scheduleStatusLabel(schedule: MarketRefreshSchedule, runtimeRecoveryRequired = false): string {
  if (runtimeRecoveryRequired || schedule.degraded || schedule.runtime_status === 'degraded') return 'Inactive — recovery required'
  if (schedule.runtime_status === 'disabled' || !schedule.enabled) return 'Disabled'
  if (!schedule.effective) return 'Inactive — recovery required'
  return 'Active'
}

function scheduleSummary(schedule: MarketRefreshSchedule): string {
  if (!schedule.enabled || schedule.runtime_status === 'disabled') return 'Off — choose a schedule before enabling'
  const preset = schedulePresetFromCron(schedule.cron)
  if (preset === 'custom') return `Custom cron ${schedule.cron} in ${schedule.timezone}`
  return `${preset === 'daily' ? 'Daily' : 'Weekdays'} at ${scheduleTimeFromCron(schedule.cron)} in ${schedule.timezone}`
}

const recoveryMessage = 'Automatic refresh is inactive while runtime recovery is unresolved. Reload schedule, or retry or reconcile with the deployment owner before relying on these settings.'

export default function SettingsPanel({ portfolioID, csrfToken, onUnauthorized, onReload }: SettingsPanelProps) {
  const [schedule, setSchedule] = useState<MarketRefreshSchedule | null>(null)
  const [schedulePending, setSchedulePending] = useState(true)
  const [schedulePreset, setSchedulePreset] = useState<SchedulePreset>('daily')
  const [scheduleTime, setScheduleTime] = useState(DEFAULT_SCHEDULE_TIME)
  const [scheduleCron, setScheduleCron] = useState(DEFAULT_SCHEDULE_CRON)
  const [scheduleEnabled, setScheduleEnabled] = useState(false)
  const [scheduleTimezone, setScheduleTimezone] = useState('Asia/Singapore')
  const [scheduleCurrency, setScheduleCurrency] = useState('')
  const [scheduleSaving, setScheduleSaving] = useState(false)
  const [scheduleMessage, setScheduleMessage] = useState('')
  const [scheduleError, setScheduleError] = useState('')
  const [conflictPending, setConflictPending] = useState(false)
  const [reconciliationRequired, setReconciliationRequired] = useState(false)
  // The last read remains useful for draft recovery, but is not current runtime
  // truth after an uncertain/degraded write until a healthy GET reconciles it.
  const [runtimeRecoveryRequired, setRuntimeRecoveryRequired] = useState(false)
  const controllerRef = useRef<AbortController | null>(null)
  const mutationRef = useRef<AbortController | null>(null)
  const scheduleSavingRef = useRef(false)
  const generationRef = useRef(0)
  const scopeRef = useRef(portfolioID)
  const unauthorizedRef = useRef(onUnauthorized)
  const reloadRef = useRef(onReload)
  const scheduleErrorRef = useRef<HTMLParagraphElement | null>(null)
  const timeRef = useRef<HTMLInputElement | null>(null)
  const cronRef = useRef<HTMLInputElement | null>(null)
  const timezoneRef = useRef<HTMLInputElement | null>(null)
  const currencyRef = useRef<HTMLInputElement | null>(null)
  scopeRef.current = portfolioID
  unauthorizedRef.current = onUnauthorized
  reloadRef.current = onReload

  const expireSession = () => {
    generationRef.current += 1
    controllerRef.current?.abort()
    mutationRef.current?.abort()
    controllerRef.current = null
    mutationRef.current = null
    scheduleSavingRef.current = false
    setScheduleSaving(false)
    unauthorizedRef.current()
  }

  const applyCanonicalSchedule = (next: MarketRefreshSchedule) => {
    const preset = next.enabled ? schedulePresetFromCron(next.cron) : 'daily'
    setScheduleEnabled(next.enabled)
    setScheduleCron(next.enabled ? next.cron : DEFAULT_SCHEDULE_CRON)
    setSchedulePreset(preset)
    setScheduleTimezone(next.timezone)
    setScheduleCurrency(next.reporting_currency)
    if (next.enabled && preset !== 'custom') setScheduleTime(scheduleTimeFromCron(next.cron))
    else if (!next.enabled) setScheduleTime(DEFAULT_SCHEDULE_TIME)
  }

  const load = (initialScheduleError = '', options: LoadOptions = {}) => {
    const generation = ++generationRef.current
    controllerRef.current?.abort()
    mutationRef.current?.abort()
    controllerRef.current = null
    mutationRef.current = null
    scheduleSavingRef.current = false
    setScheduleSaving(false)
    const controller = new AbortController()
    controllerRef.current = controller
    if (!options.preserveDraft) setSchedule(null)
    setSchedulePending(true)
    setScheduleError(initialScheduleError)
    setScheduleMessage('')
    getMarketRefreshSchedule(portfolioID, controller.signal).then((next) => {
      if (controller.signal.aborted || generation !== generationRef.current || next.portfolio_id !== scopeRef.current) return
      setSchedule(next)
      setSchedulePending(false)
      setRuntimeRecoveryRequired(false)
      if (!options.preserveDraft || options.adopt) applyCanonicalSchedule(next)
      if (options.adopt) {
        setConflictPending(false)
        setReconciliationRequired(false)
        setScheduleMessage('Saved schedule reloaded; the canonical settings are now adopted.')
      }
      if (!options.retainError) setScheduleError(next.degraded ? `${scheduleStatusLabel(next)}: ${recoveryMessage}` : '')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      setSchedulePending(false)
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      setScheduleError(reason instanceof APIError && reason.status === 503 ? recoveryMessage : reason instanceof Error ? reason.message : 'Unable to load the refresh schedule')
    })
  }

  useEffect(() => {
    // A portfolio replacement is a new authority boundary. Do not carry a
    // conflict/reconciliation lock or an old runtime uncertainty into it.
    setConflictPending(false)
    setReconciliationRequired(false)
    setRuntimeRecoveryRequired(false)
    setScheduleError('')
    setScheduleMessage('')
    load()
    return () => {
      generationRef.current += 1
      controllerRef.current?.abort()
      mutationRef.current?.abort()
      scheduleSavingRef.current = false
      controllerRef.current = null
      mutationRef.current = null
    }
  }, [portfolioID])

  const focusError = (message: string, field?: React.RefObject<HTMLInputElement | null>) => {
    setScheduleError(message)
    setScheduleMessage('')
    if (field) field.current?.focus()
    else setTimeout(() => scheduleErrorRef.current?.focus(), 0)
  }

  const changeSchedulePreset = (preset: SchedulePreset) => {
    setSchedulePreset(preset)
    if (preset !== 'custom' && validScheduleTime(scheduleTime)) setScheduleCron(scheduleCronForPreset(preset, scheduleTime))
  }

  const changeScheduleTime = (time: string) => {
    setScheduleTime(time)
    if (schedulePreset !== 'custom' && validScheduleTime(time)) setScheduleCron(scheduleCronForPreset(schedulePreset, time))
  }

  const saveSchedule = async () => {
    if (!schedule || scheduleSavingRef.current || conflictPending || reconciliationRequired) return
    if (schedule.degraded) {
      focusError(recoveryMessage)
      return
    }
    if (!validScheduleTime(scheduleTime) && schedulePreset !== 'custom') {
      focusError('Enter a local time between 00:00 and 23:59.', timeRef)
      return
    }
    if (!/^[A-Za-z][A-Za-z0-9_+./-]{0,63}$/.test(scheduleTimezone)) {
      focusError('Enter a valid IANA timezone, such as Asia/Singapore.', timezoneRef)
      return
    }
    if (scheduleCurrency !== '' && !/^[A-Z]{3}$/.test(scheduleCurrency)) {
      focusError('Reporting currency must be blank or three uppercase letters.', currencyRef)
      return
    }
    if (scheduleEnabled && (scheduleCron.trim() === '' || scheduleCron.trim().split(/\s+/).length !== 5)) {
      focusError('Enter a standard five-field cron expression.', cronRef)
      return
    }
    const generation = generationRef.current
    const controller = new AbortController()
    scheduleSavingRef.current = true
    mutationRef.current = controller
    setScheduleSaving(true)
    setScheduleError('')
    setScheduleMessage('Saving schedule…')
    try {
      const next = await saveMarketRefreshSchedule(portfolioID, { enabled: scheduleEnabled, cron: scheduleEnabled ? scheduleCron : '', timezone: scheduleTimezone, reporting_currency: scheduleCurrency }, schedule.etag, csrfToken, controller.signal)
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current !== portfolioID) return
      setSchedule(next)
      applyCanonicalSchedule(next)
      setConflictPending(false)
      setReconciliationRequired(false)
      setScheduleMessage('Schedule saved.')
      setScheduleError(next.degraded ? recoveryMessage : '')
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) {
        expireSession()
        return
      }
      if (reason instanceof APIError && reason.status === 412) {
        const staleMessage = 'Settings changed elsewhere. Your edits were not saved. Reload saved settings and review before saving again.'
        setConflictPending(true)
        setReconciliationRequired(false)
        focusError(staleMessage)
        reloadRef.current()
        // Read the latest canonical value, but deliberately retain the local draft.
        load(staleMessage, { preserveDraft: true, retainError: true })
      } else if (reason instanceof APIError && reason.status === 400) {
        focusError('Schedule was not accepted. Check the cron expression, timezone and currency. Your edits are kept.')
      } else {
        setReconciliationRequired(true)
        setRuntimeRecoveryRequired(true)
        focusError(reason instanceof APIError && reason.status === 503 ? `${recoveryMessage} The save result is not confirmed.` : 'Schedule save not confirmed. Reload schedule to check the saved setting before saving again.')
      }
    } finally {
      if (generation === generationRef.current) {
        setScheduleSaving(false)
        scheduleSavingRef.current = false
      }
      if (mutationRef.current === controller) mutationRef.current = null
    }
  }

  const retryRead = () => load('', { preserveDraft: Boolean(schedule), adopt: true })

  return <section className="settings-surface" aria-labelledby="market-refresh-schedule-title">
    <section className="schedule-card" aria-labelledby="market-refresh-schedule-title">
      <div className="section-heading"><div><span className="eyebrow">Durable automation</span><h2 id="market-refresh-schedule-title">Market refresh schedule</h2></div><span className="muted">Portfolio: {portfolioID}</span></div>
      {schedulePending && !schedule && <p role="status" aria-live="polite">Loading schedule…</p>}
      {!schedule && !schedulePending && scheduleError && <p ref={scheduleErrorRef} tabIndex={-1} className="notice error" role="alert" aria-live="assertive">{scheduleError} <button type="button" onClick={() => load('')}>Reload schedule</button></p>}
      {schedule && <>
        {schedulePending && <p role="status" aria-live="polite">Reloading schedule…</p>}
        {(schedule.degraded || reconciliationRequired || conflictPending || runtimeRecoveryRequired || scheduleError) && <p ref={scheduleErrorRef} tabIndex={-1} className="notice error" role="alert" aria-live="assertive">{scheduleError || (schedule.degraded || runtimeRecoveryRequired ? `${scheduleStatusLabel(schedule, runtimeRecoveryRequired)}: ${recoveryMessage}` : '')} {conflictPending && <button type="button" onClick={() => load('', { preserveDraft: true, adopt: true })}>Reload saved settings</button>} {!conflictPending && (schedule.degraded || reconciliationRequired || runtimeRecoveryRequired) && <button type="button" onClick={retryRead}>Reload schedule</button>} </p>}
        <p className="schedule-summary" role="status" aria-live="polite" aria-label="Schedule summary"><strong>{scheduleSummary(schedule)}</strong> · Status: {scheduleStatusLabel(schedule, runtimeRecoveryRequired)}. {schedule.reporting_currency ? `Reporting currency: ${schedule.reporting_currency}.` : 'Reporting currency: Portfolio default (blank; not the Holdings selector).'}</p>
        {!schedule.enabled && schedulePreset === 'daily' && scheduleTime === DEFAULT_SCHEDULE_TIME && <p className="muted">Unsaved Daily template: 05:00 in {scheduleTimezone}. It is not the saved schedule until you enable and save it.</p>}
        <label><input type="checkbox" checked={scheduleEnabled} disabled={schedule.degraded || runtimeRecoveryRequired || scheduleSaving || conflictPending || reconciliationRequired} onChange={(event) => setScheduleEnabled(event.target.checked)} aria-describedby={schedule.degraded || runtimeRecoveryRequired ? 'market-refresh-schedule-status' : undefined} /> Enable scheduled refreshes</label>
        <div className="schedule-grid">
          <label>Preset<select aria-label="Preset" value={schedulePreset} disabled={schedule.degraded || runtimeRecoveryRequired || scheduleSaving || conflictPending || reconciliationRequired} onChange={(event) => changeSchedulePreset(event.target.value as SchedulePreset)}><option value="daily">Daily</option><option value="weekdays">Weekdays</option><option value="custom">Custom cron</option></select></label>
          {schedulePreset !== 'custom' ? <label>Local time<input ref={timeRef} aria-label="Schedule local time" type="time" value={scheduleTime} disabled={schedule.degraded || runtimeRecoveryRequired || scheduleSaving || conflictPending || reconciliationRequired} onChange={(event) => changeScheduleTime(event.target.value)} /></label> : <label>Five-field cron<input ref={cronRef} aria-label="Custom five-field cron" value={scheduleCron} disabled={schedule.degraded || runtimeRecoveryRequired || scheduleSaving || conflictPending || reconciliationRequired} onChange={(event) => setScheduleCron(event.target.value)} placeholder="0 5 * * *" /></label>}
          <label>IANA timezone<input ref={timezoneRef} aria-label="Schedule timezone" value={scheduleTimezone} disabled={schedule.degraded || runtimeRecoveryRequired || scheduleSaving || conflictPending || reconciliationRequired} maxLength={64} onChange={(event) => setScheduleTimezone(event.target.value)} /></label>
          <label>Reporting currency (optional)<input ref={currencyRef} aria-label="Reporting currency" value={scheduleCurrency} disabled={schedule.degraded || runtimeRecoveryRequired || scheduleSaving || conflictPending || reconciliationRequired} maxLength={3} onChange={(event) => setScheduleCurrency(event.target.value.toUpperCase())} placeholder="Use portfolio default" /></label>
        </div>
        <p className="muted" aria-label="Schedule source">Schedule source: {schedule.source === 'saved' ? 'Saved in portfolio (saved).' : 'Deployment defaults (environment).'} {schedule.source === 'environment' && 'The fallback is not saved until you choose Save schedule.'}</p>
        <p id="market-refresh-schedule-status" className="muted" role="status" aria-live="polite" aria-label="Schedule runtime status">Schedule runtime status: {scheduleStatusLabel(schedule, runtimeRecoveryRequired)} (technical runtime: {runtimeRecoveryRequired ? 'unconfirmed' : schedule.runtime_status}; effective: {runtimeRecoveryRequired ? 'unconfirmed' : schedule.effective ? 'yes' : 'no'}).</p>
        <details><summary>Technical details</summary><p className="muted schedule-revision" aria-label="Schedule revision">Schedule revision: {schedule.revision} · ETag: <span className="schedule-etag">{schedule.etag}</span></p></details>
        <button type="button" onClick={saveSchedule} disabled={scheduleSaving || schedule.degraded || runtimeRecoveryRequired || conflictPending || reconciliationRequired} aria-describedby={schedule.degraded || runtimeRecoveryRequired || conflictPending || reconciliationRequired ? 'market-refresh-schedule-status' : undefined}>{scheduleSaving ? 'Saving schedule…' : 'Save schedule'}</button>
        {scheduleMessage && <p role="status" aria-live="polite">{scheduleMessage}</p>}
      </>}
    </section>
  </section>
}
