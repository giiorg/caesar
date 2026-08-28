import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { GateEvidence } from '../../gates/decide-gate'
import { expectDecisionRules } from '../../gates/decision-rules.testkit'
import type { RunSummary } from '../../runs/runs-query'
import { renderApp, scriptedStream } from '../../test/render-app'

type PhaseSummary = NonNullable<RunSummary['phase_summary']>[number]

const AT = '2026-08-20T00:00:00Z'

/** A gate this session may decide, so the three controls render. */
const decidable: GateEvidence = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: null,
  can_decide: true,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

/**
 * One inbox row. `run-0` is also `mixed()`'s first run, and the two carry the
 * SAME task title — which is what the wire really does, since both read it off
 * the same task. Every query for that title is therefore scoped to a card.
 */
const approvalRow = (i: number, evidence: GateEvidence) => ({
  run_id: `run-${i}`,
  task_id: `t${i}`,
  task_title: i === 0 ? 'gate on A' : `waiting ${i}`,
  node_id: 'approval',
  project_id: 'p1',
  requester_id: 'u1',
  attempt: 1,
  generation: 1,
  waiting_since: AT,
  evidence,
})

const inbox = (n: number, evidence: GateEvidence = decidable) =>
  http.get('/api/v1/approvals', () =>
    HttpResponse.json({
      approvals: Array.from({ length: n }, (_, i) => approvalRow(i, evidence)),
    }),
  )

/**
 * A run as a list ROW, annotated `RunSummary` so `tsc --noEmit` forces every
 * key the contract declares.
 *
 * The annotation is the WHOLE honesty mechanism here, unlike the full run in
 * `on-the-wire.testkit.ts`. `dto.go:104-149` puts `omitempty` on nothing, so
 * `started_at`, `finished_at` and a phase's `status` arrive as an explicit
 * `null` and never as a missing key — and every one of those properties is a
 * scalar or a slice rather than a `$ref`, so the generated type really does say
 * `null | string` and this fixture can write the shape the server sends.
 * `on-the-wire.testkit.ts` exists because a `$ref` property gets no null branch
 * in this toolchain; nothing in THIS projection is a `$ref`, so there is no
 * shape the wire carries that a `RunSummary`-annotated literal cannot express.
 */
const runRow = (over: Partial<RunSummary> & Pick<RunSummary, 'id'>): RunSummary => ({
  task_id: 't1',
  task_title: 'a run',
  project_id: 'p1',
  project_name: 'caesar',
  repo_slug: 'acme/app',
  workflow_name: 'default',
  status: 'running',
  attempt: 1,
  generation: 1,
  requester_id: 'u1',
  created_at: AT,
  started_at: AT,
  finished_at: null,
  // A DEFAULT SHAPE, not an empty one, so that every card in every fixture
  // below really does render a nested <ul> of dots. With an empty default the
  // card-counting assertions would pass by accident and would break the moment
  // a later fixture gave a run some phases.
  phase_summary: [dot('build:succeeded:implement')],
  ...over,
})

/**
 * `node:status:type` → one dot. `null` is the literal wire null.
 *
 * THE NODE ID AND THE NODE TYPE ARE DELIBERATELY DIFFERENT WORDS in every
 * fixture below, and that is what makes the `data-node-type` assertion mean
 * something: a component that inferred "the node called gate is the gate" from
 * the id would pass against a fixture where the two agree, and would be wrong
 * on the first workflow that names its gate `approval` — which is the shape
 * used here.
 */
const dot = (spec: string): PhaseSummary => {
  const [nodeId, status, nodeType] = spec.split(':')
  return {
    node_id: nodeId as string,
    node_type: nodeType as string,
    status: status === 'null' ? null : (status as string),
    attempt: 1,
    generation: 1,
  }
}

const runWith = (specs: string[], over: Partial<RunSummary> = {}): RunSummary =>
  runRow({ id: 'run-1', task_title: 'still going', phase_summary: specs.map(dot), ...over })

/**
 * A page with both halves of Home in it: two runs the "Running" section claims
 * — one `running` and one `waiting`, which is why that section is not a
 * `status === 'running'` test — and two finished ones for "Recent".
 */
