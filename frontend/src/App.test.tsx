import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import App from './App'

describe('Portfolio Dashboard shell', () => {
  it('states that import approval is pending without showing holdings', () => {
    render(<App />)
    expect(screen.getByRole('heading', { name: 'Portfolio Dashboard' })).toBeTruthy()
    expect(screen.getByText(/awaiting approval/i)).toBeTruthy()
    expect(screen.queryByText(/NVDA|INTC|Maybank/)).toBeNull()
  })
})
