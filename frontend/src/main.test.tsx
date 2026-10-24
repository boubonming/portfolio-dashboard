import { render, screen } from '@testing-library/react'
import { useTheme } from '@mui/material/styles'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup } from '@testing-library/react'

function ThemeProbe() {
  const theme = useTheme()
  return <output data-testid="theme-probe">{theme.palette.background.default}</output>
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('production entrypoint providers', () => {
  it('applies the central dark theme and CssBaseline wrapper', async () => {
    document.body.innerHTML = '<div id="root"></div>'
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => undefined)))
    const { ProductionProviders } = await import('./main')
    render(<ProductionProviders><ThemeProbe /></ProductionProviders>)
    expect(screen.getByTestId('theme-probe').textContent).toBe('#111817')
    expect(document.querySelector('style[data-emotion]')).toBeTruthy()
  })
})