const mixed = (): RunSummary[] => [
  runRow({
    id: 'run-0',
    task_title: 'gate on A',
    status: 'waiting',
    phase_summary: [dot('build:succeeded:implement'), dot('approval:waiting_decision:gate')],
  }),
  runRow({ id: 'run-1', task_title: 'still going', status: 'running' }),
  // THE TWO FINISHED RUNS ARRIVE IN THE WRONG ORDER FOR "Recent", DELIBERATELY.
  // The server pages by `created_at` and these two share one; what separates
  // them is `finished_at`, and 'broke' finished two hours BEFORE 'shipped'. So
  // the order this fixture arrives in is the order a component that simply kept
  // the server's would render — which is what makes the newest-first assertion
  // falsifiable. MEASURED: with 'shipped' first in this array, deleting the
  // sort left that test green.
  runRow({
    id: 'run-3',
    task_title: 'broke',
    status: 'failed',
    finished_at: '2026-08-20T01:00:00Z',
  }),
  runRow({
    id: 'run-2',
    task_title: 'shipped',
    status: 'succeeded',
    finished_at: '2026-08-20T03:00:00Z',
  }),
]

const runs = (rows: RunSummary[] | (() => RunSummary[])) =>
  http.get('/api/v1/runs', () =>
    HttpResponse.json({
      runs: typeof rows === 'function' ? rows() : rows,
      next_cursor: null,
    }),
  )

