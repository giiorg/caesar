import { describe, expect, it } from 'vitest'
import darkCss from './theme-dark.css?raw'
import lightCss from './theme-light.css?raw'

// theme-dark.css's own comment calls its selector "load-bearing, not tidiness",
// and until this file existed nothing held it to that. MEASURED: reverting
// `:root:not([data-theme="light"])` to `:root[data-theme="dark"]` left vitest,
// `biome ci .`, `tsc --noEmit` and `vite build` all green, while an unstamped
// <html> then matched NO theme block at all — every `--caesar-*` undefined,
// every semantic `--color-*` invalid-at-computed-value-time, and first paint
// plus the whole of /login on the UA canvas.
//
// The subject is the SELECTOR's semantics, so the assertions run the real
// selector text from the shipped file through jsdom's real selector engine
// rather than regex-matching the source. `?raw` reads the file as text without
// Vite processing it, so what is asserted is exactly what ships.
//
// Why the default cannot instead be established in JS: nothing sets data-theme
// before React mounts, useTheme stamps it from an effect, and production CSP is
// `script-src 'self'`, which rules out the usual blocking inline <script>.

/**
 * The selector of the rule that declares `--caesar-ground`, i.e. the block that
 * defines a theme. Comments are stripped first so a selector mentioned inside
 * one (theme-dark.css discusses `:root[data-theme="light"]` in prose) cannot be
 * picked up instead of the real rule.
 */
function themeSelector(css: string): string {
  const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const rule = withoutComments.match(/([^{}]+)\{[^{}]*--caesar-ground\s*:/)
  if (!rule?.[1]) throw new Error('no rule declaring --caesar-ground was found')
  return rule[1].trim()
}

describe('the theme selectors', () => {
  const dark = themeSelector(darkCss)
  const light = themeSelector(lightCss)

  it('applies the dark tokens to a document nothing has stamped yet', () => {
    const html = document.documentElement
    html.removeAttribute('data-theme')
    // The load-bearing assertion. `:root[data-theme="dark"]` fails it.
    expect(html.matches(dark)).toBe(true)
    expect(html.matches(light)).toBe(false)
  })

  it('still applies the dark tokens to an explicitly dark document', () => {
    const html = document.documentElement
    html.setAttribute('data-theme', 'dark')
    expect(html.matches(dark)).toBe(true)
    expect(html.matches(light)).toBe(false)
    html.removeAttribute('data-theme')
  })

  it('hands an explicitly light document to the light block alone', () => {
    const html = document.documentElement
    html.setAttribute('data-theme', 'light')
    // Disjoint by construction: no cascade order or specificity comparison
    // decides which theme wins, so neither file can be reordered into a bug.
    expect(html.matches(dark)).toBe(false)
    expect(html.matches(light)).toBe(true)
    html.removeAttribute('data-theme')
  })

  it('falls back to dark for a document stamped with something unrecognised', () => {
    const html = document.documentElement
    html.setAttribute('data-theme', 'sepia')
    expect(html.matches(dark)).toBe(true)
    expect(html.matches(light)).toBe(false)
    html.removeAttribute('data-theme')
  })

  it('defines the same token names in both themes', () => {
    const names = (css: string) => [...css.matchAll(/--caesar-[a-z-]+(?=\s*:)/g)].map((m) => m[0])
    expect(new Set(names(lightCss))).toEqual(new Set(names(darkCss)))
  })
})
