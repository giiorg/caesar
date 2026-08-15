import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { queryClient } from '../api/query-client'
import { routeTree } from '../routeTree.gen'
import { server } from '../test/msw'

const me = () =>
  HttpResponse.json({
    user: {
      id: 'u1',
      email: 'user@example.com',
      role: 'admin',
      active: true,
      created_at: '',
      updated_at: '',
    },
    grants: [{ project_id: 'p1', level: 'run' }],
  })
const noSession = () =>
  HttpResponse.json(
    { title: 'Authentication required', status: 401 },
    { status: 401, headers: { 'content-type': 'application/problem+json' } },
  )

function renderAt(path: string) {
  queryClient.clear()
  // BOTH arguments are load-bearing and neither is optional.
  //
  // createMemoryHistory with an initial entry, because a jsdom router has no
  // URL to read and `router.navigate` after construction races the first
  // render — the guard's whole job happens during that first load.
  //
  // context: { queryClient }, because the routeTree is created with
  // `createRootRouteWithContext<{ queryClient: QueryClient }>()` and the _app
  // loader dereferences `context.queryClient` to call ensureQueryData.
  // Omitting it is a type error, and past the cast a runtime one — and the
  // failure lands in the guard's own test, so it reads as the guard being
  // broken rather than the harness.
  const router = createRouter({
    routeTree,
    context: { queryClient },
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

describe('the protected layout', () => {
  it('sends a direct unauthenticated visit to a STUB route to /login', async () => {
    // The whole reason this layout exists: a stub makes no request to be
    // rejected, so without a loader it would render the full shell to anyone.
    server.use(http.get('/api/v1/auth/me', noSession))
    const router = renderAt('/tasks')
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()

    // AND IT MUST BE THE LOADER THAT DID IT. Without this line the test is
    // VACUOUS — MEASURED: neuter the loader's ensureQueryData and all three
    // tests still pass, because AppLayout's useSuspenseQuery then throws the
    // same 401 and AppErrorBoundary navigates to the same place. The visible
    // outcome is identical; the history is not.
    //
    // A loader `throw redirect()` during the initial load REPLACES the entry
    // (length 1). A navigate() from a rendered error boundary PUSHES one
    // (length 2), leaving /tasks behind /login in the back stack — so Back
    // returns the visitor to a route they cannot see and re-runs the whole
    // failed load. Resolving the session before anything renders is the
    // property this route exists for, and this is what makes it falsifiable.
    expect(router.history.length).toBe(1)
  })

  it('renders the shell on a reload with a valid cookie', async () => {
    server.use(http.get('/api/v1/auth/me', me))
    renderAt('/')
    expect(await screen.findByRole('navigation')).toBeInTheDocument()
  })

  it('sends an expired session back to /login', async () => {
    let calls = 0
    server.use(
      http.get('/api/v1/auth/me', () => {
        calls += 1
        return calls === 1 ? me() : noSession()
      }),
    )
    renderAt('/')
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
  // The plan wrote this against `/approvals` returning a 500. That route does
  // not exist until Task 11, and a memory-history visit to an unmatched path
  // renders a not-found, not the boundary — the assertion would have passed or
  // failed for reasons unrelated to the panel. A 500 from /auth/me reaches the
  // same place through the same wiring: the _app loader rethrows anything that
  // is not a 401, and `errorComponent` is what catches it.
  it('renders the correlation id when a protected route 500s', async () => {
    server.use(
      http.get('/api/v1/auth/me', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500, correlation_id: 'cid-99' },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    )
    renderAt('/')
    expect(await screen.findByText('cid-99')).toBeInTheDocument()
  })
})

describe('the /login route guard', () => {
  it('redirects an already-signed-in visitor inward', async () => {
    server.use(http.get('/api/v1/auth/me', me))
    const router = renderAt('/login')
    expect(await screen.findByRole('navigation')).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/')
  })

  it('leaves a visitor with no session on the form', async () => {
    server.use(http.get('/api/v1/auth/me', noSession))
    const router = renderAt('/login')
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
  })
})
