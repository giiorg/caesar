import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { GateEvidence } from '../../gates/decide-gate'
import { expectDecisionRules } from '../../gates/decision-rules.testkit'
import { onTheWire } from '../../runs/on-the-wire.testkit'
import type { Run } from '../../runs/run-query'
import { renderApp } from '../../test/render-app'

type Phase = NonNullable<Run['phases']>[number]
type Publication = NonNullable<Run['publications']>[number]
type Artifact = NonNullable<Run['artifacts']>[number]
/** A run patch: what a fixture below changes about `baseRun()`. */
type RunShape = Partial<Run>

const AT = '2026-08-20T00:00:00Z'

/**
 * `workflow_name` IS A SUBSTRING OF `repo_slug` HERE, and the header test below
 * is written knowing it. `caesar/e2e` contains `e2e`, so
 * `toHaveTextContent('e2e')` is satisfied by a header rendering only the slug —
 * the assertion that separates them is an exact-string `getByText('e2e')`,
 * which testing-library compares against a node's whole normalized text.
 */
const baseRun = (): Run => ({
  id: 'run-1',
  task_id: 't1',
  task_title: 'Add revocation',
  repo_slug: 'caesar/e2e',
  workflow_name: 'e2e',
  status: 'running',
  definition_hash: 'h1',
  base_sha: 'abc123',
  policy_snapshot: {},
  generation: 1,
  execution_used_ms: 62_000,
  wall_deadline: '2099-01-01T00:00:00Z',
  created_at: AT,
  started_at: AT,
  finished_at: null,
  artifacts: null,
  publications: null,
  phases: null,
})

const evidence: GateEvidence = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: null,
  can_decide: true,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

const phase = (over: Partial<Phase> & Pick<Phase, 'node_id'>): Phase => ({
  attempt: 1,
  max_attempts: 3,
  status: 'succeeded',
  generation: 1,
  created_at: AT,
  finished_at: AT,
  ...over,
})

/**
 * The spine every test that is not about publication reads: two done phases and
 * a gate still waiting, in the order `ListPhasesForRun` materializes them
 * (`store/queries.sql:226` — `ORDER BY created_at, node_id, attempt`).
 */
const threePhases = (over: { review?: string; evidence?: GateEvidence } = {}): RunShape => ({
  phases: [
    phase({ node_id: 'impl' }),
    // REVIEW IS STILL RUNNING UNLESS A CASE SAYS OTHERWISE, and that default is
    // what makes the live-update test mean anything. With `phase()`'s own
    // `succeeded` default left in place here, `threePhases()` and
    // `threePhases({ review: 'succeeded' })` render the IDENTICAL timeline —
    // MEASURED: the run query key mutant (`['run', runId]` →
    // `['run-detail', runId]`) SURVIVED, because the state the test waited for
    // was already on screen before the frame was ever written.
    phase({
      node_id: 'review',
      status: over.review ?? 'running',
      ...(over.review === undefined ? { finished_at: null } : {}),
    }),
    phase({
      node_id: 'gate',
      status: 'running',
      finished_at: null,
      evidence: over.evidence ?? evidence,
    }),
  ],
  artifacts: [artifactRow({ node_id: 'impl', type: 'patch', id: 'a1' })],
})

const artifactRow = (
  over: Partial<Artifact> & Pick<Artifact, 'node_id' | 'type' | 'id'>,
): Artifact => ({
  attempt: 1,
  generation: 1,
  parent_ids: [],
  created_at: AT,
  ...over,
})

/** The same gate, already decided: no evidence, a decision, and nothing to do. */
const decidedGate = (): RunShape => ({
  phases: [
    phase({ node_id: 'impl' }),
    phase({
      node_id: 'gate',
      decision: {
        decision: 'approve',
        note: 'ship it',
        acted_by_id: 'u2',
        acted_by_email: 'alice@example.com',
        acted_at: '2026-08-20T00:01:00Z',
        self_approval: false,
        examined_artifact_id: 'a1',
      },
    }),
  ],
})

/**
 * A run whose publish node has a publication. The phase and the publication
 * share `node_id`, which is the ONLY thing tying them together on the wire:
 * `ApiPhaseResponse` has no node_type, so "this phase is a publish node" is a
 * statement about `run.publications` and not about the phase.
 */
