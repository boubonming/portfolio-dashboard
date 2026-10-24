import { StrictMode } from 'react'
import type { ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { CssBaseline, ThemeProvider } from '@mui/material'
import App from './App'
import './styles.css'
import { portfolioTheme } from './theme'

export function ProductionProviders({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider theme={portfolioTheme}>
      <CssBaseline />
      {children}
    </ThemeProvider>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ProductionProviders><App /></ProductionProviders>
  </StrictMode>,
)
