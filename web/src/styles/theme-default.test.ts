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

  // `color-scheme` used to sit on `html` as `dark light`, which asks the UA to
  // pick from the OS preference — the one input the tokens deliberately stop
  // obeying the moment someone uses Task 8's toggle. A light-OS visitor on the
  // default dark theme therefore got light scrollbars, form controls and focus
  // rings around a dark page. It belongs beside the colours, in the block that
  // knows which theme won, and it has to move as a PAIR: setting it in one file
  // only leaves the other theme inheriting whatever the first one stamped.
  it.each([
    ['dark', darkCss],
    ['light', lightCss],
  ])('has %s declare its own color-scheme rather than deferring to the OS', (name, css) => {
    expect(css.replace(/\/\*[\s\S]*?\*\//g, '')).toMatch(
      new RegExp(`color-scheme:\\s*${name}\\s*;`),
    )
  })

  it('defines the same token names in both themes', () => {
    const names = (css: string) => [...css.matchAll(/--caesar-[a-z-]+(?=\s*:)/g)].map((m) => m[0])
    expect(new Set(names(lightCss))).toEqual(new Set(names(darkCss)))
  })
})

/**
 * WCAG AA, held over BOTH themes, because a palette is only as accessible as
 * its worst theme and only one of these two was ever looked at.
 *
 * This exists because the light theme shipped below AA and nothing noticed.
 * `accent-contrast` on `accent` — a token PAIR WHOSE ENTIRE PURPOSE IS
 * CONTRAST — measured 4.23:1 in light against 10.43:1 in dark, and the
 * connection indicator's `text-warning` on `bg-warning/10` composited to
 * 3.55:1, making the banner that exists to be noticed the least readable thing
 * on the page. Both were measured by hand, recorded in a ledger, and left: the
 * same defect class as every other property this project asserted in a comment
 * that no test could falsify. Dark was fine throughout, which is exactly how it
 * survived — the flagship theme is the one anyone looks at.
 *
 * The subject is the SHIPPED token values, read out of the two CSS files with
 * `?raw`, so retuning a colour re-runs the check by construction.
 */

/** OKLCH -> linear-light sRGB (CSS Color 4), clamped into gamut. */
function oklchToLinearSrgb(L: number, C: number, hDeg: number): [number, number, number] {
  const h = (hDeg * Math.PI) / 180
  const a = C * Math.cos(h)
  const b = C * Math.sin(h)
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ].map((v) => Math.min(1, Math.max(0, v))) as [number, number, number]
}

const luminance = (c: readonly [number, number, number]) =>
  0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]

function contrast(fg: readonly [number, number, number], bg: readonly [number, number, number]) {
  const [hi, lo] = [luminance(fg), luminance(bg)].sort((x, y) => y - x) as [number, number]
  return (hi + 0.05) / (lo + 0.05)
}

/** The `--caesar-*` tokens of one theme file, as linear sRGB. */
function tokensOf(css: string): Record<string, [number, number, number]> {
  const declarations = [
    ...css
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .matchAll(/--caesar-([a-z-]+)\s*:\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)/g),
  ]
  // Every token is an oklch() literal today, and the parser silently skips
  // anything that is not. Without this the first token written as a hex or a
  // var() would vanish from the audit and the suite would still be green.
  const declared = [...css.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/--caesar-[a-z-]+(?=\s*:)/g)]
  expect(declarations).toHaveLength(declared.length)
  return Object.fromEntries(
    declarations.map((m) => [
      m[1] as string,
      oklchToLinearSrgb(Number(m[2]), Number(m[3]), Number(m[4])),
    ]),
  )
}

/** `bg-<token>/<pct>` — Tailwind's alpha modifier, composited over `under`. */
const over = (
  fg: readonly [number, number, number],
  under: readonly [number, number, number],
  alpha: number,
): [number, number, number] => [
  alpha * fg[0] + (1 - alpha) * under[0],
  alpha * fg[1] + (1 - alpha) * under[1],
  alpha * fg[2] + (1 - alpha) * under[2],
]

