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

  it('offers every route in the V1 page map', () => {
    renderShell()
    const nav = screen.getByRole('navigation')
    // Asserted as LINKS with hrefs, not as text. `toHaveTextContent` alone —
    // which is what the plan asked for — passes just as happily against seven
    // <span>s, i.e. against a nav that navigates nowhere.
    const expected: ReadonlyArray<readonly [string, string]> = [
      ['Home', '/'],
      ['Tasks', '/tasks'],
      ['Runs', '/runs'],
      ['Approvals', '/approvals'],
      ['Workflows', '/workflows'],
      ['Repos', '/repos'],
      ['Admin', '/admin'],
    ]
    for (const [label, href] of expected) {
      expect(within(nav).getByRole('link', { name: label })).toHaveAttribute('href', href)
    }
    expect(within(nav).getAllByRole('link')).toHaveLength(expected.length)
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

describe('the approvals badge', () => {
  it('carries the count of things waiting on you', () => {
    renderShell({ approvalCount: 3 })
    expect(screen.getByTestId('approvals-badge')).toHaveTextContent('3')
    // On the Approvals link and no other — the badge is the whole reason that
    // row is singled out in the nav table.
    const approvals = screen.getByRole('link', { name: /approvals/i })
    expect(within(approvals).getByTestId('approvals-badge')).toBeInTheDocument()
  })

  it('shows nothing at all when there is nothing waiting', () => {
    // Drop the `> 0` guard and this is a badge reading "0" — a call to action
    // for nothing, on the one row people are meant to trust.
    renderShell({ approvalCount: 0 })
    expect(screen.queryByTestId('approvals-badge')).not.toBeInTheDocument()
  })

  it('shows nothing when the count has not been reported yet', () => {
    renderShell()
    expect(screen.queryByTestId('approvals-badge')).not.toBeInTheDocument()
  })
})
