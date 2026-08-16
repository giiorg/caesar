import { createMemoryHistory, createRouter, RouterContextProvider } from '@tanstack/react-router'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { queryClient } from '../api/query-client'
import { routeTree } from '../routeTree.gen'
import { AppShell } from './app-shell'

const me = {
  user: {
    id: 'u1',
    email: 'user@example.com',
    role: 'admin',
    active: true,
    created_at: '',
    updated_at: '',
  },
  grants: [],
}

/**
 * THE SHELL CANNOT BE RENDERED BARE, and the plan's harness did exactly that.
 *
 * MEASURED: `render(<AppShell me={me}>body</AppShell>)` dies in `useLinkProps`
 * with "Cannot read properties of null (reading 'isServer')" — `<Link>` reads
 * the router off context and there is none. So the plan's own first test,
 * which asserts that the nav offers every route in the V1 page map, could not
 * have passed for ANY correct implementation of the shell: the nav it demands
 * is made of the links its harness cannot render.
 *
 * `RouterContextProvider` — not `RouterProvider` — is what fixes it. It
 * publishes the router on context and renders ITS OWN children, where
 * RouterProvider renders the router's matches instead. So the subject stays
 * the shell as a component handed its props, with no loader, no query and no
 * server, while the links resolve against the REAL route tree: a `to` that
 * survives the compiler but names a route the generator never emitted would
 * still be caught here.
 */
