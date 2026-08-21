import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { Run } from '../../runs/run-query'
import { noSession, renderApp } from '../../test/render-app'

const approval = {
  run_id: 'r1',
  task_id: 't1',
  task_title: 'Add a thing',
  node_id: 'gate',
  project_id: 'p1',
  requester_id: 'u1',
  attempt: 1,
  generation: 1,
  waiting_since: '2026-08-15T00:00:00Z',
}

const evidence = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: null,
  can_decide: true,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

/**
 * `GET /runs/run-1` as the deep link reads it: one waiting gate phase carrying
 * the same evidence the inbox row carries.
 *
 * Annotated `Run` so a field added to or renamed on ApiRunResponse reddens
 * `tsc --noEmit` here rather than arriving at the surface as a missing
 * property. `evidence` is spread in as a separate literal because the
 * generated `ApiGateEvidenceResponse` is what the phase's property is typed
 * to, and writing it inline twice would be two places to keep true.
 */
const waitingRun = (): Run => ({
  id: 'run-1',
  task_id: 't1',
  task_title: 'gate on A',
  repo_slug: 'acme/app',
  workflow_name: 'default',
  status: 'running',
  definition_hash: 'h1',
  base_sha: 'abc',
  policy_snapshot: {},
  generation: 1,
  execution_used_ms: 62_000,
  wall_deadline: '2099-01-01T00:00:00Z',
  created_at: '2026-08-20T00:00:00Z',
  started_at: '2026-08-20T00:00:00Z',
  finished_at: null,
  artifacts: null,
  publications: null,
  phases: [
    {
      node_id: 'gate',
      attempt: 1,
      max_attempts: 1,
      status: 'running',
      generation: 1,
      created_at: '2026-08-20T00:00:00Z',
      finished_at: null,
      evidence,
    },
  ],
})

const PATCH_B64 = btoa('--- a/one.txt\n+++ b/one.txt\n@@ -1 +1 @@\n-old\n+new\n')

/**
 * A run as the WIRE carries it, which is not what the generated type can say.
 *
 * `dto.go:182` and `:191` declare a phase's `decision` and `evidence` as
 * pointers with NO `omitempty`, so an absent one marshals to `null` and never
 * to a missing key — the DTO's own comment says the property "really is null on
 * every non-gate phase and on every decided one". The generated types render
 * both optional-and-NON-nullable (a `$ref` property carries no null branch in
 * this toolchain), so a `Run`-annotated fixture cannot write `null` at all.
 *
 * A fixture that leaves the key out instead is testing a body the server never
 * sends, and it makes `!phase.evidence` and `phase.evidence !== undefined`
 * indistinguishable — MEASURED: with the key merely absent, swapping the deep
 * route to `=== undefined` left the whole suite green. This puts the true shape
 * on the wire while the `Run` annotation above keeps every other field honest.
 */
const onTheWire = (run: Run) => ({
  ...run,
  // Spread LAST, so a phase that really carries one keeps it and only the
  // absent ones become null.
  phases: (run.phases ?? []).map((phase) => ({ decision: null, evidence: null, ...phase })),
})

/**
 * Everything the deep link touches: the inbox row that renders the card, the
 * run its loader reads, and the patch the surface decodes. Returned as an
 * ARRAY and spread, because `renderApp` takes a handler list and this journey
 * crosses three endpoints — one of which (`/runs/run-1`) has no default
 * handler, and `onUnhandledRequest: 'error'` would fail the test somewhere far
 * from the missing one.
 */
const oneWaitingGate = () => [
  http.get('/api/v1/approvals', () =>
    HttpResponse.json({
      approvals: [
        { ...approval, run_id: 'run-1', task_title: 'gate on A', node_id: 'gate', evidence },
      ],
    }),
  ),
  http.get('/api/v1/runs/run-1', () => HttpResponse.json(onTheWire(waitingRun()))),
  http.get('/api/v1/runs/:id/artifacts/:artifactId', () =>
    HttpResponse.json({
      id: 'a1',
      run_id: 'run-1',
      node_id: 'gate',
      type: 'patch',
      attempt: 1,
      generation: 1,
      parent_ids: [],
      payload: { diff: PATCH_B64 },
    }),
  ),
]

const frame = (id: number, kind: string) =>
  `id: ${id}\nevent: ${kind}\ndata: ${JSON.stringify({
    id,
    run_id: 'r1',
    kind,
    payload: {},
    occurred_at: '',
  })}\n\n`

