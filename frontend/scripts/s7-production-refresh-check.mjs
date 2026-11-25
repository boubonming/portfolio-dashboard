#!/usr/bin/env node
// S7_APP_URL=http://127.0.0.1:4173/ S7_CDP_ENDPOINT=http://127.0.0.1:9222 node scripts/s7-production-refresh-check.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const appURL = process.env.S7_APP_URL || 'http://127.0.0.1:4173/'
const cdpEndpoint = process.env.S7_CDP_ENDPOINT || 'http://127.0.0.1:9222'
const fixture = await readFile(new URL('./s7-synthetic-fixture.js', import.meta.url), 'utf8')
async function targetWebSocketURL() {
  if (cdpEndpoint.startsWith('ws://') || cdpEndpoint.startsWith('wss://')) return cdpEndpoint
  const response = await fetch(`${cdpEndpoint.replace(/\/$/, '')}/json/list`)
  assert.equal(response.ok, true)
  const targets = await response.json()
  const page = targets.find((target) => target.type === 'page')
  assert.ok(page?.webSocketDebuggerUrl)
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
  if (message.error) reject(new Error(message.error.message))
  else resolve(message.result)
})
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }) })
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
async function waitFor(expression, timeout = 8000) {
  const started = Date.now()
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return
    await new Promise((resolve) => setTimeout(resolve, 75))
  }
  throw new Error(`Timed out waiting for ${expression}`)
}
async function keyPress(key) {
  const keyCode = { Enter: 13, Tab: 9 }[key]
  const params = { key, code: key, modifiers: 0, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode }
  await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params, ...(key === 'Enter' ? { text: '\r', unmodifiedText: '\r' } : {}) })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params })
  await new Promise((resolve) => setTimeout(resolve, 100))
}
async function focusAndActivateText(label) {
  assert.equal(await evaluate(`(() => { const node = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`), true, `missing button ${label}`)
  await keyPress('Enter')
}
async function focusAndActivateTab(label) {
  assert.equal(await evaluate(`(() => { const node = [...document.querySelectorAll('[role="tab"]')].find((candidate) => candidate.textContent?.trim() === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`), true, `missing tab ${label}`)
  await keyPress('Enter')
}
async function focusAndActivate(selector) {
  assert.equal(await evaluate(`(() => { const node = document.querySelector(${JSON.stringify(selector)}); node?.focus(); return Boolean(node); })()`), true, `missing ${selector}`)
  await keyPress('Enter')
}
async function checkViewport(label) {
  const state = await evaluate('({ width: innerWidth, root: document.documentElement.scrollWidth, body: document.body.scrollWidth, active: document.activeElement?.id || document.activeElement?.textContent?.trim() })')
  assert.ok(state.root <= state.width + 1, `${label} root overflow: ${JSON.stringify(state)}`)
  assert.ok(state.body <= state.width + 1, `${label} body overflow: ${JSON.stringify(state)}`)
  return state
}
await command('Page.enable')
await command('Runtime.enable')
await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
const results = []
for (const width of [1280, 320]) {
  await command('Emulation.setDeviceMetricsOverride', { width, height: 800, deviceScaleFactor: 1, mobile: false })
  await command('Page.navigate', { url: appURL })
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Refresh prices")))')
  await focusAndActivateText('Refresh prices')
  await waitFor('Boolean(document.body.textContent?.includes("Prices refreshed. Complete valuation loaded."))')
  await focusAndActivateText('View refresh details')
  await waitFor('Boolean(document.body.textContent?.includes("Refresh details"))')
  const refreshState = await checkViewport(`${width}px refresh`)
  await command('Page.navigate', { url: `${appURL}?s7-long-diagnostics=1` })
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Refresh prices")))')
  await focusAndActivateText('Refresh prices')
  await waitFor('Boolean(document.body.textContent?.includes("valuation is incomplete"))')
  await focusAndActivateText('View refresh details')
  await waitFor('Boolean(document.body.textContent?.includes("Refresh details"))')
  assert.ok((await evaluate('document.body.textContent || ""')).includes('S7-LONG-LABEL-'), `${width}px long diagnostic label missing`)
  const longDiagnosticState = await checkViewport(`${width}px long refresh diagnostic`)
  await focusAndActivateTab('Overview')
  await waitFor('document.querySelector("#overview-title")?.textContent?.includes("Overview")')
  const overviewState = await checkViewport(`${width}px overview`)
  assert.equal(await evaluate('Boolean(document.querySelector("#overview-title"))'), true)
  await command('Page.navigate', { url: `${appURL}?s7-overview-actions=1` })
  await waitFor('Boolean([...document.querySelectorAll("[role=tab]")].find((node) => node.textContent?.trim() === "Overview"))')
  await focusAndActivateTab('Overview')
  await waitFor('Boolean(document.body.textContent?.includes("No immutable snapshot yet"))')
  await focusAndActivateText('View holdings')
  await waitFor('Boolean(document.querySelector("#holdings-title"))')
  const holdingsFocus = await evaluate('document.activeElement?.id')
  assert.equal(holdingsFocus, 'holdings-title')
  await command('Page.navigate', { url: `${appURL}?s7-overview-actions=1&action=market` })
  await waitFor('Boolean([...document.querySelectorAll("[role=tab]")].find((node) => node.textContent?.trim() === "Overview"))')
  await focusAndActivateTab('Overview')
  await waitFor('Boolean(document.body.textContent?.includes("No immutable snapshot yet"))')
  await focusAndActivateText('Open Market Data')
  await waitFor('Boolean(document.querySelector("#market-data-title"))')
  const marketFocus = await evaluate('document.activeElement?.id')
  assert.equal(marketFocus, 'market-data-title')
  await command('Page.navigate', { url: `${appURL}?s7-overview-actions=1&action=currency` })
  await waitFor('Boolean([...document.querySelectorAll("[role=tab]")].find((node) => node.textContent?.trim() === "Overview"))')
  await focusAndActivateTab('Overview')
  await waitFor('Boolean(document.body.textContent?.includes("No immutable snapshot yet"))')
  await focusAndActivateText('Change reporting currency in Holdings')
  await waitFor('Boolean(document.querySelector("#currency"))')
  const currencyFocus = await evaluate('document.activeElement?.id')
  assert.equal(currencyFocus, 'currency')
  results.push({ width, refreshState, longDiagnosticState, overviewState, overviewActions: { holdingsFocus, marketFocus, currencyFocus }, detailsRetained: true, keyboardOnly: true })
}
console.log(JSON.stringify({ widths: results, noPrivateStorage: await evaluate('localStorage.length === 0 && sessionStorage.length === 0') }, null, 2))
socket.close()