function renderShell(props: Partial<Parameters<typeof AppShell>[0]> = {}) {
  const router = createRouter({
    routeTree,
    // Never dereferenced — no loader runs, because no match is rendered. It is
    // passed because the tree is typed with `createRootRouteWithContext`, and
    // a cast here would be a lie about a seam that is cheap to satisfy.
    context: { queryClient },
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <RouterContextProvider router={router}>
      <AppShell me={me} {...props}>
        body
      </AppShell>
    </RouterContextProvider>,
  )
}

describe('the app shell', () => {
  afterEach(() => {
    // useTheme writes to BOTH, and neither is reset between tests in a file.
    // Without this the theme case leaks its choice into every case after it —
    // and into any later file in the same worker.
    document.documentElement.removeAttribute('data-theme')
    localStorage.clear()
  })

  it('hands the resolved session to the chrome rather than fetching one', () => {
    // Task 7 left `me` accepted and never read, so nothing proved the session
    // actually reached the shell. This is the assertion that makes the prop
    // load-bearing: the shell renders no server call of its own, and this test
    // has no MSW handler, so the only place the address can come from is the
    // prop the _app loader resolved.
    renderShell()
    expect(screen.getByText('user@example.com')).toBeInTheDocument()
  })
})

/**
 * THESE TWO DESCRIBES REPLACE `it('offers every route in the V1 page map')`.
 *
 * That test asserted one flat list — Home · Tasks · Runs · Approvals ·
 * Workflows · Repos · Admin, each a link with its href, and then
 * `getAllByRole('link')).toHaveLength(7)` on the whole nav. Its premise was
 * that the page map IS the tab bar, which is the conflation Plan 11 shipped:
 * decisions §7 has a seven-page map AND a five-tab shell, and nav.tsx cited the
 * first as authority for the second.
 *
 * This is a REPLACEMENT and not a relaxation. The length assertion is kept in
 * both halves and made stricter: each describe pins its presentation's whole
 * list, in order, with hrefs, as a single `toEqual` — so a link added to,
 * removed from, or reordered within either presentation fails, which the old
 * per-label loop plus a count could not do. What is genuinely gone is the claim
 * that there is ONE list. There are two, they are different, and the old test
 * could not have been repaired to say so: `within(nav).getByRole('link', …)`
 * now finds two Approvals links and throws before any count is reached.
 */
describe('the phone tab bar', () => {
  it('has exactly the five locked tabs, in order, and each one navigates', () => {
    renderShell()
    const tabs = within(screen.getByTestId('phone-tabs')).getAllByRole('link')
    // On text AND href, the property the replaced test was right to assert:
    // labels alone pass just as happily against five <span>s, i.e. against a
    // tab bar that navigates nowhere.
    expect(tabs.map((t) => [t.textContent, t.getAttribute('href')])).toEqual([
      ['Home', '/'],
      ['Runs', '/runs'],
      ['Approvals', '/approvals'],
      ['Tasks', '/tasks'],
      ['More', '/more'],
    ])
  })

  it('does not put a configuration surface in the thumb zone', () => {
    // The failure this replaces was not "a wrong number of tabs" — it was
    // Workflows, Repos and Admin competing for the same five thumb positions as
    // the four screens a person uses every day. Stated separately from the
    // ordered list above because it is the reason for the list, and because it
    // keeps naming the three pages even if the locked five are ever renamed.
    renderShell()
    const bar = within(screen.getByTestId('phone-tabs'))
    for (const label of ['Workflows', 'Repos', 'Admin']) {
      expect(bar.queryByRole('link', { name: label })).toBeNull()
    }
  })

  it('keeps one navigation landmark', () => {
    // _app.test.tsx finds the shell with getByRole('navigation') in ten places
    // and e2e/approvals.spec.ts in one. A second <nav> for the desktop rail
    // would make every one of those ambiguous — and jsdom applies no media
    // queries, so both would be present regardless of viewport. The two
    // presentations are CSS inside ONE landmark, which is why this is asserted
    // here rather than left to whichever unrelated test happened to break.
    renderShell()
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
  })
})

describe('the desktop rail', () => {
  it('carries all seven pages, because a sidebar has room for them', () => {
    renderShell()
    const rail = within(screen.getByTestId('desktop-rail')).getAllByRole('link')
    // Seven, and `More` is NOT among them: More is a phone affordance with
    // nothing behind it on a surface that already shows what it holds.
    expect(rail.map((l) => [l.textContent, l.getAttribute('href')])).toEqual([
      ['Home', '/'],
      ['Runs', '/runs'],
      ['Approvals', '/approvals'],
      ['Tasks', '/tasks'],
      ['Workflows', '/workflows'],
      ['Repos', '/repos'],
      ['Admin', '/admin'],
    ])
  })
})

describe('the theme toggle', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
    localStorage.clear()
  })

  // THE OS MUST PREFER DARK FOR THIS SUITE, and that is not decoration.
  // src/test/setup.ts gives every DOM test a matchMedia reporting NO
  // preference, so `system` resolves to LIGHT — and the plan's assertion
  // (`dataset.theme === 'light'` after one click) would then hold before the
  // click, against a component with no button in it at all. Preferring dark is
  // what makes switching to light an observable event.
  function preferDark() {
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark'),
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
  }

  it('switches theme by writing data-theme, never by editing a component', async () => {
    preferDark()
    renderShell()
    expect(document.documentElement.dataset.theme).toBe('dark')
    await userEvent.click(screen.getByRole('button', { name: /switch to light/i }))
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  /**
   * `Theme` has THREE states and this is the only control over it, so a
   * two-state toggle would strand one of them — permanently, since the choice
   * is persisted. use-theme.test.ts's last case covers `setTheme('system')`'s
   * removeItem leg and says outright that "Task 8's ThemeToggle is built
   * directly on this leg"; without this test that leg has no production caller
   * and the coverage is notional.
   */
  it('cycles all the way back to following the OS', async () => {
    preferDark()
    renderShell()
    const click = async (name: RegExp) =>
      await userEvent.click(screen.getByRole('button', { name }))

    await click(/switch to light/i)
    expect(document.documentElement.dataset.theme).toBe('light')
    await click(/switch to dark/i)
    expect(document.documentElement.dataset.theme).toBe('dark')
    await click(/switch to system/i)

    // Back to the OS: the stored choice is GONE, not overwritten with 'dark'.
    // Storing a value here would look identical on this render and silently
    // pin the theme forever after.
    expect(localStorage.getItem('caesar.theme')).toBeNull()
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('names the current theme as well as the next one', () => {
    // The visible word is the NEXT theme, which alone is ambiguous — "Light"
    // could name what you have or what you would get.
    preferDark()
    renderShell()
    expect(
      screen.getByRole('button', { name: 'Theme: system. Switch to light.' }),
    ).toHaveTextContent('light')
  })
})

describe('the connection indicator', () => {
  it('says so out loud when the stream is lost', () => {
    // The UI never renders stale data as live. A frozen badge that looks
    // authoritative is worse than an honest one that admits it lost the stream.
    renderShell({ connection: 'reconnecting' })
    expect(screen.getByRole('status')).toHaveTextContent(/reconnecting/i)
  })

  it('distinguishes a stream that gave up from one still trying', () => {
    // Without this case both non-live states can render one string and the
    // suite stays green — MEASURED as a surviving mutant. The two say
    // different things to the person reading them: `reconnecting` will fix
    // itself, `stopped` will not, and only one of them asks for a reload.
    renderShell({ connection: 'stopped' })
    const status = screen.getByRole('status')
    expect(status).toHaveTextContent(/stopped/i)
    expect(status).not.toHaveTextContent(/reconnecting/i)
  })

  it('is silent when the stream is live', () => {
    renderShell({ connection: 'live' })
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('is silent when no state has been reported yet', () => {
    // The prop is optional and defaults to 'live'. A default of anything else
    // would put a warning on screen before the transport has said a word.
    renderShell()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})

/**
 * EVERY QUERY HERE IS SCOPED TO A PRESENTATION, and the count is the assertion.
 *
 * Both presentations are in the DOM at once — CSS hides one — so a bare
 * `getByTestId('approvals-badge')` finds two elements and throws, and a bare
 * `queryByTestId(...).not.toBeInTheDocument()` would pass against a badge that
 * renders in the rail and not the bar. `queryAllByTestId(...)` with a length
 * makes the number the subject, which is the only shape that can tell "no
 * badge anywhere" from "a badge on one viewport only".
 */
describe('the approvals badge', () => {
  it.each(['phone-tabs', 'desktop-rail'])(
    'carries the count of things waiting on you, in %s',
    (presentation) => {
      // Asserted in BOTH, because CSS decides which one a person sees and
      // neither is a fallback for the other: a badge only in the rail is a
      // badge missing on every phone, and jsdom cannot tell you which.
      renderShell({ approvalCount: 3 })
      const inside = within(screen.getByTestId(presentation))
      const approvals = inside.getByRole('link', { name: /approvals/i })
      expect(within(approvals).getByTestId('approvals-badge')).toHaveTextContent('3')
      // On the Approvals link and no other — the badge is the whole reason
      // that row is singled out in the nav table.
      expect(inside.getAllByTestId('approvals-badge')).toHaveLength(1)
    },
  )

  it('shows nothing at all when there is nothing waiting', () => {
    // Drop the `> 0` guard and this is a badge reading "0" — a call to action
    // for nothing, on the one row people are meant to trust.
    renderShell({ approvalCount: 0 })
    expect(screen.queryAllByTestId('approvals-badge')).toHaveLength(0)
  })

  it('shows nothing when the count has not been reported yet', () => {
    renderShell()
    expect(screen.queryAllByTestId('approvals-badge')).toHaveLength(0)
  })
})
