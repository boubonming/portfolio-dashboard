import { useEffect, useRef, useState } from 'react'
import { APIError, getMarketRefreshSchedule, saveMarketRefreshSchedule, type MarketRefreshSchedule } from './api'

export type SchedulePreset = 'daily' | 'weekdays' | 'custom'

export function scheduleCronForPreset(preset: Exclude<SchedulePreset, 'custom'>, time: string): string {
  const [hour, minute] = time.split(':')
  return preset === 'daily' ? `${Number(minute)} ${Number(hour)} * * *` : `${Number(minute)} ${Number(hour)} * * 1-5`
}

export function schedulePresetFromCron(cron: string): SchedulePreset {
  const fields = cron.trim().split(/\s+/)
  if (fields.length === 5 && fields[2] === '*' && fields[3] === '*' && fields[4] === '*') return 'daily'
  if (fields.length === 5 && fields[2] === '*' && fields[3] === '*' && fields[4] === '1-5') return 'weekdays'
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

export default function SettingsPanel({ portfolioID, csrfToken, onUnauthorized, onReload }: SettingsPanelProps) {
  const [schedule, setSchedule] = useState<MarketRefreshSchedule | null>(null)
  const [schedulePreset, setSchedulePreset] = useState<SchedulePreset>('daily')
  const [scheduleTime, setScheduleTime] = useState('05:00')
  const [scheduleCron, setScheduleCron] = useState('0 5 * * *')
  const [scheduleEnabled, setScheduleEnabled] = useState(false)
  const [scheduleTimezone, setScheduleTimezone] = useState('Asia/Singapore')
  const [scheduleCurrency, setScheduleCurrency] = useState('')
  const [scheduleSaving, setScheduleSaving] = useState(false)
  const [scheduleMessage, setScheduleMessage] = useState('')
  const [scheduleError, setScheduleError] = useState('')
  const controllerRef = useRef<AbortController | null>(null)
  const mutationRef = useRef<AbortController | null>(null)
  const scheduleSavingRef = useRef(false)
  const generationRef = useRef(0)
  const scopeRef = useRef(portfolioID)
  const unauthorizedRef = useRef(onUnauthorized)
  const reloadRef = useRef(onReload)
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

  const load = (initialScheduleError = '') => {
    const generation = ++generationRef.current
    controllerRef.current?.abort()
    mutationRef.current?.abort()
    controllerRef.current = null
    mutationRef.current = null
    scheduleSavingRef.current = false
    setScheduleSaving(false)
    const controller = new AbortController()
    controllerRef.current = controller
    setSchedule(null)
    setScheduleError(initialScheduleError)
    setScheduleMessage('')
    getMarketRefreshSchedule(portfolioID, controller.signal).then((next) => {
      if (controller.signal.aborted || generation !== generationRef.current || next.portfolio_id !== scopeRef.current) return
      const preset = schedulePresetFromCron(next.cron)
      const fields = next.cron.trim().split(/\s+/)
      setSchedule(next)
      setScheduleEnabled(next.enabled)
      setScheduleCron(next.cron)
      setSchedulePreset(preset)
      setScheduleTimezone(next.timezone)
      setScheduleCurrency(next.reporting_currency)
      if (preset !== 'custom' && fields.length === 5) setScheduleTime(`${fields[1].padStart(2, '0')}:${fields[0].padStart(2, '0')}`)
      setScheduleError('')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      if (reason instanceof APIError && reason.status === 503) {
        setScheduleError('Automatic refresh is inactive because runtime recovery is unresolved. Retry or reconcile the schedule with the operator before relying on it.')
        return
      }
      setScheduleError(reason instanceof Error ? reason.message : 'Unable to load the refresh schedule')
    })
  }

  useEffect(() => {
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

  const changeSchedulePreset = (preset: SchedulePreset) => {
    setSchedulePreset(preset)
    if (preset !== 'custom' && validScheduleTime(scheduleTime)) setScheduleCron(scheduleCronForPreset(preset, scheduleTime))
  }

  const changeScheduleTime = (time: string) => {
    setScheduleTime(time)
    if (schedulePreset !== 'custom' && validScheduleTime(time)) setScheduleCron(scheduleCronForPreset(schedulePreset, time))
  }

  const saveSchedule = async () => {
    if (!schedule || scheduleSavingRef.current) return
    if (schedule.degraded) {
      setScheduleError('Automatic refresh is inactive while runtime recovery is unresolved. Retry or reconcile the schedule before saving settings.')
      return
    }
    if (!validScheduleTime(scheduleTime) && schedulePreset !== 'custom') {
      setScheduleError('Enter a local time between 00:00 and 23:59.')
      return
    }
    if (!/^[A-Za-z][A-Za-z0-9_+./-]{0,63}$/.test(scheduleTimezone)) {
      setScheduleError('Enter a valid bounded IANA timezone.')
      return
    }
    if (scheduleCurrency !== '' && !/^[A-Z]{3}$/.test(scheduleCurrency)) {
      setScheduleError('Reporting currency must be blank or three uppercase letters.')
      return
    }
    if (scheduleEnabled && (scheduleCron.trim() === '' || scheduleCron.trim().split(/\s+/).length !== 5)) {
      setScheduleError('Enabled schedules require a standard five-field cron expression.')
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
      setScheduleMessage('Schedule saved.')
      setScheduleError('')
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) {
        expireSession()
        return
      }
      if (reason instanceof APIError && reason.status === 412) {
        const staleMessage = 'This schedule changed in another window. Reloading the latest settings before saving again.'
        setScheduleError(staleMessage)
        setScheduleMessage('')
        reloadRef.current()
        load(staleMessage)
      } else if (reason instanceof APIError && reason.status === 503) {
        setScheduleError('Automatic refresh is inactive because runtime recovery is unresolved. Retry or reconcile the schedule with the operator before saving.')
        setScheduleMessage('')
      } else {
        setScheduleError(reason instanceof Error ? reason.message : 'Unable to save the schedule')
      }
    } finally {
      if (generation === generationRef.current) {
        setScheduleSaving(false)
        scheduleSavingRef.current = false
      }
      if (mutationRef.current === controller) mutationRef.current = null
    }
  }

  return <section className="settings-surface" aria-labelledby="market-refresh-schedule-title">
    <section className="schedule-card" aria-labelledby="market-refresh-schedule-title">
      <div className="section-heading"><div><span className="eyebrow">Durable automation</span><h2 id="market-refresh-schedule-title">Market refresh schedule</h2></div><span className="muted">Portfolio: {portfolioID}</span></div>
      {!schedule && <p role="status" aria-live="polite">Loading schedule…</p>}
      {!schedule && scheduleError && <p className="notice error" role="alert" aria-live="assertive">{scheduleError} <button type="button" onClick={() => load()}>Reload</button></p>}
      {schedule && <>
        {schedule.degraded && <p id="market-refresh-schedule-status" className="notice error" role="alert" aria-live="assertive">Automatic refresh is inactive: the scheduler is degraded. Retry or reconcile the schedule with the operator before relying on these settings.</p>}
        <label><input type="checkbox" checked={scheduleEnabled} disabled={schedule.degraded || scheduleSaving} onChange={(event) => setScheduleEnabled(event.target.checked)} aria-describedby={schedule.degraded ? 'market-refresh-schedule-status' : undefined} /> Enable scheduled refreshes</label>
        <div className="schedule-grid">
          <label>Preset<select value={schedulePreset} disabled={schedule.degraded || scheduleSaving} onChange={(event) => changeSchedulePreset(event.target.value as SchedulePreset)}><option value="daily">Daily</option><option value="weekdays">Weekdays</option><option value="custom">Custom cron</option></select></label>
          {schedulePreset !== 'custom' ? <label>Local time<input aria-label="Schedule local time" type="time" value={scheduleTime} disabled={schedule.degraded || scheduleSaving} onChange={(event) => changeScheduleTime(event.target.value)} /></label> : <label>Five-field cron<input aria-label="Custom five-field cron" value={scheduleCron} disabled={schedule.degraded || scheduleSaving} onChange={(event) => setScheduleCron(event.target.value)} placeholder="0 5 * * *" /></label>}
          <label>IANA timezone<input aria-label="Schedule timezone" value={scheduleTimezone} disabled={schedule.degraded || scheduleSaving} maxLength={64} onChange={(event) => setScheduleTimezone(event.target.value)} /></label>
          <label>Reporting currency (optional)<input aria-label="Reporting currency" value={scheduleCurrency} disabled={schedule.degraded || scheduleSaving} maxLength={3} onChange={(event) => setScheduleCurrency(event.target.value.toUpperCase())} placeholder="Inherit" /></label>
        </div>
        <p className="muted" aria-label="Schedule source">Schedule source: {schedule.source} — {schedule.source === 'saved' ? 'Saved in the portfolio database.' : 'Using deployment bootstrap defaults until you save a portfolio setting.'} {schedule.source === 'environment' && 'Daily example: 0 5 * * * in Asia/Singapore.'}</p>
        <p className="muted" role="status" aria-live="polite" aria-label="Schedule runtime status">Schedule runtime status: {schedule.runtime_status}{schedule.effective ? ' (effective)' : ' (not effective)'}</p>
        <p className="muted" aria-label="Schedule revision">Schedule revision: {schedule.revision} · ETag: {schedule.etag}</p>
        <button type="button" onClick={saveSchedule} disabled={scheduleSaving || schedule.degraded}>{scheduleSaving ? 'Saving schedule…' : 'Save schedule'}</button>
        {scheduleMessage && <p role="status" aria-live="polite">{scheduleMessage}</p>}
        {scheduleError && <p className="notice error" role="alert" aria-live="polite">{scheduleError} <button type="button" onClick={() => load()}>Reload</button></p>}
      </>}
    </section>
  </section>
}