const published = (over: Partial<Publication> = {}): RunShape => ({
  phases: [phase({ node_id: 'impl' }), phase({ node_id: 'publish' })],
  publications: [
    {
      node_id: 'publish',
      status: 'published',
      branch: 'caesar/run-1',
      target_branch: 'main',
      base_sha: 'abc123',
      ...over,
    },
  ],
})

/** `GET /runs/run-1`, from a fixed shape or from one re-read on every request. */
const run = (shape: RunShape | (() => RunShape)) =>
  http.get('/api/v1/runs/run-1', () =>
    HttpResponse.json(
      onTheWire({ ...baseRun(), ...(typeof shape === 'function' ? shape() : shape) }),
    ),
  )

const PATCH_B64 = btoa('--- a/one.txt\n+++ b/one.txt\n@@ -1 +1 @@\n-old\n+new\n')
const patchPayload = () => ({ diff: PATCH_B64 })

const artifact = (payload: unknown) =>
  http.get('/api/v1/runs/:id/artifacts/:artifactId', ({ params }) =>
    HttpResponse.json({
      id: params.artifactId,
      run_id: 'run-1',
      node_id: 'gate',
      type: 'patch',
      attempt: 1,
      generation: 1,
      parent_ids: [],
      payload,
    }),
  )

const threeEvents = () => [
  { id: 1, kind: 'run.created', payload: null, occurred_at: AT },
  { id: 2, kind: 'phase.started', payload: { node_id: 'impl' }, occurred_at: AT },
  { id: 3, kind: 'phase.succeeded', payload: { node_id: 'impl' }, occurred_at: AT },
]

const events = (rows: unknown[] | (() => unknown[])) =>
  http.get('/api/v1/runs/:id/events', () =>
    HttpResponse.json({ events: typeof rows === 'function' ? rows() : rows }),
  )

/**
 * The attention stream, under the test's control rather than the clock's.
 *
 * `render-app.tsx` already offers `silentStream` (opens, says nothing) and
 * `pendingStream` (never answers); this is the third shape and the only one a
 * live-update test can use — a handler that hands its controller back so a
 * frame can be written at a chosen moment, AFTER the screen under test has
 * rendered. It stays in this file because it has exactly one consumer.
 *
 * `emit` awaits the open, so a frame written before the transport's fetch has
 * been dispatched is queued rather than dropped on the floor. Without that the
 * test races the layout's effect and fails intermittently for a reason that has
 * nothing to do with its subject.
 */
function scriptedStream() {
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
        occurred_at: AT,
      })
      c.enqueue(enc.encode(`id: ${seq}\nevent: ${f.kind}\ndata: ${data}\n\n`))
    },
  }
}

