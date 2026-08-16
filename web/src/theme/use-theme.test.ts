import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTheme } from './use-theme'

function mockPrefersDark(dark: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({
    matches: dark && q.includes('dark'),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

describe('useTheme', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
    localStorage.clear()
  })

  it('defaults to the system preference and resolves it onto data-theme', () => {
    mockPrefersDark(true)
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('system')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  // Without this case `resolve()` can ignore matchMedia entirely and return a
  // hardcoded 'dark' — MEASURED: that mutant passed the two tests either side of
  // this one. Only the false branch tells 'reads the preference' apart from
  // 'always dark'.
  it('resolves system to light when the OS does not prefer dark', () => {
    mockPrefersDark(false)
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('system')
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  // The read half of persistence. Without it `stored()` can return a constant
  // 'system' and never touch localStorage — MEASURED as a surviving mutant —
  // which would silently drop a saved theme on every page load.
  it('rehydrates an explicit choice from localStorage on mount', () => {
    localStorage.setItem('caesar.theme', 'light')
    mockPrefersDark(true) // the OS disagrees; the stored choice must still win
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('an explicit choice overrides the system preference and persists', () => {
    mockPrefersDark(true)
    const { result } = renderHook(() => useTheme())
    act(() => result.current.setTheme('light'))
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem('caesar.theme')).toBe('light')
  })

  // The third member of `Theme` — going BACK to 'system' after an explicit
  // choice. It is not symmetric with the case above: 'system' is stored as the
  // ABSENCE of a key, so this leg is a removeItem and not a setItem, and the
  // only proof it ran is that the next mount rehydrates 'system' rather than
  // the choice being abandoned. MEASURED as a surviving mutant before this
  // test existed: rewriting the body to `if (next !== 'system')
  // localStorage.setItem(KEY, next)` — i.e. dropping the removeItem — left all
  // nine other tests green while leaving 'light' in storage forever, so a user
  // who picked light and then picked system would silently get light back on
  // the next load and the toggle would report 'light'. Task 8's ThemeToggle is
  // built directly on this leg.
  it('returning to system clears the stored choice and follows the OS again', () => {
    localStorage.setItem('caesar.theme', 'light')
    mockPrefersDark(true)
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('light')

    act(() => result.current.setTheme('system'))

    expect(result.current.theme).toBe('system')
    expect(localStorage.getItem('caesar.theme')).toBeNull()
    // Following the OS again is the observable consequence: the stored choice
    // was light, the OS prefers dark, and dark is what must be stamped.
    expect(document.documentElement.dataset.theme).toBe('dark')
  })
})
