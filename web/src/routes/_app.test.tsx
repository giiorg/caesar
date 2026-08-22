import { cleanup, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { queryClient } from '../api/query-client'
import {
  authenticatedMe as me,
  noSession,
  pendingStream,
  renderApp,
  silentStream,
} from '../test/render-app'

// The harness's defaults are installed BEHIND a caller's handlers in one
// `server.use` call, so every test below that passes its own /auth/me is also
// the proof that the override wins — a harness whose defaults outranked the
// caller would fail the first test in this file rather than pass it quietly.

describe('the protected layout', () => {
  it('sends an unauthenticated visit to a loader-bearing route to /login, on a REPLACED entry', async () => {
    // THIS TEST CHANGED ITS MEANING IN PLAN 12a AND THE CHANGE IS THE POINT.
    //
    // It used to navigate to /tasks BECAUSE that route issued no request of its
    // own, and it carried two properties at once on the strength of that. Task
    // 14 gave /tasks a loader, so they are two tests now: this one on a route
    // that fetches, the next one on a route that does not.
    //
    // The fix that was rejected: invent a loader-free route to keep the old
    // property on /tasks. That is a seam with no production caller — a mistake
    // this project has already paid for, where a test-only affordance made all
    // five of its own tests vacuous. The next test uses /more instead, which is
    // a real page reached from a real tab.
    let taskRequests = 0
    const router = renderApp('/tasks', [
      http.get('/api/v1/auth/me', noSession),
      http.get('/api/v1/tasks', () => {
        taskRequests += 1
        return noSession()
      }),
    ])
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()

    // THE ROUTE REALLY IS LOADER-BEARING, and that is this test's premise
    // rather than a detail. MEASURED: the router runs the layout loader and the
    // child loader on the same load, so /api/v1/tasks IS requested and IS
    // rejected even though the layout's redirect wins. Delete /tasks' loader
    // and this line goes to 0 — which is exactly how this test would decay back
    // into the no-request case it no longer claims to cover.
    expect(taskRequests).toBe(1)

    // ONE ENTRY. A loader `throw redirect()` during the initial load REPLACES
    // it; a navigate() from a rendered error boundary PUSHES one, leaving
    // /tasks behind /login in the back stack — so Back returns the visitor to a
    // route they cannot see and re-runs the whole failed load.
    //
    // Without this line the test is VACUOUS — MEASURED: neuter _app's
    // ensureQueryData and it still passes, because AppLayout's
    // useSuspenseQuery then throws the same 401 with no data to fall back on
    // and AppErrorBoundary navigates to the same place. The visible outcome is
    // identical (the sign-in assertion above stays green under that mutant);
    // the history is not.
    //
    // WHAT THIS LINE DOES NOT SAY, so that nobody adds the claim back: it is
    // not a redirect COUNT. MEASURED — a redirect thrown from a loader can
    // never add an entry, because the router spreads the redirect's options and
    // then hard-sets `replace: true` over them (router-core load-client.js,
    // `followRedirect`). Making /tasks' loader throw a second /login redirect
    // of its own leaves this at 1. Loader-versus-rendered-tree is the only
    // distinction it draws, and that is the one worth drawing.
    expect(router.history.length).toBe(1)
    expect(router.state.location.pathname).toBe('/login')
  })

  it('sends an unauthenticated visit to a route with NO REQUEST OF ITS OWN to /login', async () => {
    // THE CASE A REDIRECT-ON-401 DESIGN CANNOT CATCH, kept rather than given
    // up. The brief for this rewrite dropped it on the premise that after Plan
    // 12a no loader-free route remains; MEASURED FALSE — /more has no loader
    // (routes/_app/more.tsx:13), it renders SECONDARY and asks the server for
    // nothing, and /admin, /repos and /workflows are loader-free too.
    //
    // What it buys over the test above, MEASURED: move the session check out of
    // the layout loader and into each data route's loader, and the test above
    // still passes — /tasks would guard itself on the way to its own data.
    // This one goes to 2, because this route asks the server for nothing, so a
    // per-fetch guard has nothing to run on and the visitor is turned away only
    // after the shell has tried to render, from the error boundary, onto a
    // pushed entry. Guarding the LAYOUT rather than the fetches is the
    // property, and this is the only test in the suite that can see it.
    const router = renderApp('/more', [http.get('/api/v1/auth/me', noSession)])
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
    expect(router.history.length).toBe(1)
  })

  it('renders the shell on a reload with a valid cookie', async () => {
    renderApp('/')
    expect(await screen.findByRole('navigation')).toBeInTheDocument()
  })

  it('sends an expired session back to /login', async () => {
    let calls = 0
    renderApp('/', [
      http.get('/api/v1/auth/me', () => {
        calls += 1
        return calls === 1 ? me() : noSession()
      }),
    ])
    await screen.findByRole('navigation')
    // INVALIDATE ONLY. `queryClient.clear()` first would remove the entry
    // outright, leaving invalidateQueries nothing to mark stale — the refetch
    // that this test exists to observe would then be an artefact of the
    // cache being empty rather than of the session having expired, which is a
    // different code path and the one that is NOT in production.
    await queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
    expect(calls).toBeGreaterThanOrEqual(2)
  })
})

describe('the protected layout error surface', () => {
  // The plan wrote this against `/approvals` returning a 500. Task 11 gave
  // that route a loader, so it can carry its own error test now — but this one
  // stays on /auth/me, because the two failures reach the boundary by
  // different paths (a layout loader rethrow versus a child loader) and only
  // this one exercises the rethrow.
  it('renders the correlation id when a protected route 500s', async () => {
    renderApp('/', [
      http.get('/api/v1/auth/me', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500, correlation_id: 'cid-99' },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])
    expect(await screen.findByText('cid-99')).toBeInTheDocument()
  })
})

describe('the /login route guard', () => {
  it('redirects an already-signed-in visitor inward', async () => {
    const router = renderApp('/login')
    expect(await screen.findByRole('navigation')).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/')
  })

  it('leaves a visitor with no session on the form', async () => {
    const router = renderApp('/login', [http.get('/api/v1/auth/me', noSession)])
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
  })
})

describe('the layout as the single stream owner', () => {
  it('opens exactly ONE stream for the tab, and does not reopen it on a state change', async () => {
    // The bug this exists to catch is silent and expensive: `useAttentionStream`
    // closes over `navigate`, so if TanStack Router handed back a fresh
    // reference on each render, the effect would tear down and reopen the
    // stream on every connection announcement — and every announcement is a
    // setState, so it would never stop. The visible symptom is nothing at all;
    // the invisible one is a connection per render against a server that holds
    // each one open.
    let opens = 0
    renderApp('/', [
      http.get('/api/v1/stream', () => {
        opens += 1
        return silentStream()
      }),
    ])
    await screen.findByRole('navigation')
    // Force a re-render through the exact state the hook itself sets: an
    // invalidation the layout observes. If the effect were unstable this is
    // where the second connection would appear.
    await queryClient.invalidateQueries({ queryKey: ['approvals'] })
    await screen.findByRole('navigation')
    expect(opens).toBe(1)
  })

  it('says NOTHING about the connection until the transport has spoken', async () => {
    // The plan started this hook at 'reconnecting', which puts "this view may
    // be out of date" on screen from the first paint of every page load —
    // over data the route loader fetched moments earlier. The indicator's own
    // rule is that a banner which is always present stops being read, so the
    // initial state is silence.
    //
    // `pendingStream` IS THE TEST. With the harness's default the stream is
    // already open by the time anything renders, so the transport has said
    // 'live' and the assertion holds for a reason that has nothing to do with
    // the initial state — MEASURED: this test passed with the initial state
    // flipped back to 'reconnecting'. Holding the opening window open is what
    // makes it a test of the value under discussion.
    renderApp('/', [pendingStream()])
    await screen.findByRole('navigation')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('closes the stream when the layout goes away', async () => {
    // An effect cleanup that forgot to close leaves an orphan connection AND
    // its watchdog interval behind — per unmount, for the life of the page,
    // against a server that holds each one open. Nothing about the visible UI
    // changes, which is exactly why it needs an assertion.
    //
    // Observed through the REQUEST's abort signal, not through the response
    // body's `cancel` — MEASURED: msw does not run a ReadableStream's cancel
    // callback when the client aborts, so a test written the obvious way would
    // report "not closed" for a perfectly good cleanup.
    let aborted = false
    renderApp('/', [
      http.get('/api/v1/stream', ({ request }) => {
        request.signal.addEventListener('abort', () => {
          aborted = true
        })
        return silentStream()
      }),
    ])
    await screen.findByRole('navigation')
    expect(aborted).toBe(false)
    cleanup()
    await waitFor(() => expect(aborted).toBe(true))
  })

  it('leaves the badge ABSENT when the approvals endpoint fails', async () => {
    // The documented degradation, stated as a test rather than as a comment:
    // the badge is chrome on every page, so a failing approvals endpoint must
    // not suspend or break the five pages that have nothing to do with it. It
    // renders nothing, which does read the same as an empty inbox — the cost is
    // real and it is why /approvals itself routes the failure to the boundary
    // instead.
    //
    // On /tasks, because /approvals would route this 500 to the error panel and
    // there would be no shell left to inspect.
    renderApp('/tasks', [
      http.get('/api/v1/approvals', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500 },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])
    await screen.findByRole('navigation')
    // The PAGE rendered too, not only the chrome — otherwise a shell wrapped
    // around a route that never resolved would satisfy this test. It was
    // `/not built yet/` while `/tasks` was a stub; Plan 12a gave the route a
    // loader over `GET /tasks` and `GET /runs`, neither of which this test
    // touches, so the heading is the same assertion against a real screen.
    await screen.findByRole('heading', { name: 'Up next' })
    expect(screen.queryByTestId('approvals-badge')).not.toBeInTheDocument()
  })

  it('sends a session lost ON THE STREAM to /login', async () => {
    // The transport classifies a 401 on the stream itself as terminal — the
    // one case where the connection cannot be retried into working. Nothing
    // else in the tree acts on it, so without the hook's `session-lost` arm the
    // tab sits on a dead stream with no indication and no navigation.
    //
    // Starting at /tasks is what makes the navigation observable while
    // /auth/me stays healthy: the boundary sends the user to /login, whose
    // guard proves the session and sends them to `/`. A hook that ignored the
    // event would leave them on /tasks.
    const router = renderApp('/tasks', [
      http.get('/api/v1/stream', () =>
        HttpResponse.json(
          { title: 'Authentication required', status: 401 },
          { status: 401, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])
    await screen.findByRole('navigation')
    // On the router's own state, not on rendered text. The subject here is
    // WHERE the tab ended up, and Home's rendering is Task 13's business —
    // asserting on whatever it happens to say would make this session test go
    // red for a reason that has nothing to do with the session.
    await waitFor(() => expect(router.state.location.pathname).toBe('/'))
  })

  it('does not open a stream at all for a visitor with no session', async () => {
    // The stream is inside the protected layout, so an unauthenticated visitor
    // must never reach it. Opening one anyway would spend a connection per
    // login-page view and get a 401 for it.
    let opens = 0
    renderApp('/login', [
      http.get('/api/v1/auth/me', noSession),
      http.get('/api/v1/stream', () => {
        opens += 1
        return silentStream()
      }),
    ])
    await screen.findByRole('button', { name: /sign in/i })
    expect(opens).toBe(0)
  })
})
