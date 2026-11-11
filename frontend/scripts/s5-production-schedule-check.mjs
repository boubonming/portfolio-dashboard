#!/usr/bin/env node
// S5_APP_URL=http://127.0.0.1:4173/ S5_CDP_ENDPOINT=http://127.0.0.1:9222 node scripts/s5-production-schedule-check.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const appURL = process.env.S5_APP_URL || 'http://127.0.0.1:4173/'
const cdpEndpoint = process.env.S5_CDP_ENDPOINT || 'http://127.0.0.1:9222'
const fixture = await readFile(new URL('./s5-synthetic-fixture.js', import.meta.url), 'utf8')

async function targetWebSocketURL() {
  if (cdpEndpoint.startsWith('ws://') || cdpEndpoint.startsWith('wss://')) return cdpEndpoint
  const response = await fetch(`${cdpEndpoint.replace(/\/$/, '')}/json/list`)
  assert.equal(response.ok, true, `Unable to read CDP targets from ${cdpEndpoint}`)
  const targets = await response.json()
  const page = targets.find((target) => target.type === 'page')
  assert.ok(page?.webSocketDebuggerUrl, 'No page target is available in the configured CDP endpoint')
  return page.webSocketDebuggerUrl
}

const socket = new WebSocket(await targetWebSocketURL())
let nextID = 0
const pending = new Map()
socket.addEventListener('message', (event) => {
  const message = JSON.parse(event.data)
  if (!message.id || !pending.has(message.id)) return
  const { resolve, reject } = pending.get(message.id)
  pending.delete(message.id)
  if (message.error) reject(new Error(`${message.error.code}: ${message.error.message}`))
  else resolve(message.result)
})
await new Promise((resolve, reject) => {
  socket.addEventListener('open', resolve, { once: true })
  socket.addEventListener('error', reject, { once: true })
})
function command(method, params = {}) {
  const id = ++nextID
  socket.send(JSON.stringify({ id, method, params }))
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }))
}
async function evaluate(expression) {
  const result = await command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || 'Browser evaluation failed')
  return result.result?.value
}
async function waitFor(expression, timeout = 7000) {
  const started = Date.now()
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return
    await new Promise((resolve) => setTimeout(resolve, 60))
  }
  throw new Error(`Timed out waiting for ${expression}`)
}
async function settle() { await new Promise((resolve) => setTimeout(resolve, 100)) }
async function keyPress(key, { ctrl = false, shift = false } = {}) {
  const keyData = {
    Enter: { code: 'Enter', keyCode: 13, text: '\r' },
    ' ': { code: 'Space', keyCode: 32, text: ' ' },
    Tab: { code: 'Tab', keyCode: 9 },
    ArrowDown: { code: 'ArrowDown', keyCode: 40 },
    a: { code: 'KeyA', keyCode: 65 },
  }[key] || { code: key, keyCode: 0 }
  const modifiers = ctrl ? 2 : shift ? 8 : 0
  const params = { key, code: keyData.code, modifiers, ...(keyData.keyCode ? { windowsVirtualKeyCode: keyData.keyCode, nativeVirtualKeyCode: keyData.keyCode } : {}) }
  if (keyData.text) Object.assign(params, { text: keyData.text, unmodifiedText: keyData.text })
  await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params })
  await settle()
}
async function focus(selector) {
  assert.equal(await evaluate(`Boolean((document.querySelector(${JSON.stringify(selector)}) || {}).focus)`), true, `Control not found: ${selector}`)
  await evaluate(`document.querySelector(${JSON.stringify(selector)}).focus(); true`)
}
async function activateButton(label) {
  const selector = `button`
  const found = await evaluate(`(() => { const node = [...document.querySelectorAll(${JSON.stringify(selector)})].find((candidate) => candidate.textContent?.trim() === ${JSON.stringify(label)} || candidate.getAttribute('aria-label') === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`)
  assert.equal(found, true, `Button not found: ${label}`)
  await keyPress('Enter')
}
async function activateTab(label) {
  const found = await evaluate(`(() => { const node = [...document.querySelectorAll('[role="tab"]')].find((candidate) => candidate.textContent?.trim() === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`)
  assert.equal(found, true, `Tab not found: ${label}`)
  await keyPress('Enter')
  await waitFor(`Boolean([...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true' && node.textContent?.trim() === ${JSON.stringify(label)}))`)
}
async function activateCheckbox() {
  await focus('input[type="checkbox"]')
  await keyPress(' ')
}
async function editInput(label, value) {
  await focus(`[aria-label=${JSON.stringify(label)}]`)
  await keyPress('a', { ctrl: true })
  await command('Input.insertText', { text: value })
  await settle()
}
async function scheduleState() {
  return evaluate(`(() => ({
    summary: document.querySelector('[aria-label="Schedule summary"]')?.textContent || '',
    status: document.querySelector('[aria-label="Schedule runtime status"]')?.textContent || '',
    source: document.querySelector('[aria-label="Schedule source"]')?.textContent || '',
    loading: [...document.querySelectorAll('[role="status"]')].some((node) => node.textContent?.includes('Loading schedule…')),
    puts: window.__s5?.state.putCalls.length || 0,
    width: { inner: innerWidth, root: document.documentElement.scrollWidth, body: document.body.scrollWidth },
  }))()`)
}
async function reload(width, url = appURL) {
  await command('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: false })
  await command('Page.navigate', { url })
  await waitFor('Boolean(window.__s5 && document.querySelector("button.sign-out"))')
  await waitFor('document.querySelectorAll("[role=tab]").length === 6')
}
async function openSettings({ expectSchedule = true } = {}) {
  await activateTab('Settings')
  if (expectSchedule) await waitFor('Boolean(document.querySelector("[aria-label=\\"Schedule summary\\"]"))')
}
async function assertNoRootOverflow(label) {
  const value = await scheduleState()
  assert.ok(value.width.root <= value.width.inner + 1, `${label} root overflow: ${JSON.stringify(value.width)}`)
  assert.ok(value.width.body <= value.width.inner + 1, `${label} body overflow: ${JSON.stringify(value.width)}`)
  return value
}
async function runWidth(width, detailed) {
  await reload(width)
  await openSettings()
  let state = await assertNoRootOverflow(`${width}px Off`)
  assert.match(state.summary, /Off/)
  assert.match(state.status, /Disabled/)
  assert.match(await evaluate('document.body.textContent'), /Unsaved Daily template/)
  assert.equal(state.puts, 0, 'Opening Settings must not PUT')

  await activateCheckbox()
  await activateButton('Save schedule')
  await waitFor('document.querySelector("[aria-label=\\"Schedule summary\\"]")?.textContent?.includes("Daily")')
  state = await assertNoRootOverflow(`${width}px Active`)
  assert.match(state.status, /Active/)
  assert.equal(state.puts, 1, 'Rapid activation must issue one PUT')

  await reload(width, `${appURL.replace(/\/$/, '')}/?s5degraded=1`)
  await openSettings()
  await waitFor('document.querySelector("[role=alert]")?.textContent?.includes("Inactive")')
  state = await assertNoRootOverflow(`${width}px degraded`)
  assert.match(state.status, /Inactive — recovery required/)
  assert.equal(await evaluate('Boolean(document.querySelector("button[disabled]"))'), true, 'Degraded state must disable controls')

  if (!detailed) return { width, off: true, active: true, degraded: true, overflow: state.width }

  const custom = await evaluate(`({ ...window.__s5.state.schedule, enabled: true, cron: '*/30 5 * * *', timezone: 'Etc/UTC', reporting_currency: 'SGD', revision: 2, updated_at: '2026-01-03T12:00:00.000000000Z', source: 'saved', etag: '"${'c'.repeat(64)}"', effective: true, degraded: false, runtime_status: 'active' })`)
  await evaluate(`window.__s5.configure({ getError: false, nextSchedule: ${JSON.stringify(custom)} }); true`)
  await activateButton('Reload schedule')
  await waitFor('document.querySelector("[aria-label=\\"Schedule summary\\"]")?.textContent?.includes("Custom cron */30 5 * * *")')
  assert.equal(await evaluate('document.querySelector("[aria-label=\\"Custom five-field cron\\"]")?.value'), '*/30 5 * * *')

  await editInput('Schedule timezone', 'America/New_York')
  await evaluate('window.__s5.configure({ conflictOnce: true }); true')
  await activateButton('Save schedule')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === "Reload saved settings"))')
  assert.equal(await evaluate('window.__s5.state.putCalls.length'), 1)
  assert.equal(await evaluate('document.querySelector("[aria-label=\\"Schedule timezone\\"]")?.value'), 'America/New_York')
  await activateButton('Reload saved settings')
  await waitFor('document.querySelector("[aria-label=\\"Schedule timezone\\"]")?.value === "Etc/UTC"')
  assert.equal(await evaluate('window.__s5.state.putCalls.length'), 1, 'Conflict adoption must not auto-PUT')

  await editInput('Schedule timezone', 'Asia/Tokyo')
  await evaluate('window.__s5.configure({ uncertainOnce: true }); true')
  await activateButton('Save schedule')
  await waitFor('document.querySelector("[role=alert]")?.textContent?.includes("save not confirmed")')
  assert.equal(await evaluate('window.__s5.state.putCalls.length'), 2)
  await activateButton('Reload schedule')
  await waitFor('document.querySelector("[aria-label=\\"Schedule timezone\\"]")?.value === "Etc/UTC"')
  assert.equal(await evaluate('window.__s5.state.putCalls.length'), 2, 'Uncertain save recovery must be GET-only')

  await reload(width, `${appURL.replace(/\/$/, '')}/?s5error=1`)
  await openSettings({ expectSchedule: false })
  await waitFor('Boolean(document.querySelector("[role=alert]"))')
  state = await scheduleState()
  assert.equal(state.loading, false, 'Completed GET failure must not remain Loading')
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === "Reload schedule"))'), true)
  return { width, off: true, active: true, custom: true, conflict: true, uncertain: true, error: true, overflow: state.width }
}

await command('Page.enable')
await command('Runtime.enable')
await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
const results = [await runWidth(1280, true), await runWidth(320, false)]
console.log(JSON.stringify({ results, keyboardOnly: true, syntheticFixture: true, noPrivateStorage: await evaluate('localStorage.length === 0 && sessionStorage.length === 0') }, null, 2))
socket.close()
