// @ts-expect-error Vitest executes this test helper in Node, while the app tsconfig omits Node typings.
import { readFileSync } from 'node:fs'

export type StyleRuleEvidence = {
  selectorText: string
  cssText: string
}

let installedSheet: CSSStyleSheet | null = null

export function installCommittedStyles() {
  const style = document.createElement('style')
  style.dataset.testStyles = 'committed'
  style.textContent = readFileSync('src/styles.css', 'utf8')
  document.head.appendChild(style)
  installedSheet = style.sheet
  return () => {
    style.remove()
    installedSheet = null
  }
}

function styleRules(rules: CSSRuleList | undefined = installedSheet?.cssRules) {
  const result: CSSStyleRule[] = []
  for (const rule of Array.from(rules || [])) {
    if (rule.type === CSSRule.STYLE_RULE) result.push(rule as CSSStyleRule)
    else if ('cssRules' in rule) result.push(...styleRules((rule as CSSGroupingRule).cssRules))
  }
  return result
}

export function committedStyleSelectors() {
  return styleRules().map((rule) => rule.selectorText)
}

export function committedStyleRules() {
  return styleRules().map((rule) => ({ selectorText: rule.selectorText, cssText: rule.cssText }))
}

function selectorForState(selector: string, state: 'focus-visible' | 'disabled' | undefined) {
  // JSDOM does not implement the focus-visible pseudo-class. A focused element
  // is the closest deterministic CSSOM witness for the committed selector.
  return state === 'focus-visible' ? selector.replaceAll(':focus-visible', ':focus') : selector
}

export function matchingStyleRules(element: Element, state?: 'focus-visible' | 'disabled'): StyleRuleEvidence[] {
  const rules = styleRules()
  return rules.flatMap((rule) => rule.selectorText.split(/,(?![^()]*\))/).map((selector) => selector.trim()).map((selector) => ({ selector, rule })))
    .filter(({ selector }) => {
      try { return element.matches(selectorForState(selector, state)) } catch { return false }
    })
    .map(({ selector, rule }) => ({ selectorText: selector, cssText: rule.cssText }))
}

export function hasMatchingDeclaration(element: Element, property: string, value: string, state?: 'focus-visible' | 'disabled') {
  return matchingStyleRules(element, state).some(({ cssText }) => new RegExp(`${property}\\s*:\\s*${value.replace(/[.*+?^${}()|[\\]\\]/g, '\\$&')}(?:;|\\s|$)`, 'i').test(cssText))
}

export function hasMatchingSelector(element: Element, selector: string, state?: 'focus-visible' | 'disabled') {
  return matchingStyleRules(element, state).some(({ selectorText }) => selectorText === selector)
}

export function assertNoMatchingSelector(elements: Element[], selector: string) {
  return elements.every((element) => !hasMatchingSelector(element, selector))
}