#!/usr/bin/env node
// Run against the loopback production bundle with Chromium CDP enabled:
// S6_APP_URL=http://127.0.0.1:4173/ S6_CDP_ENDPOINT=http://127.0.0.1:9222 node scripts/s6-production-mapping-check.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const appURL = process.env.S6_APP_URL || 'http://127.0.0.1:4173/'
const cdpEndpoint = process.env.S6_CDP_ENDPOINT || 'http://127.0.0.1:9222'
const fixture = await readFile(new URL('./s6-synthetic-fixture.js', import.meta.url), 'utf8')
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
  const waiter = pending.get(message.id); pending.delete(message.id)
  if (message.error) waiter.reject(new Error(message.error.message)); else waiter.resolve(message.result)
})
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }) })
function command(method, params = {}) { const id = ++nextID; socket.send(JSON.stringify({ id, method, params })); return new Promise((resolve, reject) => pending.set(id, { resolve, reject })) }
async function evaluate(expression) { const result = await command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || 'browser evaluation failed'); return result.result?.value }
async function waitFor(expression, timeout = 6000) { const started = Date.now(); while (Date.now() - started < timeout) { if (await evaluate(expression)) return; await new Promise((resolve) => setTimeout(resolve, 60)) } throw new Error(`Timed out waiting for ${expression}`) }
async function settle() { await new Promise((resolve) => setTimeout(resolve, 100)) }
async function keyPress(key, { ctrl = false, shift = false } = {}) {
  const virtualKeyCodes = { Enter: 13, Tab: 9, Space: 32, ArrowDown: 40, ArrowUp: 38, ArrowRight: 39, ArrowLeft: 37, a: 65 }
  const inputKey = key === 'Space' ? ' ' : key
  const params = { key: inputKey, code: key === 'a' ? 'KeyA' : key, modifiers: ctrl ? 2 : shift ? 8 : 0 }
  if (virtualKeyCodes[key]) Object.assign(params, { windowsVirtualKeyCode: virtualKeyCodes[key], nativeVirtualKeyCode: virtualKeyCodes[key] })
  const character = key === 'Enter' ? '\r' : key === 'Space' ? ' ' : undefined
  await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params, ...(character ? { text: character, unmodifiedText: character } : {}) })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params })
  await settle()
}
async function focus(selector) {
  const found = await evaluate(`(() => { const node = document.querySelector(${JSON.stringify(selector)}); node?.focus(); return Boolean(node && document.activeElement === node); })()`)
  assert.equal(found, true, `Could not focus ${selector}`)
}
async function typeInto(selector, value) {
  await focus(selector)
  await keyPress('a', { ctrl: true })
  await command('Input.insertText', { text: value })
  await settle()
}
async function activateButton(label) {
  const found = await evaluate(`(() => { const node = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === ${JSON.stringify(label)} || candidate.getAttribute('aria-label') === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`)
  assert.equal(found, true, `Button not found: ${label}`)
  await keyPress('Enter')
}
async function activateTab(label, { expectSelected = true } = {}) {
  const found = await evaluate(`(() => { const node = [...document.querySelectorAll('[role="tab"]')].find((candidate) => (candidate.textContent || '').trim() === ${JSON.stringify(label)} || candidate.getAttribute('aria-label') === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`)
  assert.equal(found, true, `Tab not found: ${label}`)
  await keyPress('Enter')
  if (expectSelected) await waitFor(`Boolean([...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true' && ((node.textContent || '').trim() === ${JSON.stringify(label)} || node.getAttribute('aria-label') === ${JSON.stringify(label)})))`)
}
async function selectFirstProvider(selector) {
  await focus(selector)
  await keyPress('ArrowDown')
  await keyPress('Enter')
}
function assertNoOverflow(state, label) { assert.ok(state.rootScrollWidth <= state.innerWidth + 1, `${label} root overflow: ${JSON.stringify(state)}`); assert.ok(state.bodyScrollWidth <= state.innerWidth + 1, `${label} body overflow: ${JSON.stringify(state)}`) }
async function shellState() { return evaluate(`(() => ({ innerWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, selected: [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true')?.textContent }))()`) }
async function mappingState() { return evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); const rows = [...(region?.querySelectorAll('tbody tr') || [])]; return { rows: rows.length, text: region?.textContent || '', scrollWidth: region?.scrollWidth || 0, clientWidth: region?.clientWidth || 0, identities: rows.map((row) => row.querySelector('.mapping-identity-cell')?.getBoundingClientRect().toJSON()) } })()`) }
async function nativeScan(reverse = false) {
  const count = await evaluate(`document.querySelectorAll('[role="region"][aria-label*="Market"] tbody tr select:not(:disabled), [role="region"][aria-label*="Market"] tbody tr input:not(:disabled)').length`)
  assert.ok(count > 0, 'native mapping scan has no controls')
  if (reverse) await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); const controls = [...region.querySelectorAll('tbody tr select, tbody tr input')].filter((node) => !node.disabled); controls.at(-1)?.focus(); return true })()`)
  else await evaluate(`document.querySelector('[role="region"][aria-label*="Market"]')?.focus(); true`)
  for (let index = reverse ? count - 1 : 0; reverse ? index >= 0 : index < count; reverse ? index-- : index++) {
    if (!reverse) await keyPress('Tab')
    const state = await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); const node = document.activeElement; const rect = node?.getBoundingClientRect(); const identity = node?.closest('tr')?.querySelector('.mapping-identity-cell')?.getBoundingClientRect(); return { node: node?.tagName, insideIdentity: Boolean(node?.closest('.mapping-identity-cell')), rect: rect?.toJSON(), identity: identity?.toJSON(), region: region?.getBoundingClientRect().toJSON() } })()`)
    assert.equal(state.node === 'INPUT' || state.node === 'SELECT', true, `native ${reverse ? 'reverse' : 'forward'} focus left mapping controls`)
    assert.ok(state.rect.left >= state.region.left - 1 && state.rect.right <= state.region.right + 1, `native focus clipped: ${JSON.stringify(state)}`)
    if (!state.insideIdentity) assert.ok(state.rect.left >= state.identity.right + 4, `native focus hidden by sticky identity: ${JSON.stringify(state)}`)
    if (reverse && index > 0) await keyPress('Tab', { shift: true })
  }
}
async function runWidth(width) {
  await command('Emulation.setDeviceMetricsOverride', { width, height: 800, deviceScaleFactor: 1, mobile: false })
  await command('Page.navigate', { url: appURL })
  await waitFor('Boolean(document.querySelector("[role=tab]"))')
  await waitFor('Boolean([...document.querySelectorAll("button")].some((node) => node.textContent?.includes("Sign out")))')
  await activateTab('Market Data')
  await waitFor('document.querySelectorAll("[role=region][aria-label*=Market] tbody tr").length === 2')
  await waitFor('document.querySelectorAll("[role=region][aria-label*=Market] input[type=radio]").length === 1')
  let state = await mappingState()
  assert.equal(state.rows, 2)
  assert.match(state.text, /Distinguishing provider candidate/)
  assert.match(state.text, /provider unavailable/)
  assertNoOverflow(await shellState(), `${width}px Market Data initial`)
  // Unverified discovery suggestions are not owner edits and must not block a tab exit.
  await activateTab('Holdings')
  await activateTab('Market Data')
  await waitFor('document.querySelectorAll("[role=region][aria-label*=Market] tbody tr").length === 2')
  assert.equal(await evaluate('Boolean(document.querySelector("[role=alertdialog]"))'), false)
  assert.equal(await evaluate('Boolean(document.querySelector("button[aria-label=\\"Next unfinished mapping\\"]"))'), true)
  await activateButton('Next unfinished mapping')
  assert.equal(await evaluate('Boolean(document.activeElement?.matches("tbody tr:first-child select, tbody tr:first-child input"))'), true)
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled)'), true)

  // Owner editing, confirmation and navigation are all native keyboard input.
  await typeInto('tbody tr:nth-child(1) input[placeholder="Exact listing / coin ID"]', 'same-coin-owner-edit')
  await focus('tbody tr:nth-child(1) input[type="checkbox"]'); await keyPress('Space')
  await activateTab('Holdings', { expectSelected: false })
  await waitFor('Boolean(document.querySelector("[role=alertdialog]"))')
  await activateButton('Keep editing')
  await waitFor('Boolean([...document.querySelectorAll("[role=tab]")].find((node) => node.getAttribute("aria-selected") === "true" && node.getAttribute("aria-label") === "Market data"))')
  await typeInto('tbody tr:nth-child(1) input[placeholder="Exact listing / coin ID"]', 'same-coin')
  await focus('tbody tr:nth-child(1) input[type="checkbox"]'); await keyPress('Space')
  await selectFirstProvider('tbody tr:nth-child(2) select')
  await typeInto('tbody tr:nth-child(2) input[placeholder="Exact listing / coin ID"]', 'second-coin')
  await typeInto('tbody tr:nth-child(2) input[aria-label^="Quote currency"]', 'EUR')
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled)'), true)
  await typeInto('tbody tr:nth-child(2) input[aria-label^="Quote currency"]', 'USD')
  await typeInto('tbody tr:nth-child(2) input[placeholder="Where you verified it"]', 'S6 manual verification')
  await focus('tbody tr:nth-child(2) input[type="checkbox"]'); await keyPress('Space')
  await waitFor('document.body.textContent.includes("2 of 2 mappings confirmed")')
  assert.equal(await evaluate('Boolean(document.querySelector("button[aria-label=\\"Next unfinished mapping\\"]"))'), false)
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled)'), false)
  await nativeScan(false)

  await activateButton('Save all mappings')
  await waitFor('window.__s6.state.putCalls === 1')
  await waitFor('document.body.textContent.includes("changed in another window")')
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled)'), true)
  await activateButton('Reload setup')
  await waitFor('Boolean(window.__s6.state.pendingRead)')
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Keep editing with current setup")))'), false)
  await evaluate('window.__s6.resolveRead(); true')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Keep editing with current setup")))')
  await activateButton('Keep editing with current setup')

  await activateButton('Save all mappings')
  await waitFor('window.__s6.state.putCalls === 2')
  await waitFor('document.body.textContent.includes("outcome is uncertain")')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Keep editing with current setup")))')
  await activateButton('Keep editing with current setup')
  await activateButton('Save all mappings')
  await waitFor('window.__s6.state.putCalls === 3')
  await waitFor('document.body.textContent.includes("Mappings saved")')
  await waitFor('document.body.textContent.includes("Could not reload Market Data setup")')
  await activateButton('Reload setup')
  await waitFor('window.__s6.state.setupReads >= 5')
  assert.ok(await evaluate('document.body.textContent.includes("Mappings saved")'))
  assert.equal(await evaluate('Boolean(document.querySelector("[role=alert]"))'), false)
  assertNoOverflow(await shellState(), `${width}px Market Data recovered`)
  state = await mappingState()
  if (width <= 320) assert.ok(state.scrollWidth > state.clientWidth, `${width}px mapping table is not horizontally bounded`)
  await nativeScan(false)
  await nativeScan(true)
  return { width, shell: await shellState(), mapping: state, putCalls: await evaluate('window.__s6.state.putCalls'), setupReads: await evaluate('window.__s6.state.setupReads'), receipt: await evaluate('document.body.textContent.includes("Mappings saved")') }
}
await command('Page.enable'); await command('Runtime.enable')
await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
const desktop = await runWidth(1280)
const narrow = await runWidth(320)
console.log(JSON.stringify({ desktop, narrow }, null, 2))
socket.close()