/** A stream that opens and then writes `body` after a tick. */
const streamOf = (body: string) =>
  new HttpResponse(
    new ReadableStream({
      start(c) {
        const enc = new TextEncoder()
        setTimeout(() => c.enqueue(enc.encode(body)), 10)
      },
    }),
    { headers: { 'content-type': 'text/event-stream' } },
  )

describe('/approvals', () => {
  it('renders the list and moves the badge when a frame arrives, with no user action', async () => {
    let count = 0
    renderApp('/approvals', [
      http.get('/api/v1/approvals', () =>
        HttpResponse.json({ approvals: count === 0 ? [] : [approval] }),
      ),
      http.get('/api/v1/stream', () => {
        count = 1
        return streamOf(frame(1, 'gate.waiting'))
      }),
    ])
    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
    // No click, no refresh: the assertion is on the RENDERED badge, never on a
    // request count. A refetch is the mechanism, and forbidding it would assert
    // against the design.
    //
    // Scoped to the phone tab bar because the shell renders BOTH presentations
    // at once and CSS picks one; jsdom applies no media queries, so an unscoped
    // query finds two badges and throws. The bar rather than the rail: this
    // suite's subject is the flagship phone surface, which is also the only
    // viewport e2e/approvals.spec.ts runs.
    expect(
      await within(screen.getByTestId('phone-tabs')).findByTestId('approvals-badge'),
    ).toHaveTextContent('1')
    expect(await screen.findByText('Add a thing')).toBeInTheDocument()
  })

  it('has its list in hand by the time the shell is on screen', async () => {
    // Asserted synchronously after the chrome appears: the route and the
    // chrome commit together rather than the list arriving a paint later.
    //
    // THIS DOES NOT PROVE THE LOADER, and saying so would be the third guard in
    // this tree to be green for the wrong reason. MEASURED: delete the loader
    // and this still passes, along with every other test in the file — the
    // component's `useSuspenseQuery` reaches the same cache and its throw
    // reaches the same boundary, and under jsdom React resolves the suspension
    // inside the same act() flush. The loader's real payoff is a router-level
    // load that `defaultPreload: 'intent'` can start on hover, and nothing here
    // can see it, partly because AppLayout's own badge query has already asked
    // for the same key. Recorded as unfalsifiable rather than dressed up.
    renderApp('/approvals')
    await screen.findByRole('navigation')
    expect(screen.getByText(/nothing is waiting on you/i)).toBeInTheDocument()
  })

  it('renders no badge at all while the inbox is empty', async () => {
    renderApp('/approvals')
    await screen.findByText(/nothing is waiting on you/i)
    // A badge reading "0" is a call to action for nothing — the nav's own rule,
    // and the reason the count is not simply always rendered.
    expect(screen.queryByTestId('approvals-badge')).not.toBeInTheDocument()
  })

  it('survives the null list the contract permits', async () => {
    // `approvals` is required AND nullable in the generated types, because a Go
    // nil slice marshals to null. Today's handler builds a non-nil empty slice
    // so the wire carries `[]` — which means every other test in this file
    // would pass against a client that reads `.length` straight off the
    // response and throws a TypeError on the one shape the contract allows.
    // The failing case would be a user's very first visit.
    renderApp('/approvals', [
      http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: null })),
    ])
    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
  })

  it('says the SCOPE is empty, not the inbox, when the stream proves there are no grants', async () => {
    // The whole forbidden path, end to end and for real: the server sends the
    // one ambiguous frame it has, the transport probes /auth/me rather than
    // concluding, the probe comes back with zero grants, and only then does the
    // client say something about the user's access. `scopeEmpty` is the answer
    // travelling from the transport through context into a route that never
    // opened a connection of its own.
    renderApp('/approvals', [
      http.get('/api/v1/auth/me', () =>
        HttpResponse.json({
          user: {
            id: 'u1',
            email: 'user@example.com',
            role: 'member',
            active: true,
            created_at: '',
            updated_at: '',
          },
          grants: [],
        }),
      ),
      http.get('/api/v1/stream', () => streamOf('event: forbidden\ndata: {}\n\n')),
    ])
    expect(await screen.findByText(/no projects are shared with you yet/i)).toBeInTheDocument()
    // And the chrome admits the stream is over, which is the other half of the
    // same frame: 'stopped' is terminal, so the honest banner is "reload", not
    // "reconnecting".
    expect(await screen.findByText(/live updates stopped/i)).toBeInTheDocument()
  })

  it('a card opens the surface at a linkable URL', async () => {
    // URL-AS-STATE, and the reason it is a URL rather than a drawer: this is
    // the screen a notification sends somebody to, and a gate under review is
    // exactly the thing they will want to send on to a colleague. The
    // assertion is on `router.state.location.pathname` and NOT only on the
    // rendered surface, because a modal opened from the card would render the
    // same DOM while leaving the address bar on /approvals.
    //
    // `hard-facts` arriving is the second half: the deep route really loads
    // the run, finds the phase whose node_id matches, and hands its evidence
    // to the surface. A route that resolved but rendered nothing would still
    // move the pathname.
    const router = renderApp('/approvals', oneWaitingGate())
    await userEvent.click(await screen.findByRole('link', { name: /gate on A/ }))
    expect(router.state.location.pathname).toBe('/approvals/run-1/gate')
    expect(await screen.findByTestId('hard-facts')).toBeInTheDocument()
  })

  it('names whoever decided a gate that is no longer waiting', async () => {
    // The 409-after-the-fact case, reached by URL rather than by click: a
    // notification arrives, the reader opens it, and somebody else has already
    // decided. `ApiPhaseResponse.evidence` is absent on a phase that is no
    // longer waiting (dto.go builds it only for a waiting gate), so the deep
    // route has nothing to hand the surface — and an empty approval screen
    // would read as a gate waiting on the reader.
    const decided = waitingRun()
    decided.phases = [
      {
        node_id: 'gate',
        attempt: 1,
        max_attempts: 1,
        status: 'succeeded',
        generation: 1,
        created_at: '2026-08-20T00:00:00Z',
        finished_at: '2026-08-20T00:01:00Z',
        decision: {
          decision: 'approve',
          note: '',
          acted_by_id: 'u2',
          acted_by_email: 'alice@example.com',
          acted_at: '2026-08-20T00:01:00Z',
          self_approval: false,
          examined_artifact_id: 'a1',
        },
      },
    ]
    renderApp('/approvals/run-1/gate', [
      http.get('/api/v1/runs/run-1', () => HttpResponse.json(onTheWire(decided))),
    ])
    expect(await screen.findByText(/alice@example\.com/)).toBeInTheDocument()
    expect(screen.queryByTestId('hard-facts')).toBeNull()
  })

  it("routes a 401 from THIS route's loader through /login", async () => {
    // THE BRANCH THIS REACHES HAS BEEN UNREACHABLE UNTIL NOW. `_app`'s
    // AppErrorBoundary handles a 401 arriving from a CHILD loader — not the
    // first load's, which the layout loader redirects, and not a background
    // expiry, which AppLayout reads off its own query — and until this task no
    // route had a loader of its own to produce one. The ledger recorded it as
    // "reasoned, not measured"; this is the measurement.
    //
    // /auth/me DELIBERATELY STAYS HEALTHY, and that is what isolates the
    // branch. Expire the session instead and three mechanisms could produce
    // the same redirect — the layout loader, AppLayout's expiry check, and this
    // boundary — which is the trap Task 7 already fell into once. With the
    // session genuinely valid, the boundary is the only thing in the tree that
    // can act on this 401.
    //
    // Landing back on `/` is the whole round trip and not an accident: the
    // boundary sends the user to /login, /login's own guard proves the session
    // with a fresh request and sends them inward again. A client that simply
    // rendered the router's default error would sit on /approvals forever.
    //
    // The global redirect interceptor is NOT what does this: it is armed in
    // `router.ts`, which only `main.tsx` imports, so under this harness
    // `redirectToLogin` is still its no-op default.
    const router = renderApp('/approvals', [http.get('/api/v1/approvals', noSession)])
    expect(await screen.findByText(/not built yet/i)).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/')
  })

  it('shows the correlation id when the approvals endpoint 500s', async () => {
    // The other side of the same boundary: a non-401 keeps the user where they
    // are and shows them something they can quote to whoever runs the server.
    //
    // The generous timeout is not slack, it is the retry policy being honest:
    // `approvalsQueryOptions` inherits the client's default, which retries a
    // 5xx twice with react-query's exponential backoff (~1s, then ~2s). So a
    // server error costs a real user about three seconds of pending route
    // before the panel appears. `meQueryOptions` opts out of that with
    // `retry: false` because a 401 there is an ANSWER; a 500 here is not.
    renderApp('/approvals', [
      http.get('/api/v1/approvals', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500, correlation_id: 'cid-approvals' },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])
    expect(await screen.findByText('cid-approvals', {}, { timeout: 8_000 })).toBeInTheDocument()
  }, 15_000)
})
