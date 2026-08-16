import { useCallback, useEffect, useState } from 'react'

export type Theme = 'system' | 'dark' | 'light'

const KEY = 'caesar.theme'

function stored(): Theme {
  const v = localStorage.getItem(KEY)
  return v === 'dark' || v === 'light' ? v : 'system'
}

function resolve(theme: Theme): 'dark' | 'light' {
  if (theme !== 'system') return theme
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(stored)

  useEffect(() => {
    document.documentElement.dataset.theme = resolve(theme)
  }, [theme])

  const setTheme = useCallback((next: Theme) => {
    if (next === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, next)
    setThemeState(next)
  }, [])

  return { theme, setTheme }
}
