import { type Theme, useTheme } from '../theme/use-theme'

/**
 * A THREE-state control, because `Theme` has three states.
 *
 * The plan specified `const next = theme === 'dark' ? 'light' : 'dark'`, and
 * that is wrong twice over — MEASURED, not reasoned:
 *
 * 1. `useTheme` starts at 'system', not 'dark'. So `next` is 'dark' on first
 *    render and the button reads "Dark". The plan's own test clicks
 *    `getByRole('button', { name: /light/i })`, which finds nothing: the test
 *    could never have passed against the component it was written for.
 * 2. It makes 'system' unreachable from the UI. Once anyone touches this
 *    button the app can never follow the OS again, and `setTheme('system')` —
 *    whose removeItem leg use-theme.test.ts covers specifically, saying "Task
 *    8's ThemeToggle is built directly on this leg" — would have had no
 *    production caller at all.
 *
 * There is also a silent-first-click bug in the two-state form: on a dark OS,
 * 'system' already resolves to dark, so "switch to dark" changes nothing
 * visible and the control reads as broken.
 *
 * The cycle starts at the default and ends back at it, and the label always
 * names WHAT YOU GET, never what you have — so the first click out of 'system'
 * on any OS is a visible change.
 */
const NEXT: Record<Theme, Theme> = { system: 'light', light: 'dark', dark: 'system' }

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const next = NEXT[theme]
  return (
    <button
      type="button"
      onClick={() => setTheme(next)}
      // The visible word alone is ambiguous — "Light" could name the current
      // theme or the next one, and a screen reader gets no header to read it
      // against. The accessible name says both.
      aria-label={`Theme: ${theme}. Switch to ${next}.`}
      className="text-text-muted px-3 text-sm capitalize"
    >
      {next}
    </button>
  )
}
