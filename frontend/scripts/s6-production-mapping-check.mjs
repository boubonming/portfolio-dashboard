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
async function keyPress(key, shift = false) { const params = { key, code: key, modifiers: shift ? 8 : 0 }; const codes = { Tab: 9, Enter: 13 }; if (codes[key]) Object.assign(params, { windowsVirtualKeyCode: codes[key], nativeVirtualKeyCode: codes[key] }); await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params }); await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params }); await settle() }
async function requestTab(label) { await evaluate(`(() => { const node = [...document.querySelectorAll('[role="tab"]')].find((item) => item.textContent?.includes(${JSON.stringify(label)})); node?.click(); return Boolean(node) })()`); await settle() }
async function activateTab(label) { await requestTab(label); await waitFor(`Boolean([...document.querySelectorAll('[role="tab"]')].find((node) => node.textContent?.includes(${JSON.stringify(label)}) && node.getAttribute('aria-selected') === 'true'))`) }
async function setField(selector, value) { await evaluate(`(() => { const node = document.querySelector(${JSON.stringify(selector)}); if (!node) return false; const setter = Object.getOwnPropertyDescriptor(node.constructor.prototype, 'value')?.set; setter?.call(node, ${JSON.stringify(value)}); node.dispatchEvent(new Event('input', { bubbles: true })); node.dispatchEvent(new Event('change', { bubbles: true })); return true })()`); await settle() }
async function click(selector) { await evaluate(`(() => { const node = document.querySelector(${JSON.stringify(selector)}); node?.click(); return Boolean(node) })()`); await settle() }
async function clickText(text) { await evaluate(`(() => { const node = [...document.querySelectorAll('button')].find((item) => item.textContent?.includes(${JSON.stringify(text)})); node?.click(); return Boolean(node) })()`); await settle() }
function assertNoOverflow(state, label) { assert.ok(state.rootScrollWidth <= state.innerWidth + 1, `${label} root overflow: ${JSON.stringify(state)}`); assert.ok(state.bodyScrollWidth <= state.innerWidth + 1, `${label} body overflow: ${JSON.stringify(state)}`) }
async function shellState() { return evaluate(`(() => ({ innerWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, selected: [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true')?.textContent }))()`) }
async function mappingState() { return evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); const rows = [...(region?.querySelectorAll('tbody tr') || [])]; const controls = rows.flatMap((row) => [...row.querySelectorAll('select, input')]).filter((node) => !node.disabled); return { rows: rows.length, controls: controls.length, radios: region?.querySelectorAll('input[type="radio"]').length || 0, text: region?.textContent || '', scrollWidth: region?.scrollWidth || 0, clientWidth: region?.clientWidth || 0, identities: rows.map((row) => row.querySelector('.mapping-identity-cell')?.getBoundingClientRect().toJSON()) } })()`) }
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
    if (reverse && index > 0) await keyPress('Tab', true)
  }
}
await command('Page.enable'); await command('Runtime.enable')
await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
await command('Emulation.setDeviceMetricsOverride', { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false })
await command('Page.navigate', { url: appURL })
await waitFor('Boolean(document.querySelector("[role=tab]"))'); await waitFor('Boolean([...document.querySelectorAll("button")].some((node) => node.textContent?.includes("Sign out")))')
await activateTab('Market Data'); await waitFor('document.querySelectorAll("[role=region][aria-label*=Market] tbody tr").length === 2'); await waitFor('document.querySelectorAll("[role=region][aria-label*=Market] input[type=radio]").length === 1')
let state = await mappingState(); assert.equal(state.rows, 2); assert.match(state.text, /Distinguishing provider candidate/); assert.match(state.text, /provider unavailable/); assertNoOverflow(await shellState(), '1280px Market Data')
// Candidate metadata, manual identity editing, confirmation and truthful progress.
await setField('tbody tr:nth-child(1) input[placeholder="Exact listing / coin ID"]', 'same-coin-owner-edit')
await click('tbody tr:nth-child(1) input[type="checkbox"]')
await requestTab('Holdings'); await waitFor('document.querySelector("[role=alertdialog] h2")?.textContent?.includes("Unsaved Market Data mapping")'); await clickText('Keep editing'); await waitFor('document.querySelector("[role=tab][aria-label=\\"Market data\\"]")?.getAttribute("aria-selected") === "true"')
await setField('tbody tr:nth-child(1) input[placeholder="Exact listing / coin ID"]', 'same-coin')
await click('tbody tr:nth-child(1) input[type="checkbox"]')
await setField('tbody tr:nth-child(2) select', 'coingecko'); await setField('tbody tr:nth-child(2) input[placeholder="Exact listing / coin ID"]', 'second-coin'); await setField('tbody tr:nth-child(2) input[aria-label^="Quote currency"]', 'USD'); await setField('tbody tr:nth-child(2) input[placeholder="Where you verified it"]', 'S6 manual verification'); await click('tbody tr:nth-child(2) input[type="checkbox"]')
await waitFor('document.body.textContent.includes("2 of 2 mappings confirmed")'); assert.equal(await evaluate('document.querySelector("button[aria-label=\\"Next unfinished mapping\\"]")'), null); assert.equal(await evaluate('[...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled'), false)
await nativeScan(false)
// Stale recovery: GET is held, so no choice or second PUT can appear early.
await clickText('Save all mappings'); await waitFor('window.__s6.state.putCalls === 1'); await waitFor('document.body.textContent.includes("changed in another window")'); assert.equal(await evaluate('[...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled'), true); await click('p[role=alert] button'); await waitFor('Boolean(window.__s6.state.pendingRead)'); assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Keep editing with current setup")))'), false)
assert.equal(await evaluate('[...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Save all mappings"))?.disabled'), true); await evaluate('window.__s6.resolveRead(); true'); await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Keep editing with current setup")))'); await clickText('Keep editing with current setup')
// The next save is uncertain; reconciliation offers no blind PUT and requires a choice.
await clickText('Save all mappings'); await waitFor('window.__s6.state.putCalls === 2'); await waitFor('document.body.textContent.includes("outcome is uncertain")'); await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.includes("Keep editing with current setup")))'); await clickText('Keep editing with current setup')
await clickText('Save all mappings'); await waitFor('window.__s6.state.putCalls === 3'); await waitFor('document.body.textContent.includes("Mappings saved")'); await waitFor('document.body.textContent.includes("Could not reload Market Data setup")'); assert.equal(await evaluate('window.__s6.state.putCalls'), 3); await click('p[role=alert] button[aria-label="Reload setup"]'); await waitFor('window.__s6.state.setupReads >= 5'); assert.equal(await evaluate('document.querySelector("[role=alert]")?.textContent || ""'), ''); assert.ok(await evaluate('document.body.textContent.includes("Mappings saved")'))
await command('Emulation.setDeviceMetricsOverride', { width: 320, height: 800, deviceScaleFactor: 1, mobile: false }); await evaluate('window.dispatchEvent(new Event("resize")); true'); await settle(); state = await mappingState(); assertNoOverflow(await shellState(), '320px Market Data'); assert.ok(state.scrollWidth > state.clientWidth, '320px mapping table is not horizontally bounded'); await nativeScan(false); await nativeScan(true)
console.log(JSON.stringify({ putCalls: await evaluate('window.__s6.state.putCalls'), setupReads: await evaluate('window.__s6.state.setupReads'), desktop: await shellState(), narrow: state, receipt: await evaluate('document.body.textContent.includes("Mappings saved")') }, null, 2))
socket.close()