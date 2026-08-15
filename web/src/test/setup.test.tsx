import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

// The harness in ./setup.ts is what eight later tasks render through, so both of
// its guarantees are asserted here rather than assumed. Each one was watched to
// fail with the corresponding line removed from setup.ts.
describe('the dom test harness', () => {
  it("registers jest-dom's matchers on vitest's expect", () => {
    // setup.ts's `import '@testing-library/jest-dom/vitest'` has no binding, so
    // nothing else in the tree would notice it being dropped until Task 6's
    // login suite failed with "Invalid Chai property: toBeInTheDocument" —
    // which reads like a typo in the test rather than a missing harness.
    render(<p>present</p>)
    expect(screen.getByText('present')).toBeInTheDocument()
  })

  it('provides a default window.matchMedia, since jsdom does not implement one', () => {
    // Without setup.ts's assignment this is 'undefined' and every tree
    // containing useTheme throws "window.matchMedia is not a function".
    expect(typeof window.matchMedia).toBe('function')
    expect(window.matchMedia('(prefers-color-scheme: dark)').matches).toBe(false)
  })

  // The pair below runs in order on purpose too, and guards vitest.config.ts's
  // `unstubGlobals: true`: `restoreMocks` does NOT undo vi.stubGlobal, so
  // without it a preference stubbed by one test would still be in force in
  // every later test of the same file.
  it('lets a test override matchMedia when the preference is its subject', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: true, media: q }))
    expect(window.matchMedia('(prefers-color-scheme: dark)').matches).toBe(true)
  })

  it("restores the harness default after the previous test's stub", () => {
    expect(window.matchMedia('(prefers-color-scheme: dark)').matches).toBe(false)
  })

  // These two run in order on purpose: the second one's subject IS what the
  // first one left behind. `globals: false` means RTL never registers its own
  // afterEach(cleanup), so this pair is the only thing standing between a later
  // task and a getByRole that finds two of everything.
  it('renders the first tree', () => {
    render(<p>first</p>)
    expect(screen.getByText('first')).not.toBeNull()
  })

  it("cleans up the previous test's DOM before this one renders", () => {
    render(<p>second</p>)
    expect(document.body.textContent).toBe('second')
  })
})
