import { QueryClientProvider, useQuery } from '@tanstack/react-query'
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
 * A stream the TEST writes frames on — the third shape, beside `silentStream`
 * (opens, says nothing) and `pendingStream` (never answers).
 *
 * This is the only one a live-update test can use: it hands its controller back
 * so a frame can be written at a chosen moment, AFTER the screen under test has
 * rendered. Everything downstream of it is production code — the transport, the
 * cache bridge's debounce window and react-query's refetch — so a test built on
 * it asserts on the rendered DOM rather than on a request count.
 *
 * `emit` awaits the open, so a frame written before the transport's fetch has
 * been dispatched is queued rather than dropped on the floor. Without that a
 * test races the layout's effect and fails intermittently for a reason that has
 * nothing to do with its subject.
 */
export function scriptedStream() {
  const enc = new TextEncoder()
  let open!: (c: ReadableStreamDefaultController<Uint8Array>) => void
  const opened = new Promise<ReadableStreamDefaultController<Uint8Array>>((res) => {
    open = res
  })
  let seq = 0
  return {
    handler: http.get(
      '/api/v1/stream',
      () =>
        new HttpResponse(
          new ReadableStream({
            start: (c) => open(c),
          }),
          { headers: { 'content-type': 'text/event-stream' } },
        ),
    ),
    async emit(f: { kind: string; run_id: string }): Promise<void> {
      const c = await opened
      seq += 1
      const data = JSON.stringify({
        id: seq,
        run_id: f.run_id,
        kind: f.kind,
        payload: {},
        occurred_at: '2026-08-20T00:00:00Z',
      })
      c.enqueue(enc.encode(`id: ${seq}\nevent: ${f.kind}\ndata: ${data}\n\n`))
    },
  }
}

/**
 * Mount the real router at a path, with the real query client, against MSW.
 *
 * HANDLER ORDER IS MEASURED, NOT ASSUMED. Within a single `server.use(...)`
 * call msw gives priority to the EARLIER argument, and a later `server.use`
 * call outranks an earlier one. So the caller's handlers are spread first and
 * the defaults behind them, and a test can override any single endpoint
 * without restating the others. The tempting shape — tests calling
 * `server.use` themselves before `renderApp` — silently loses, because the
 * harness's own call would then be the later one.
 */
/**
 * A query options object, as loosely as this harness can state it and still
 * refuse something that is not one.
 *
 * `UseQueryOptions<unknown, …>` would be the tempting spelling and it does not
 * work: `queryFn` is contravariant in its context, so a concretely-typed
 * `queryOptions<Task[], …, ['tasks']>` is NOT assignable to the `unknown`/
 * `QueryKey` widening, and every caller would need a cast at its own site.
 * `never[]` parameters accept any function shape, so the constraint stays on
 * this side and the one cast lives with it.
 *
 * `queryFn` is OPTIONAL because `queryOptions()` returns it optional — MEASURED,
 * `tsc` refuses `tasksQueryOptions` against a required one. The key is what this
 * type really insists on, and it is the field the harness itself would use.
 */
type ObservableQuery = {
  queryKey: readonly unknown[]
  queryFn?: (...args: never[]) => unknown
}

/**
 * AN OBSERVER PER OPTION, mounted inside the same provider as the router.
 *
 * An invalidation reaches a query only if something is watching it, so a test
 * asserting "this endpoint was refetched N times" is measuring nothing unless
 * the query has a live observer. MEASURED on `renderApp('/runs/run-1')`: the
 * first paint fetches the run, its events, the inbox and `me` — and `['runs']`
 * and `['tasks']` sit at ZERO, because run detail reads neither. Any ceiling on
 * those two would pass against any bridge.
 *
 * A real `useQuery` rather than a hand-rolled `queryCache.subscribe`, because
 * the property under test is what the CACHE does for an observer the way the
 * app makes one — a subscription with different fetch semantics would answer a
 * different question convincingly.
 *
 * The hook-in-a-loop is safe here and only here: `observe` is fixed for the
 * lifetime of one `renderApp` call, so the hook order never changes.
 */
function Observers(props: { options: readonly ObservableQuery[] }) {
  return (
    <>
      {props.options.map((o) => (
        <Observer key={JSON.stringify(o.queryKey)} options={o} />
      ))}
    </>
  )
}

function Observer(props: { options: ObservableQuery }) {
  useQuery(props.options as Parameters<typeof useQuery>[0])
  return null
}

/**
 * @param observe query options to mount an observer for, ALONGSIDE the route.
 *   `['approvals']` never needs one: `AppLayout`'s badge observes it on every
 *   protected route.
 */