describe('Home', () => {
  it('is state-first: status line, needs you, running, recent — in that order', async () => {
    renderApp('/', [inbox(2), runs(mixed())])
    const order = (await screen.findAllByRole('heading')).map((h) => h.textContent)
    expect(order).toEqual(['Needs you', 'Running', 'Recent'])
    // The status line is above the first heading and is NOT one: locked Home A
    // opens with a sentence about the situation, not with a section.
    expect(
      screen
        .getByTestId('status-line')
        .compareDocumentPosition(screen.getAllByRole('heading')[0] as Node),
    ).toBe(Node.DOCUMENT_POSITION_FOLLOWING)
  })

  it('states the counts and nothing it cannot know', async () => {
    // Locked Home A opens with an assistant-generated situation résumé. The
    // assistant is row 13 and unbuilt, so this is a COUNT LINE — computed from
    // what is already on screen. A stub never fakes success, and a plausible
    // one-line narrative is the most convincing fake available here.
    renderApp('/', [inbox(1), runs(mixed())])
    const line = await screen.findByTestId('status-line')
    expect(line).toHaveTextContent(/2 running/)
    expect(line).toHaveTextContent(/1 waiting on you/)
  })

  it('says so plainly when there is nothing to count', async () => {
    renderApp('/', [inbox(0), runs([])])
    expect(await screen.findByTestId('status-line')).toHaveTextContent(
      /nothing is running, and nothing needs a decision from you/i,
    )
  })

  it('draws a dot per node, typed, with the gate distinguishable', async () => {
    // A row of dots, not one dot. A scalar "current phase" cannot render the
    // run's shape, which is why phase_summary is an array on the wire.
    renderApp('/', [
      inbox(0),
      runs([runWith(['build:succeeded:implement', 'check:running:review', 'approval:null:gate'])]),
    ])
    const dots = within(await screen.findByTestId('phase-dots')).getAllByRole('listitem')
    expect(dots).toHaveLength(3)
    expect(dots[0]).toHaveAttribute('data-state', 'succeeded')
    expect(dots[1]).toHaveAttribute('data-state', 'running')
    // Not materialized: a DIM dot, not a queued one. `pending` means a row
    // exists and is claimable; null means no row exists.
    expect(dots[2]).toHaveAttribute('data-state', 'not-started')
    expect(dots[2]).toHaveAttribute('data-node-type', 'gate')
  })

  it('says a run has no shape rather than drawing an empty row of dots', async () => {
    // `phase_summary` is required AND nullable on the wire, and a Go nil slice
    // marshals to null. An empty <ul> would render as a card claiming a run
    // with no work in it — the "plausible empty content" a stub never fakes.
    renderApp('/', [inbox(0), runs([runRow({ id: 'run-1', phase_summary: null })])])
    expect(await screen.findByText(/no phases reported/i)).toBeInTheDocument()
    expect(screen.queryByTestId('phase-dots')).toBeNull()
  })

  it('reads the run list the WIRE may really send: `runs` is null, not []', async () => {
    // NO `runs` HANDLER, so `render-app.tsx`'s default answers
    // `{ runs: null, next_cursor: null }` — a body the contract permits and a
    // Go nil slice really marshals to. `runsQueryOptions`'s `?? []` is what
    // stands between that and `runs.filter(...)`. MEASURED with the `?? []`
    // deleted: `tsc --noEmit` reddens with five errors across the two screens
    // and the runtime throws `TypeError: Cannot read properties of null
    // (reading 'filter')`. Home's own suite pins it here rather than borrowing
    // the seven tests in `_app.test.tsx` and `approvals.test.tsx` that happen
    // to render `/` on the defaults while caring about something else.
    renderApp('/', [inbox(0)])
    expect(await screen.findByText(/no runs yet/i)).toBeInTheDocument()
  })

  it('gate cards carry the three controls inline, as locked', async () => {
    renderApp('/', [inbox(1), runs(mixed())])
    const card = await screen.findByTestId('gate-card')
    for (const name of [/^approve$/i, /request changes/i, /^reject$/i]) {
      expect(within(card).getByRole('button', { name })).toBeInTheDocument()
    }
  })

  it('and the card body opens the run AT THAT GATE, already expanded', async () => {
    // "The detail page is a choice, not a toll." Locked §3.2 says tapping the
    // body opens the run's EXPANDED GATE PHASE — not the top of a timeline the
    // person then has to find the gate in, which on a phone is a scroll and a
    // second tap for the screen they were already looking at.
    //
    // The expansion is URL state (`?phase=<node_id>`), per the constitution: a
    // gate under review is exactly the thing a person links to somebody else,
    // and React state would make that link land on a collapsed timeline.
    //
    // Scoped to the card because the run this gate belongs to also appears
    // under "Running" carrying the same task title — which is the wire's own
    // shape, not a fixture convenience.
    const router = renderApp('/', [inbox(1), runs(mixed())])
    const card = await screen.findByTestId('gate-card')
    await userEvent.click(within(card).getByRole('link', { name: /gate on A/ }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/runs/run-0'))
    expect(router.state.location.search).toMatchObject({ phase: 'approval' })
  })

  it('says whether a gate is a plan or a patch before offering a decision', async () => {
    // A card that offers a decision should say what is being decided. This is
    // why examined_artifact_type is on the inbox row and not only on the run.
    renderApp('/', [inbox(1, { ...decidable, examined_artifact_type: 'plan' }), runs(mixed())])
    expect(await screen.findByTestId('gate-card')).toHaveTextContent(/plan/i)
  })

  it('has two distinct empty states', async () => {
    renderApp('/', [inbox(0), runs([])])
    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
    expect(screen.getByText(/no runs yet/i)).toBeInTheDocument()
  })

  it('says the SCOPE is empty, not the inbox, when the stream proves there are no grants', async () => {
    // The constitution's rule, inherited from ApprovalList: "nothing is waiting
    // on you" is a statement about the inbox and "no projects are shared with
    // you yet" is a statement about grants, and telling somebody with no access
    // that they are all caught up is the one reading that makes Home useless to
    // the person it is happening to.
    renderApp('/', [
      inbox(0),
      runs([]),
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
      http.get(
        '/api/v1/stream',
        () =>
          new HttpResponse('event: forbidden\ndata: {}\n\n', {
            headers: { 'content-type': 'text/event-stream' },
          }),
      ),
    ])
    expect(await screen.findByText(/no projects are shared with you yet/i)).toBeInTheDocument()
    expect(screen.queryByText(/nothing is waiting on you/i)).toBeNull()
  })

  it('keeps the screen up when the INBOX cannot be read, and says which half is missing', async () => {
    // `/` IS WHERE `/login`'s GUARD SENDS EVERY AUTHENTICATED VISITOR, so a
    // Home that can throw from its own data is a Home that can make the app
    // unenterable: the failure reaches `_app`'s boundary, the boundary
    // navigates to /login, the guard proves the session and sends the visitor
    // back to `/`. MEASURED as an out-of-memory crash, not as a failing
    // assertion — see this route's own doc comment.
    //
    // The status line is the second half of the assertion: "0 waiting on you"
    // over an inbox that failed to load is the one lie on that line a reader
    // would act on.
    renderApp('/', [
      http.get('/api/v1/approvals', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500, correlation_id: 'cid-inbox' },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
      runs(mixed()),
    ])
    expect(await screen.findByText('cid-inbox', {}, { timeout: 8_000 })).toBeInTheDocument()
    expect(screen.getByTestId('recent-runs')).toBeInTheDocument()
    expect(screen.getByTestId('status-line')).toHaveTextContent(/2 running/)
    expect(screen.getByTestId('status-line')).toHaveTextContent(/your inbox could not be read/i)
    expect(screen.queryByText(/nothing is waiting on you/i)).toBeNull()
  }, 15_000)

  it('keeps the screen up when the RUN LIST cannot be read', async () => {
    renderApp('/', [
      inbox(1),
      http.get('/api/v1/runs', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500, correlation_id: 'cid-runs' },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])
    expect(await screen.findByText('cid-runs', {}, { timeout: 8_000 })).toBeInTheDocument()
    expect(screen.getByTestId('gate-card')).toBeInTheDocument()
    expect(screen.getByTestId('status-line')).toHaveTextContent(/1 waiting on you/)
    // Not "No runs yet." — that is a statement about the system, and this
    // screen does not know it.
    expect(screen.queryByText(/no runs yet/i)).toBeNull()
  }, 15_000)

  it('reads recent newest first, by the timestamp the card itself shows', async () => {
    // `finished_at ?? started_at ?? created_at` is what a RunCard renders, so
    // it is what "newest" has to mean here — a section ordered by one value and
    // captioned with another is a section whose order nobody can check. See
    // `mixed()` for why its two finished runs arrive in the opposite order.
    renderApp('/', [inbox(0), runs(mixed())])
    const recent = within(await screen.findByTestId('recent-runs')).getAllByTestId('run-card')
    expect(recent.map((r) => r.textContent)).toEqual([
      expect.stringContaining('shipped'),
      expect.stringContaining('broke'),
    ])
  })

  it('says it is showing ten when the cap drops rows, rather than truncating silently', async () => {
    // `log()` nothing and hide nothing. A list that quietly stops at ten reads
    // as "this is everything", which is the one thing it is not.
    const many = Array.from({ length: 12 }, (_, i) =>
      runRow({
        id: `done-${i}`,
        task_title: `done ${i}`,
        status: 'succeeded',
        finished_at: `2026-08-2${i % 10}T00:00:0${i % 10}Z`,
      }),
    )
    renderApp('/', [inbox(0), runs(many)])
    const recent = within(await screen.findByTestId('recent-runs')).getAllByTestId('run-card')
    expect(recent).toHaveLength(10)
    expect(screen.getByText(/showing the 10 most recent/i)).toBeInTheDocument()
  })

  it('does not claim a cap it did not apply', async () => {
    renderApp('/', [inbox(0), runs(mixed())])
    await screen.findByTestId('recent-runs')
    expect(screen.queryByText(/showing the 10 most recent/i)).toBeNull()
  })

  it('redraws when a phase transition arrives, with no user action — on the REAL throttle', async () => {
    // THE FIRST OBSERVATION OF THE `['runs', filters]` PREFIX MATCH, which
    // `runs-query.ts` and `cache-bridge.ts` have each claimed in a comment and
    // nothing has asserted. The bridge invalidates the BARE `['runs']`; this
    // screen mounts `['runs', {}]`; react-query matches by prefix unless
    // `exact` is set. Spelling the filters into the first element
    // (`['runs:waiting']`) would make that prefix match nothing, and the only
    // symptom would be a list quietly as stale as the refresh floor allows.
    //
    // THE SLOWEST TEST IN THE TREE, DELIBERATELY, since Task 12. A `phase.*`
    // frame now reaches this list on the throttled tier, so the wait is 250ms
    // of debounce plus PHASE_TIER_THROTTLE_MS — about 5.3 seconds of real
    // clock, through the PRODUCTION bridge with its own timers. That cost is
    // the whole value: decisions §7 locks Home's live phase dots, 5s is a bet
    // against that lock, and this is the only thing in the tree that would
    // notice the constant being retuned to the 60s refresh floor. Everything
    // faster — `cache-bridge.test.ts`, `refetch-fan.test.tsx` — runs on an
    // injected timer and would pass with any value at all.
    //
    // Asserted on the rendered DOM rather than on a request count: a refetch
    // is the MECHANISM, and forbidding it would assert against the design.
    const stream = scriptedStream()
    let phases = ['build:running:implement']
    renderApp('/', [inbox(0), runs(() => [runWith(phases)]), stream.handler])
    await screen.findByTestId('phase-dots')
    phases = ['build:succeeded:implement']
    await stream.emit({ kind: 'phase.succeeded', run_id: 'run-1' })
    await waitFor(
      () =>
        expect(
          within(screen.getByTestId('phase-dots')).getAllByRole('listitem')[0],
        ).toHaveAttribute('data-state', 'succeeded'),
      { timeout: 10_000 },
    )
  }, 15_000)

  it('carries the decision rules of every host', async () => {
    // The SAME assertions Task 11 exports, run against this host. §7 asks for
    // them on all three surfaces because §5's all-or-nothing rule is what a
    // card is likeliest to get wrong, and a host-local reimplementation is how
    // one host quietly gets a weaker version.
    //
    // LAST in this file's `it` bodies by necessity, not by taste: the kit's
    // trailing `cleanup()` unmounts every container in RTL's registry, this
    // route's included.
    await expectDecisionRules((evidence) => renderApp('/', [inbox(1, evidence), runs(mixed())]))
  })
})