describe('run detail', () => {
  it('reads top to bottom as the phase graph', async () => {
    renderApp('/runs/run-1', [run(threePhases())])
    const spine = within(await screen.findByTestId('phase-timeline')).getAllByRole('listitem')
    expect(spine.map((p) => p.getAttribute('data-node-id'))).toEqual(['impl', 'review', 'gate'])
  })

  it('expands a finished phase in place, showing its artifacts', async () => {
    renderApp('/runs/run-1', [run(threePhases())])
    await userEvent.click(await screen.findByRole('button', { name: /impl/ }))
    expect(await screen.findByText(/patch/)).toBeInTheDocument()
    // IN PLACE. A phase that navigated somewhere would break the locked spine:
    // the timeline is the screen, not an index into other screens.
    expect(screen.getByTestId('phase-timeline')).toBeInTheDocument()
  })

  it('expands a WAITING GATE into the approval surface — the same component', async () => {
    renderApp('/runs/run-1', [run(threePhases()), artifact(patchPayload())])
    await userEvent.click(await screen.findByRole('button', { name: /gate/ }))
    // The surface's own testids, asserted from THIS host: if run detail grew a
    // second, simpler gate renderer, these would be absent and the two
    // surfaces would have drifted exactly as §3.4 warns.
    expect(await screen.findByTestId('hard-facts')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: /^approve$/i })).toBeInTheDocument()
  })

  it('shows a DECIDED gate as what happened, not as a decidable one', async () => {
    renderApp('/runs/run-1', [run(decidedGate())])
    await userEvent.click(await screen.findByRole('button', { name: /gate/ }))
    expect(await screen.findByText(/alice@example.com/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
  })

  it('expands a publish phase into the PR', async () => {
    renderApp('/runs/run-1', [run(published({ pr_number: 42, pr_url: 'https://forge/pr/42' }))])
    await userEvent.click(await screen.findByRole('button', { name: /publish/ }))
    const link = await screen.findByRole('link', { name: /#42/ })
    expect(link).toHaveAttribute('href', 'https://forge/pr/42')
  })

  it('gives a HALTED publication a stated appearance', async () => {
    // The case the operator most needs to see and the easiest to render as a
    // blank space: `halted` is one of four statuses and the only one with no
    // pr_url to hang the UI on.
    renderApp('/runs/run-1', [
      run(published({ status: 'halted', halt_reason: 'base moved under the approved patch' })),
    ])
    await userEvent.click(await screen.findByRole('button', { name: /publish/ }))
    expect(await screen.findByText(/halted/i)).toBeInTheDocument()
    expect(await screen.findByText(/base moved under the approved patch/)).toBeInTheDocument()
  })

  it('offers no pull request a PENDING publication has not opened', async () => {
    // A stub never fakes success, and the four statuses are four different
    // sentences. `pending` carries no pr_url and no head_sha — nothing to link
    // — and a panel that rendered the frame and stopped would read as a
    // publication whose PR the screen simply failed to show.
    renderApp('/runs/run-1?phase=publish', [run(published({ status: 'pending' }))])
    const body = await screen.findByTestId('phase-publish')
    expect(within(body).getByText(/nothing has been pushed yet/i)).toBeInTheDocument()
    expect(within(body).queryByRole('link')).toBeNull()
  })

  it('lists only the artifacts of the attempt on screen', async () => {
    // A revision rematerializes a node at a new generation and the rows its
    // superseded attempt wrote STAY on the run (`ListArtifactsForRun` filters
    // by run, not by attempt). Listing them beside the current ones would show
    // a reader a plan the run has already thrown away, with nothing on screen
    // saying which is which.
    renderApp('/runs/run-1?phase=impl', [
      run({
        generation: 2,
        phases: [
          phase({ node_id: 'impl', generation: 1 }),
          phase({ node_id: 'impl', generation: 2 }),
        ],
        artifacts: [
          artifactRow({ node_id: 'impl', type: 'plan', id: 'superseded-1', generation: 1 }),
          artifactRow({ node_id: 'impl', type: 'patch', id: 'current-2', generation: 2 }),
        ],
      }),
    ])
    expect(await screen.findByText('current-2')).toBeInTheDocument()
    expect(screen.queryByText('superseded-1')).toBeNull()
  })

  it('says so when the wire carries a publication status this screen has never heard of', async () => {
    // The runtime half of the exhaustive switch, and the reason the switch runs
    // over a LOCAL union rather than over the wire type. `status` is a bare
    // string on the wire (`dto.go:355` documents four values in a comment, not
    // in an enum), so a fifth added server-side arrives here as an unknown
    // token — which the compiler cannot catch across that boundary. A screen
    // whose only exhaustiveness was the switch would render nothing for it.
    renderApp('/runs/run-1', [run(published({ status: 'rebasing' }))])
    await userEvent.click(await screen.findByRole('button', { name: /publish/ }))
    expect(await screen.findByText(/rebasing/)).toBeInTheDocument()
    expect(await screen.findByText(/caesar\/run-1/)).toBeInTheDocument()
  })

  it('renders events as a flat chronological log', async () => {
    // ApiRunEventResponse has no node_id — the node is inside the untyped
    // payload. Grouping events under their phase would depend on a field
    // contracts:check cannot police, and would silently mis-place every event
    // whose payload shape changed.
    renderApp('/runs/run-1', [run(threePhases()), events(threeEvents())])
    const log = within(await screen.findByTestId('event-log')).getAllByRole('listitem')
    expect(log).toHaveLength(3)
    expect(log[0]).toHaveTextContent('run.created')
  })

  it('has a sticky header with workflow, repo and attempt — and no cancel or retry', async () => {
    // §10.3: cancel does not exist and `runs/tasks.go:394` assigns it to Plan
    // 09 (`run is in flight; cancel arrives in Plan 09`); whole-task retry DOES
    // exist (rerunTask) and is deferred anyway, so the checkpoint can decide
    // what this header should offer as a whole.
    renderApp('/runs/run-1', [run(threePhases())])
    const header = await screen.findByTestId('run-header')
    expect(header).toHaveTextContent('e2e')
    expect(header).toHaveTextContent('caesar/e2e')
    expect(header).toHaveTextContent(/attempt 1/i)
    // The two above do NOT distinguish the workflow from the repo: `caesar/e2e`
    // contains `e2e`, so a header rendering only the slug satisfies both. A
    // string TextMatch is compared against a node's whole normalized text, so
    // this one resolves only to an element whose text is exactly the workflow
    // name.
    expect(within(header).getByText('e2e')).toBeInTheDocument()
    expect(within(header).queryByRole('button', { name: /cancel|retry/i })).toBeNull()
  })

  it('names the GENERATION as well as the attempt', async () => {
    // ATTEMPT CANNOT STAND IN FOR THIS. It is a per-node execution ordinal
    // that a rematerialized node CONTINUES rather than restarts
    // (`store/queries.sql:719-723`), so `attempt N` in the header is a fact
    // about one node's retries and says nothing about how many revision rounds
    // the run has been through. `generation` is that round (`dto.go:66-68`),
    // which is why it is read off the run and not off a phase.
    renderApp('/runs/run-1', [run({ ...threePhases(), generation: 2 })])
    expect(await screen.findByTestId('run-header')).toHaveTextContent(/generation 2/i)
  })

  it('collapses a superseded attempt into the one row that is current', async () => {
    // request_changes bumps the generation and rematerializes the nodes below
    // the gate, so ONE run really carries several phase rows per node id. One
    // <li> per ROW would put two `gate`s in the spine, give two rows the same
    // `data-node-id`, and make `?phase=gate` name both of them — while showing
    // a superseded attempt's status as though it were current.
    renderApp('/runs/run-1', [
      run({
        generation: 2,
        phases: [
          phase({ node_id: 'gate', status: 'failed', generation: 1 }),
          phase({ node_id: 'impl', generation: 2 }),
          phase({ node_id: 'gate', status: 'running', generation: 2, finished_at: null, evidence }),
        ],
      }),
    ])
    const spine = within(await screen.findByTestId('phase-timeline')).getAllByRole('listitem')
    expect(spine.map((p) => p.getAttribute('data-node-id'))).toEqual(['gate', 'impl'])
    expect(screen.getByTestId('phase-gate')).toHaveAttribute('data-state', 'running')
  })

  it('opens at the phase the URL names', async () => {
    renderApp('/runs/run-1?phase=gate', [run(threePhases()), artifact(patchPayload())])
    expect(await screen.findByTestId('hard-facts')).toBeVisible()
  })

  it('writes the open phase into the URL, so it can be sent to somebody else', async () => {
    // URL-AS-STATE. The assertion is on `router.state.location.search` and not
    // only on the rendered surface, because a `useState` disclosure renders the
    // identical DOM while leaving the address bar on the bare run.
    const router = renderApp('/runs/run-1', [run(threePhases()), artifact(patchPayload())])
    await userEvent.click(await screen.findByRole('button', { name: /gate/ }))
    await waitFor(() => expect(router.state.location.search).toEqual({ phase: 'gate' }))
  })

  it('ignores a phase param naming a node this run does not have', async () => {
    // A stale link must land on the timeline, not on a blank screen or a
    // crash: node ids come from a workflow version that may since have been
    // superseded by one that renamed the node.
    renderApp('/runs/run-1?phase=nonexistent', [run(threePhases())])
    expect(await screen.findByTestId('phase-timeline')).toBeVisible()
  })

  it('survives a phase param that is not even a string', async () => {
    // TanStack Router's default search parser runs JSON.parse over each value
    // and keeps the raw string only when that throws — so `?phase=gate` reaches
    // `validateSearch` as a string while THIS one reaches it as the number 42,
    // which `z.string()` rejects. `parse` would throw there and take the whole
    // screen down; `safeParse` reads it as no phase named and lands here.
    renderApp('/runs/run-1?phase=42', [run(threePhases())])
    expect(await screen.findByTestId('phase-timeline')).toBeVisible()
  })

  it('advances live when a phase transition arrives, with no user action', async () => {
    // THE END-TO-END PROOF OF THE `['run', runId]` SPELLING. The bridge names
    // that key from a frame (`realtime/cache-bridge.ts:92`) and
    // `runs/run-query.ts:32` spells it again; the two are deliberately not a
    // shared constant, so nothing but this round trip holds them together and a
    // typo in either is invisible to the compiler and to every other test.
    const stream = scriptedStream()
    let shape = threePhases()
    renderApp('/runs/run-1', [run(() => shape), stream.handler])
    await screen.findByTestId('phase-timeline')
    shape = threePhases({ review: 'succeeded' })
    await stream.emit({ kind: 'phase.succeeded', run_id: 'run-1' })
    await waitFor(
      () => expect(screen.getByTestId('phase-review')).toHaveAttribute('data-state', 'succeeded'),
      { timeout: 5_000 },
    )
  })

  it('carries the event log along on the same frame, with no second rule', async () => {
    // The log's key is `['run', runId, 'events']` and the bridge invalidates
    // `['run', runId]`, which react-query PREFIX-matches unless `exact` is set.
    // Nothing in the tree states that relationship — it is a property of two
    // literals in two files — so this is the only thing that would notice the
    // key being respelled `['run-events', runId]`.
    const stream = scriptedStream()
    let rows = threeEvents()
    renderApp('/runs/run-1', [run(threePhases()), events(() => rows), stream.handler])
    const log = await screen.findByTestId('event-log')
    expect(within(log).getAllByRole('listitem')).toHaveLength(3)
    rows = [...threeEvents(), { id: 4, kind: 'gate.waiting', payload: null, occurred_at: AT }]
    await stream.emit({ kind: 'gate.waiting', run_id: 'run-1' })
    await waitFor(() => expect(within(log).getAllByRole('listitem')).toHaveLength(4), {
      timeout: 5_000,
    })
  })

  it('shows no live agent output, and does not pretend there is any', async () => {
    // §10.4. No log-serving route exists; logs live on the runner's disk behind
    // a log_ref artifact and the event vocabulary is lifecycle-only. An empty
    // terminal box would be the most convincing lie on this screen.
    renderApp('/runs/run-1', [run(threePhases())])
    await userEvent.click(await screen.findByRole('button', { name: /review/ }))
    expect(screen.queryByTestId('agent-output')).toBeNull()
  })

  /**
   * §7's desktop sentence — "timeline becomes a left rail with the open phase's
   * detail on the right" — and the three tests below are what is falsifiable
   * about it HERE. jsdom loads no Tailwind (`app-shell.test.tsx` and
   * `e2e/approvals.spec.ts:21-23` say the same thing about the shell's two
   * presentations), so both are fully present and none of these assertions is
   * about geometry. What they pin is the structure the CSS reflows: two
   * regions neither of which contains the other, every phase reachable from
   * the rail, and the detail rendered ONCE.
   */
  it('offers every phase from a rail that is a distinct region from the detail pane', async () => {
    renderApp('/runs/run-1', [run(threePhases())])
    const rail = await screen.findByTestId('phase-rail')
    const detail = screen.getByTestId('phase-detail')
    // NEITHER contains the other. A rail nested inside the pane is a rail that
    // scrolls away with the thing it indexes, and a pane inside the rail is
    // the phone layout wearing two testids.
    expect(rail).not.toContainElement(detail)
    expect(detail).not.toContainElement(rail)
    // On hrefs and not on labels: the rail's whole job is to move `?phase=`,
    // and a list of <span>s carrying the right words does none of it.
    expect(
      within(rail)
        .getAllByRole('link')
        .map((a) => a.getAttribute('href')),
    ).toEqual(['/runs/run-1?phase=impl', '/runs/run-1?phase=review', '/runs/run-1?phase=gate'])
  })

  it('renders the open phase’s detail in the pane, once', async () => {
    renderApp('/runs/run-1?phase=gate', [run(threePhases()), artifact(patchPayload())])
    const detail = await screen.findByTestId('phase-detail')
    expect(await within(detail).findByRole('button', { name: /^approve$/i })).toBeInTheDocument()
    // ONCE, ACROSS THE WHOLE SCREEN, and this is the assertion that decides the
    // mechanism. The obvious way to build two presentations is two subtrees
    // that both render the open phase — Task 8's nav does exactly that with its
    // badge — but this body hosts `<ApprovalSurface>`, and
    // `decision-rules.testkit.ts` asserts with UNSCOPED `screen` queries on
    // behalf of three hosts. A second mount would make the kit's
    // `findByText(/approve grant/i)` throw on multiple matches in the test
    // below, so the two presentations here share one body rather than each
    // owning one.
    expect(screen.getAllByRole('button', { name: /^approve$/i })).toHaveLength(1)
  })

  it('drives both presentations from the one ?phase= param', async () => {
    const router = renderApp('/runs/run-1?phase=impl', [
      run(threePhases()),
      artifact(patchPayload()),
    ])
    const rail = await screen.findByTestId('phase-rail')
    // `"page"` and not `"true"`, and the value is MEASURED: `<Link>` marks the
    // row it considers active and `activeOptions.includeSearch` defaults true,
    // so the marked row is the one whose `?phase=` matches the address bar. An
    // explicit `aria-current` on the link was overridden by the router — see
    // `PhaseRail`. This assertion is the only thing in the tree pinning that
    // default: without it, a router upgrade that stopped comparing search would
    // mark every row current and nothing would fail.
    expect(within(rail).getByRole('link', { name: /impl/ })).toHaveAttribute('aria-current', 'page')
    expect(within(rail).getByRole('link', { name: /gate/ })).not.toHaveAttribute('aria-current')

    // The rail SETS the param where the phone row toggles it: on a master
    // detail layout, clicking the row you are already reading must not empty
    // the pane you are reading it in.
    await userEvent.click(within(rail).getByRole('link', { name: /gate/ }))
    await waitFor(() => expect(router.state.location.search).toEqual({ phase: 'gate' }))
    const detail = screen.getByTestId('phase-detail')
    expect(await within(detail).findByRole('button', { name: /^approve$/i })).toBeInTheDocument()
    expect(within(rail).getByRole('link', { name: /gate/ })).toHaveAttribute('aria-current', 'page')
    expect(within(rail).getByRole('link', { name: /impl/ })).not.toHaveAttribute('aria-current')
  })

  it('says a run has materialized nothing once, not once per presentation', async () => {
    // `phases: null` is a body the server really sends — a run created but not
    // yet compiled — and BOTH surfaces have something to say about it: the
    // timeline reports the empty graph, and the pane's caption would otherwise
    // invite a person to pick from a rail with nothing in it. The rail renders
    // nothing at all here, and the caption is suppressed with it.
    renderApp('/runs/run-1', [run({ phases: null })])
    expect(await screen.findByText(/nothing of its graph has been materialized/i)).toBeVisible()
    expect(screen.queryByTestId('phase-rail')).toBeNull()
    expect(screen.queryByText(/pick a phase/i)).toBeNull()
  })

  it('carries the decision rules of every host', async () => {
    // The SAME assertions Task 11 exports, run against this host. §7 asks for
    // them on all three surfaces because §5's all-or-nothing rule is what a
    // card is likeliest to get wrong, and a host-local reimplementation is how
    // one host quietly gets a weaker version.
    //
    // `?phase=gate` rather than a click: the kit renders and asserts, and it
    // knows nothing about a host that hides its controls behind a disclosure.
    // The URL is what opens this one, which is the point of the param.
    //
    // LAST in this file's `it` bodies by necessity, not by taste: the kit's
    // trailing `cleanup()` unmounts every container in RTL's registry, this
    // route's included.
    await expectDecisionRules((ev) =>
      renderApp('/runs/run-1?phase=gate', [
        run(threePhases({ evidence: ev })),
        artifact(patchPayload()),
      ]),
    )
  })
})