export function renderApp(
  path: string,
  handlers: RequestHandler[] = [],
  opts: { observe?: readonly ObservableQuery[] } = {},
) {
  queryClient.clear()
  server.use(
    ...handlers,
    http.get('/api/v1/auth/me', authenticatedMe),
    http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: [] })),
    // `runs: null`, not `[]`. A Go nil slice marshals to null and the contract
    // permits it, so this is a body the server may really send — the handler
    // happens to build `make(...)` today
    // (backend/internal/runs/api/dto.go:525), which is why the empty case is
    // the one nothing would otherwise ever exercise. It puts
    // `runsQueryOptions`'s `?? []` on the path of the first screen that mounts
    // a list, instead of leaving that branch unreachable from every test in the
    // tree. Task 13 mounted that screen — Home and `/runs` both read this
    // endpoint now — and the bet paid: with the `?? []` deleted, SEVEN tests
    // across `_app.test.tsx` and `approvals.test.tsx` die on `null.filter`,
    // every one of them a test that renders `/` and cares about something
    // else entirely.
    http.get('/api/v1/runs', () => HttpResponse.json({ runs: null, next_cursor: null })),
    // `events: null`, for the same reason `runs: null` is null above: the
    // contract marks it required AND nullable (a Go nil slice marshals to
    // null), api.go:721 happens to build a non-nil slice today, and the null
    // branch in `runEventsQueryOptions` would otherwise be unreachable from
    // every test in the tree. Every protected screen that mounts a run detail
    // fetches this, and `onUnhandledRequest: 'error'` turns a missing default
    // into a failure far from the missing handler.
    http.get('/api/v1/runs/:id/events', () => HttpResponse.json({ events: null })),
    // `tasks: null`, for the third time and the same reason: the contract marks
    // it required AND nullable because a Go nil slice marshals to null,
    // `handleListTasks` happens to build a non-nil slice today, and the null
    // branch in `tasksQueryOptions` would otherwise be unreachable from every
    // test in the tree.
    http.get('/api/v1/tasks', () => HttpResponse.json({ tasks: null })),
    // `decisions: null`, for the fourth time and the same reason: the contract
    // marks it required AND nullable because a Go nil slice marshals to null,
    // `handleListDecisions` happens to build a non-nil slice today, and the
    // null branch in `decisionsInfiniteQueryOptions` would otherwise be
    // unreachable from every test in the tree. Unlike the three above, only the
    // History tab fetches this — so this default is what the history-empty
    // assertion in `approvals/decision-history.test.tsx` runs against, rather
    // than a stub that never sees a reader.
    http.get('/api/v1/approvals/decisions', () =>
      HttpResponse.json({ decisions: null, next_cursor: null }),
    ),
    // `projects: null`, for the FIFTH time and the same reason: required AND
    // nullable, because a Go nil slice marshals to null; `handleListProjects`
    // happens to build a non-nil slice today
    // (backend/internal/assets/api/api.go); and the null branch in
    // `projectsQueryOptions` would otherwise be unreachable from every test in
    // the tree. Two screens read it — the Repos list and the new-task form's
    // project select — so this default is what the Repos empty state runs
    // against rather than a stub nothing observes.
    http.get('/api/v1/projects', () => HttpResponse.json({ projects: null })),
    // `repos: null` and `credentials: null`, both `| null` in the contract for
    // the same nil-slice reason.
    //
    // THERE IS DELIBERATELY NO DEFAULT FOR `GET /projects/{id}` beside these.
    // A list has an honest empty answer; ONE project does not — a default would
    // have to invent a name and a description, and every test that renders the
    // detail screen supplies its own project anyway. `onUnhandledRequest:
    // 'error'` is what makes that omission loud instead of silent.
    http.get('/api/v1/projects/:id/repos', () => HttpResponse.json({ repos: null })),
    // `grants: null`, for the SIXTH time and the same reason: required AND
    // nullable, because a Go nil slice marshals to null.
    //
    // It is also the default every test renders the project detail against —
    // `authenticatedMe` is an admin, so `canManageProject` is true and the
    // grants section mounts on every one of them. `grants-section.test.tsx`'s
    // empty-state test deliberately does NOT override it, so this default is
    // observed rather than merely present: with `?? []` deleted from
    // `grantsQueryOptions` that test dies on `null.length`. Task 19 added a
    // default that every one of its own tests then overrode, and the coalesce
    // it was supposed to exercise sat on no live path while a comment claimed
    // otherwise.
    http.get('/api/v1/projects/:id/grants', () => HttpResponse.json({ grants: null })),
    http.get('/api/v1/credentials', () => HttpResponse.json({ credentials: null })),
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
      <Observers options={opts.observe ?? []} />
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}
