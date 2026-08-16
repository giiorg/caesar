import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router'
import { render } from '@testing-library/react'
import { delay, HttpResponse, http, type RequestHandler } from 'msw'
import { queryClient } from '../api/query-client'
import { routeTree } from '../routeTree.gen'
import { server } from './msw'

/** A signed-in session with one grant. Overridable; see `renderApp`. */
export const authenticatedMe = () =>
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

export const noSession = () =>
  HttpResponse.json(
    { title: 'Authentication required', status: 401 },
    { status: 401, headers: { 'content-type': 'application/problem+json' } },
  )

/**
 * A stream that OPENS AND SAYS NOTHING — headers, then silence.
 *
 * This is what most tests want from the transport: mounting the protected
 * layout opens a connection whether or not the test cares about one, and
 * `onUnhandledRequest: 'error'` means an unstubbed /stream fails the test
 * somewhere far from the missing handler. Silence rather than an immediate
 * close, because a closed stream sends the transport straight into its backoff
 * loop and every test would then be racing a reconnect it never asked for.
 */
export const silentStream = () =>
  new HttpResponse(new ReadableStream({ start() {} }), {
    headers: { 'content-type': 'text/event-stream' },
  })

/**
 * A stream that never even ANSWERS — no headers, ever.
 *
 * `silentStream` is already open, so the transport announces 'live' the moment
 * its headers land, and any assertion about the state BEFORE a connection
 * exists is vacuous against it — MEASURED: the pre-connection silence test
 * passed identically with the initial state flipped to 'reconnecting', because
 * by the time the shell rendered the transport had already spoken. This is the
 * handler that holds the opening window open long enough to assert about it.
 */
export const pendingStream = () =>
  http.get('/api/v1/stream', async () => {
    await delay('infinite')
    return silentStream()
  })

/**
 * Mount the real router at a path, with the real query client, against MSW.
 *
 * HANDLER ORDER IS MEASURED, NOT ASSUMED. Within a single `server.use(...)`
 * call msw gives priority to the EARLIER argument, and a later `server.use`
 * call outranks an earlier one. So the caller's handlers are spread first and
 * the defaults behind them, and a test can override any single endpoint
 * without restating the other three. The tempting shape — tests calling
 * `server.use` themselves before `renderApp` — silently loses, because the
 * harness's own call would then be the later one.
 */
export function renderApp(path: string, handlers: RequestHandler[] = []) {
  queryClient.clear()
  server.use(
    ...handlers,
    http.get('/api/v1/auth/me', authenticatedMe),
    http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: [] })),
    // `runs: null`, not `[]`. A Go nil slice marshals to null and the contract
    // permits it, so this is a body the server may really send — the handler
    // happens to build `make(...)` today
    // (backend/internal/runs/api/dto.go:487), which is why the empty case is
    // the one nothing would otherwise ever exercise. Picking the permitted
    // shape costs nothing now (no route fetches /api/v1/runs yet) and puts
    // `runsQueryOptions`'s `?? []` on the path of the first screen that
    // mounts a list, instead of leaving that branch unreachable from every
    // test in the tree.
    http.get('/api/v1/runs', () => HttpResponse.json({ runs: null, next_cursor: null })),
    http.get('/api/v1/stream', silentStream),
  )
  // BOTH router arguments are load-bearing and neither is optional.
  //
  // createMemoryHistory with an initial entry, because a jsdom router has no
  // URL to read and `router.navigate` after construction races the first
  // render — the session guard's whole job happens during that first load.
  //
  // context: { queryClient }, because the routeTree is created with
  // `createRootRouteWithContext<{ queryClient: QueryClient }>()` and both the
  // `_app` and `/approvals` loaders dereference `context.queryClient`.
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