const AA = 4.5

// Every pair a component actually renders, as text. `border` on `ground` is
// deliberately absent: it measures ~1.25:1 in both themes and is a divider, not
// a control outline — WCAG's 3:1 non-text rule does not reach it, and folding
// it in would mean either a false assertion or a second threshold nobody would
// keep straight.
const TEXT_PAIRS: ReadonlyArray<readonly [string, string]> = [
  ['accent-contrast', 'accent'], // the accent button, /login's submit
  ['text', 'ground'],
  ['text', 'surface'], // the header and the nav bar
  ['text', 'surface-raised'],
  ['text-muted', 'ground'],
  ['text-muted', 'surface'], // the shell's email and the nav labels
  ['accent', 'ground'],
  ['accent', 'surface'], // the nav's active row
  ['danger', 'ground'],
  ['warning', 'ground'],
  ['success', 'ground'],
]

describe('the palette meets WCAG AA in both themes', () => {
  // THE CONVERTER'S OWN GUARD. Every assertion below routes through
  // oklchToLinearSrgb, so a converter that returned a constant — or that had
  // the matrix transposed — would report a comfortable ratio for every pair and
  // this whole suite would be worth nothing.
  it('computes contrast correctly before it is trusted to judge anything', () => {
    const white = oklchToLinearSrgb(1, 0, 0)
    expect(contrast(oklchToLinearSrgb(0, 0, 0), white)).toBeCloseTo(21, 1)
    expect(contrast(white, white)).toBeCloseTo(1, 5)
  })

  /**
   * The stronger half of the same guard, and a claim of theme-dark.css's own
   * that nothing held it to: "Every value carrying a hex comment was produced
   * by the sRGB -> OKLab -> OKLCH conversion of that hex (CSS Color 4), not
   * typed by hand." Converting back has to land on the hex it names.
   *
   * It anchors the converter to seven real colours rather than to greyscale,
   * where a transposed or mis-signed a/b term is invisible — and it catches a
   * token retyped by eye, which is the way the mockup's palette would actually
   * drift.
   */
  it('round-trips every hex the dark theme says it was converted from', () => {
    const encode = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055)
    const toHex = (t: readonly [number, number, number]) =>
      `#${t
        .map((c) =>
          Math.round(encode(c) * 255)
            .toString(16)
            .padStart(2, '0'),
        )
        .join('')}`

    const annotated = [
      ...darkCss.matchAll(
        /--caesar-([a-z-]+)\s*:\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\);\s*\/\*\s*(#[0-9a-f]{6})/g,
      ),
    ]
    expect(annotated.length).toBeGreaterThanOrEqual(7)
    for (const m of annotated) {
      expect([m[1], toHex(oklchToLinearSrgb(Number(m[2]), Number(m[3]), Number(m[4])))]).toEqual([
        m[1],
        m[5],
      ])
    }
  })

  for (const [name, css] of [
    ['dark', darkCss],
    ['light', lightCss],
  ] as const) {
    describe(name, () => {
      const t = tokensOf(css)

      it.each(TEXT_PAIRS)('renders %s on %s at AA or better', (fg, bg) => {
        expect(
          contrast(t[fg] as [number, number, number], t[bg] as [number, number, number]),
        ).toBeGreaterThanOrEqual(AA)
      })

      it('keeps the connection indicator readable on its own tinted band', () => {
        // shell/connection-indicator.tsx renders `text-warning` on
        // `bg-warning/10`, so the background is NOT `ground` — the plain
        // warning-on-ground pair above passes while the thing that ships is
        // half a point lower. This is the number that was 3.55:1.
        const band = over(
          t.warning as [number, number, number],
          t.ground as [number, number, number],
          0.1,
        )
        expect(contrast(t.warning as [number, number, number], band)).toBeGreaterThanOrEqual(AA)
      })
    })
  }
})
