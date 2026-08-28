import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { GateEvidence } from '../../gates/decide-gate'
import { expectDecisionRules } from '../../gates/decision-rules.testkit'
import { onTheWire } from '../../runs/on-the-wire.testkit'
import type { Run } from '../../runs/run-query'
import { renderApp, scriptedStream } from '../../test/render-app'

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

/** A gate decision as `GET /runs/{id}` carries it, with the verb varying. */
const gateDecision = (verb: string, note: string): NonNullable<Phase['decision']> => ({
  decision: verb,
  note,
  acted_by_id: 'u2',
  acted_by_email: 'alice@example.com',
  acted_at: '2026-08-20T00:01:00Z',
  self_approval: false,
  examined_artifact_id: 'a1',
})

/**
 * ONE GATE, TWO DECISIONS, ONE REVISION ROUND APART — the spec's own acceptance
 * shape for the attempt-precise link, and the ordinary life of a gate rather
 * than an edge case: request_changes opens a revision, which bumps the run's
 * generation and rematerializes the slice, and the gate comes back under the
 * SAME node id (`runs/revisions.go:22-40`). `?phase=gate` names both of its
 * rows and the timeline collapses them to the newest, so a link carrying only
 * the node would send BOTH `gate.decided` events to the approval — including
 * the one recording the request for changes.
 *
 * THE GATE'S GENERATION AND ATTEMPT DELIBERATELY DIFFER — (2,1) then (3,2) —
 * AND AN EARLIER GATE IS WHAT MAKES THEM. A fixture whose two ordinals move
 * together cannot tell the triple's second key from its third: MEASURED, a
 * transposition at either wiring site (the arguments to `phaseForSelector`, or
 * the two keys in the event row's `search`) survived the whole suite, and
 * TypeScript catches neither because both are `number`.
 *
 * They can only be separated by a round the gate did not take part in, and
 * `plan-gate` is that round. `attempt` is a PER-NODE ordinal that a
 * rematerialized node continues (`store/queries.sql:719-723`) while
 * `generation` is the RUN's revision round, bumped by `openRevision`'s call to
 * `BumpRunGeneration` (`runs/revisions.go:129`, its only caller anywhere) — so
 * any gate that has been
 * decided in every round of its run has attempt == generation forever. Here
 * `plan-gate` spends round 1 before `gate` has materialized at all, which is
 * what a workflow with a plan gate ahead of a review gate does every time.
 *
 * NOT A RETRY OF THE GATE, which was the other way to separate them and is not
 * a shape the control plane can produce: `InsertGatePhase` writes
 * `waiting_decision` and its own comment says the claim CTE predicates on
 * `pending` (`store/queries.sql:404-411`), so a gate is never leased and never
 * reaches the retry path that inserts `Attempt+1` at the same generation
 * (`runs/orchestrator.go:1609`). `materializeNode` is the only writer of a gate
 * row, so a gate's attempt advances once per revision round and never twice.
 */
const twoDecisions = (): RunShape => ({
  generation: 3,
  phases: [
    // ROUND 1, and this gate is only here to spend it. Its request_changes is
    // what carried the run to generation 2 while `impl` and `gate` below were
    // still unmaterialized — a node materializes when its upstream lands.
    phase({
      node_id: 'plan-gate',
      status: 'superseded',
      decision: gateDecision('request_changes', 'plan the migration first'),
    }),
    phase({
      node_id: 'plan-gate',
      generation: 2,
      attempt: 2,
      decision: gateDecision('approve', 'good plan'),
    }),
    // ROUND 2: `gate`'s FIRST attempt, at the run's SECOND generation.
    phase({ node_id: 'impl', generation: 2, status: 'superseded' }),
    phase({
      node_id: 'gate',
      generation: 2,
      status: 'superseded',
      decision: gateDecision('request_changes', 'narrow the diff'),
    }),
    // ROUND 3: the same gate, one round on, at attempt 2.
    phase({ node_id: 'impl', generation: 3, attempt: 2 }),
    phase({
      node_id: 'gate',
      generation: 3,
      attempt: 2,
      decision: gateDecision('approve', 'ship it'),
    }),
  ],
})

/** The two `gate.decided` events those two decisions wrote (`runs/gates.go:228-237`, `runs/revisions.go:212-221`). */
const twoDecisionEvents = () => [
  {
    id: 1,
    kind: 'gate.decided',
    payload: {
      node_id: 'gate',
      attempt: 1,
      generation: 2,
      decision: 'request_changes',
      examined_artifact_id: 'a1',
      acted_by: 'u2',
      self_approval: false,
    },
    occurred_at: AT,
  },
  {
    id: 2,
    kind: 'gate.decided',
    payload: {
      node_id: 'gate',
      attempt: 2,
      generation: 3,
      decision: 'approve',
      examined_artifact_id: 'a2',
      acted_by: 'u2',
      self_approval: false,
    },
    occurred_at: AT,
  },
]

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

/**
 * EVERY OTHER STATUS IN THE ENUM (`runs/status.go:14-20`), and the whole enum
 * rather than a sample because "failed only" is the entire rule: the button's
 * guard is one comparison, and a sampled list leaves whichever status it
 * omitted as the one a widened guard could quietly start answering. `dead` and
 * `superseded` are terminal too and are deliberately silent here — that is
 * sofo's ruling and not an oversight, and this row is where a later ruling
 * would have to be changed.
 */
const NOT_FAILED = ['pending', 'running', 'waiting', 'succeeded', 'superseded', 'dead'] as const

/**
 * `POST /api/v1/tasks/{id}/runs`, and the run detail the success navigates to.
 *
 * THE SPY RECORDS THE PATH, not merely that a call happened. The mutation names
 * its task through `run.task_id`, and a version that passed `run.id` instead
 * would still POST, still match a `:id`-shaped handler, still get its 201 and
 * still navigate — every assertion but this one survives it. The path is the
 * only place the two are distinguishable, which is why the fixture's `task_id`
 * (`t1`) and its run id (`run-1`) are different strings.
 */
const rerunOk = (seen: { path?: string }) => [
  http.post('/api/v1/tasks/:id/runs', ({ request }) => {
    seen.path = new URL(request.url).pathname
    return HttpResponse.json(onTheWire(rerunResult()), { status: 201 })
  }),
  http.get('/api/v1/runs/run-2', () => HttpResponse.json(onTheWire(rerunResult()))),
]

/** What `rerunTask` answers with: a fresh, still-pending run of the same task. */
const rerunResult = (): Run => ({
  ...baseRun(),
  id: 'run-2',
  status: 'pending',
  started_at: null,
  generation: 1,
})

const problem = (status: number, body: Record<string, unknown>) =>
  HttpResponse.json(body, { status, headers: { 'content-type': 'application/problem+json' } })

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

  it('sends an artifact ROW to the artifact view', async () => {
    // PER ROW, NOT PER PHASE, and the fixture is what makes that a claim rather
    // than a coincidence: this attempt produced two artifacts, so a link on the
    // phase card could only ever open one of them.
    //
    // THE ASSERTION IS THE RENDERED PATCH ON THE OTHER SIDE, never the href,
    // and that is not fastidiousness. Named
    // `runs.$runId.artifacts.$artifactId.tsx`, the destination route nests
    // under THIS one, whose component renders no `<Outlet />` — MEASURED: the
    // href was correct, the URL was correct, and run detail rendered again. An
    // href-only check passes against that; only reading the artifact's own
    // output catches it.
    renderApp('/runs/run-1?phase=impl', [
      run({
        phases: [phase({ node_id: 'impl' })],
        artifacts: [
          artifactRow({ node_id: 'impl', type: 'plan', id: 'a0' }),
          artifactRow({ node_id: 'impl', type: 'patch', id: 'a1' }),
        ],
      }),
      // ANSWERS PER ID, and that is what makes the click's DESTINATION
      // falsifiable rather than merely its route. With one payload served for
      // every id, a row that linked to the phase's FIRST artifact instead of
      // its own would land on `a0`, render the same patch, and pass — MEASURED
      // as a surviving mutant before this handler was split in two.
      http.get('/api/v1/runs/:id/artifacts/:artifactId', ({ params }) =>
        HttpResponse.json({
          id: params.artifactId,
          run_id: 'run-1',
          node_id: 'impl',
          type: params.artifactId === 'a1' ? 'patch' : 'plan',
          attempt: 1,
          generation: 1,
          parent_ids: [],
          created_at: AT,
          payload: params.artifactId === 'a1' ? patchPayload() : { summary: 'the superseded plan' },
        }),
      ),
    ])
    await userEvent.click(await screen.findByRole('link', { name: /a1/ }))
    expect(await screen.findByText('one.txt')).toBeInTheDocument()
    expect(screen.queryByText('the superseded plan')).toBeNull()
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
    // NEITHER ABSENCE HAS THE SAME REASON ANY MORE, and the title keeps them
    // together only because this fixture is `running`.
    //
    // Cancel does not exist at all: §10.3, and `runs/tasks.go:394` assigns it
    // to Plan 09 (`run is in flight; cancel arrives in Plan 09`). Retry DOES
    // exist and is right below — it is absent HERE because a running run is not
    // a failed one, which makes this assertion one case of the `NOT_FAILED` row
    // rather than a statement that the button was never built.
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

  /**
   * RETRY, AND ONLY ON A FAILED RUN — sofo's checkpoint ruling, verbatim.
   *
   * A rejected run ends `failed` too (`gates.go:241-247` fails the run in the
   * same transaction that finishes the rejected gate), so this one status rule
   * covers a rejection as well and there is no second branch for it.
   *
   * The button rides `rerunTask`, which is a TASK-level operation: it does not
   * resume this run, it mints a new one from the task's pinned version. That is
   * why the success navigates away rather than refreshing in place — the run on
   * screen is not the run that will make progress.
   */
  it('offers Retry on a failed run', async () => {
    renderApp('/runs/run-1', [run({ ...threePhases(), status: 'failed' })])
    const header = await screen.findByTestId('run-header')
    expect(within(header).getByRole('button', { name: /^retry$/i })).toBeInTheDocument()
  })

  it.each(NOT_FAILED)('offers no Retry on a %s run', async (status) => {
    renderApp('/runs/run-1', [run({ ...threePhases(), status })])
    const header = await screen.findByTestId('run-header')
    expect(within(header).queryByRole('button', { name: /retry/i })).toBeNull()
  })

  it('Retry posts to the run’s TASK and lands on the run it created', async () => {
    const seen: { path?: string } = {}
    const router = renderApp('/runs/run-1', [
      run({ ...threePhases(), status: 'failed' }),
      ...rerunOk(seen),
    ])
    await userEvent.click(await screen.findByRole('button', { name: /^retry$/i }))
    // The NAVIGATION is what is awaited, not the request: the POST resolving is
    // one `onSuccess` away from the address changing, and asserting on the spy
    // first would pass against a mutation that never navigated at all.
    await waitFor(() => expect(router.state.location.pathname).toBe('/runs/run-2'))
    expect(seen.path).toBe('/api/v1/tasks/t1/runs')
    expect(await screen.findByTestId('run-header')).toHaveTextContent('pending')
  })

  /**
   * The 409 the service really answers, and the reason the button needs an
   * error surface at all.
   *
   * `runs/tasks.go:401` returns ClassConflict when another re-run of this task
   * committed its pending run between our read and our insert — two operators
   * on the same failed run, which is exactly the shape a shared inbox produces.
   * It is a NORMAL answer: nothing is broken, somebody else's re-run won, and
   * the screen must say so and keep its correlation id rather than throw.
   */
  it('renders a losing re-run’s conflict in place, correlation id and all', async () => {
    renderApp('/runs/run-1', [
      run({ ...threePhases(), status: 'failed' }),
      http.post('/api/v1/tasks/:id/runs', () =>
        problem(409, {
          type: 'about:blank',
          title: 'Conflict',
          status: 409,
          detail: 'another re-run of this task is already pending',
          correlation_id: 'cid-11',
        }),
      ),
    ])
    await userEvent.click(await screen.findByRole('button', { name: /^retry$/i }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('another re-run of this task is already pending')
    expect(alert).toHaveTextContent('cid-11')
    // Still on the failed run, and the button is still there to try again.
    expect(screen.getByRole('button', { name: /^retry$/i })).toBeEnabled()
  })

  /**
   * THE ARM THAT IS NOT AN `ApiError` AT ALL, and the only thing that makes the
   * narrowing in front of `ApiErrorPanel` falsifiable.
   *
   * A request that never reached the server rejects with fetch's own TypeError,
   * which carries no status, no problem document and no correlation id — and
   * `ApiErrorPanel` deliberately RETHROWS anything that is not ours. Handed the
   * raw error it would throw during render and take the whole run screen down,
   * on the one failure a user is most likely to hit. Without this case the
   * narrowing is unobservable: every other test here fails with a real problem
   * document, which passes through it unchanged.
   */
  it('survives a Retry that never reaches the server', async () => {
    renderApp('/runs/run-1', [
      run({ ...threePhases(), status: 'failed' }),
      http.post('/api/v1/tasks/:id/runs', () => HttpResponse.error()),
    ])
    await userEvent.click(await screen.findByRole('button', { name: /^retry$/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/check your connection/i)
    // The header is still rendered — the point of the case is that nothing threw.
    expect(screen.getByTestId('run-header')).toHaveTextContent('caesar/e2e')
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

  // THE COUNTER IS GONE FROM THE SUMMARY, and these two cases are why. Both
  // rows below rendered "attempt 1/3" and "attempt 4/3" before this — a promise
  // of a further attempt under a gate a person had just rejected, and a
  // nonsense fraction under one that is merely parked. What the line may say is
  // `phaseStatusLine`'s business and is pinned exhaustively in
  // `runs/phase-status.test.ts`; what these assert is that the ROUTE reaches
  // it, on both of the surfaces that render a summary.
  it('a rejected gate names the rejection rather than a counter, on both surfaces', async () => {
    // A rejected gate is a `failed` row with a PERMANENT outcome and no
    // successor — `gates.go:241-247` finishes it and fails the run in one
    // transaction. Status alone cannot save this row; the outcome class does.
    renderApp('/runs/run-1', [
      run({
        status: 'failed',
        phases: [
          phase({ node_id: 'impl' }),
          phase({
            node_id: 'gate',
            status: 'failed',
            outcome: {
              class: 'permanent',
              code: 'gate_rejected',
              message: 'gate "gate" was rejected',
            },
          }),
        ],
      }),
    ])
    const row = within(await screen.findByTestId('phase-gate'))
    expect(row.getByText('gate_rejected')).toBeInTheDocument()
    expect(row.queryByText(/attempt \d/)).toBeNull()
    // The rail restates the same summary, so it must say the same thing. This
    // is the assertion that fails if a future edit gives one host a line the
    // other does not have.
    expect(within(screen.getByTestId('phase-rail')).getByText('gate_rejected')).toBeInTheDocument()
  })

  it('says resumable — with the reason from the attempt before — rather than 4/3', async () => {
    // `attempt` outruns `max_attempts` only through UNCHARGED waits
    // (`orchestrator.go:1532` spends the budget on chargeable failures while
    // the ordinal counts every attempt), so 4/3 is not a bug and must not read
    // as one. The waiting row has no outcome yet — the code that parked it is
    // on attempt 3, which is what `priorAttemptOf` goes and gets.
    renderApp('/runs/run-1', [
      run({
        phases: [
          // `review` is also on attempt 3 and is here so that the lookup's NODE
          // scoping is load-bearing: matching on the ordinal alone would reach
          // this row first and report the wrong code below.
          phase({
            node_id: 'review',
            attempt: 3,
            status: 'failed',
            outcome: { class: 'transient', code: 'harness_error', message: 'exit 1' },
          }),
          phase({
            node_id: 'impl',
            attempt: 3,
            status: 'failed',
            outcome: { class: 'transient', code: 'rate_limited', message: 'cap window' },
          }),
          phase({ node_id: 'impl', attempt: 4, status: 'pending', finished_at: null }),
        ],
      }),
    ])
    const row = within(await screen.findByTestId('phase-impl'))
    expect(row.getByText('resumable — not chargeable (rate_limited)')).toBeInTheDocument()
    expect(row.queryByText(/4\/3/)).toBeNull()
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
    // that key from a frame (`realtime/cache-bridge.ts`) and `runs/run-query.ts`
    // spells it again; the two are deliberately not a shared constant, so
    // nothing but this round trip holds them together and a typo in either is
    // invisible to the compiler and to every other test.
    //
    // It is also the one live-update test a `phase.*` frame still drives at full
    // speed after Task 12: the run DTO is the per-window tier, so this lands in
    // 250ms while the collections wait out the throttle.
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

  it('carries the event log along on the same frame, by NAMING it', async () => {
    // The log's key is `['run', runId, 'events']` and this is the only thing in
    // the tree that would notice it being respelled `['run-events', runId]` —
    // the spelling lives in two files as two literals, deliberately not as a
    // shared constant.
    //
    // WHAT CARRIES IT CHANGED IN TASK 12, and the old sentence here is why this
    // comment is being rewritten rather than left alone: the log used to arrive
    // for free, because the bridge invalidated `['run', runId]` and react-query
    // prefix-matches unless `exact` is set. That prefix ride is exactly what
    // made a chatty run refetch its whole log ten times a window, so the run DTO
    // now goes out exact and the log states its own tier membership. This test
    // stayed green across that change with its stated reason turned false —
    // `gate.waiting` is a lifecycle kind, so the log is invalidated by name on
    // the immediate tier.
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

  it('sends each gate.decided row to ITS attempt, not to the newest of that node', async () => {
    // THE ACCEPTANCE SHAPE FOR §2.4's SECOND LINK CLASS. Both rows name the
    // same node, so a `?phase=gate` link resolves both to the newest attempt —
    // and the row recording a request for changes would open somebody else's
    // approval. The two clicks below are the same assertion in both
    // directions: each pane shows its own verb AND not the other's, so neither
    // half can pass because the text was never on screen.
    const router = renderApp('/runs/run-1', [run(twoDecisions()), events(twoDecisionEvents())])
    const log = await screen.findByTestId('event-log')
    const detail = screen.getByTestId('phase-detail')

    await userEvent.click(within(log).getByRole('link', { name: /gate attempt 1/ }))
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ phase: 'gate', generation: 2, attempt: 1 }),
    )
    expect(await within(detail).findByText(/requested changes on this gate/)).toBeVisible()
    expect(within(detail).queryByText(/approved this gate/)).toBeNull()
    // The summary and `data-state` follow the body. A row that rendered
    // attempt 1's decision under the newest attempt's status would be the same
    // lie in a quieter attribute, and `data-state` is what the live-update
    // tests read.
    expect(screen.getByTestId('phase-gate')).toHaveAttribute('data-state', 'superseded')

    // AND EVERY OTHER ROW STILL SHOWS ITSELF. The selected attempt belongs to
    // ONE row; a resolution that leaked into the closed rows would put the
    // gate's node id and the gate's status on the `impl` row, which is a
    // visible rendering bug that the two assertions above cannot see — MEASURED,
    // the `open &&` conjunct in `PhaseTimeline`'s `shown` was unfalsifiable
    // until this pair existed. The node label is asserted beside `data-state`
    // because a leak changes both and the label is what a reader would notice.
    const implRow = within(screen.getByTestId('phase-impl'))
    expect(screen.getByTestId('phase-impl')).toHaveAttribute('data-state', 'succeeded')
    expect(implRow.getAllByText('impl').length).toBeGreaterThan(0)
    expect(implRow.queryByText('gate')).toBeNull()

    await userEvent.click(within(log).getByRole('link', { name: /gate attempt 2/ }))
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ phase: 'gate', generation: 3, attempt: 2 }),
    )
    expect(await within(detail).findByText(/approved this gate/)).toBeVisible()
    expect(within(detail).queryByText(/requested changes on this gate/)).toBeNull()
    expect(screen.getByTestId('phase-gate')).toHaveAttribute('data-state', 'succeeded')
  })

  it('says so when the URL names an attempt this run does not carry', async () => {
    // HONEST ABSENCE RATHER THAN A SILENT FALL-BACK TO NEWEST, which would
    // re-create the exact bug the selector exists to fix — quietly, and only
    // for the links that had gone stale.
    renderApp('/runs/run-1?phase=gate&generation=9&attempt=9', [run(twoDecisions())])
    expect(await screen.findByText(/not in the run's phase list/i)).toBeVisible()
    expect(screen.queryByText(/approved this gate/)).toBeNull()
  })

  /**
   * A SELECTOR `searchSchema` REJECTS MUST LAND ON THE NODE'S NEWEST — every
   * such value, by the same route, and never on the "not in this run's phase
   * list" sentence, which is a claim about the RUN rather than about the URL.
   *
   * The first case is the one that decides the schema's spelling, and it is
   * MEASURED rather than reasoned. TanStack Router's parser JSON.parses each
   * value, so `?generation=true` reaches `validateSearch` as the BOOLEAN true.
   * `z.coerce.number()` reads it as 1 and silently targets attempt 1 — a real
   * row, a real decision, and not the one anybody asked for; plain
   * `z.number()` rejects the object, the raw boolean survives the ancestor
   * merge, and the read-site narrow lands it where `?phase=42` already lands.
   * Both spellings run against this fixture, and it tells them apart because
   * attempt 1 and attempt 2 of this gate were decided differently.
   *
   * The other two are what makes the read-site narrow re-assert the SCHEMA's
   * claim — integer and positive — rather than merely `typeof === 'number'`.
   * Both reach `useSearch()` as ordinary numbers that the schema refused, and
   * a looser narrow would carry them into `phaseForSelector` and answer the
   * absence sentence. MEASURED: with the narrow weakened to `typeof value ===
   * 'number'` the whole suite stayed green until these two cases existed.
   */
  it.each([
    ['a boolean the parser never turned into a number', 'true'],
    ['a fraction no attempt ordinal can be', '1.5'],
    ['a zero, which no generation ever is', '0'],
  ])('reads %s as no selector at all', async (_label, value) => {
    renderApp(`/runs/run-1?phase=gate&generation=${value}&attempt=${value}`, [run(twoDecisions())])
    expect(await screen.findByText(/approved this gate/)).toBeVisible()
    expect(screen.queryByText(/not in the run's phase list/i)).toBeNull()
  })

  it('drops an attempt selector when a rail row is clicked, because a rail row means newest', async () => {
    // AN OBJECT `search` REPLACES, IT DOES NOT MERGE — measured in
    // `@tanstack/router-core@1.171.24` (`dist/esm/router.js:898`,
    // `functionalUpdate(dest.search, currentSearch)` returns `dest.search`
    // verbatim). Nothing in the tree pinned that, and Task 9 is the first
    // change that breaks if it flips: a rail row that inherited a stale
    // `generation`/`attempt` would open a node at an attempt belonging to a
    // different node. The assertion is `toEqual` on the WHOLE search object
    // for that reason — a `toMatchObject` would pass with both keys still on it.
    const router = renderApp('/runs/run-1?phase=gate&generation=2&attempt=1', [run(twoDecisions())])
    const rail = await screen.findByTestId('phase-rail')
    await userEvent.click(within(rail).getByRole('link', { name: /impl/ }))
    await waitFor(() => expect(router.state.location.search).toEqual({ phase: 'impl' }))
  })

  it('links a publish.pr_opened row to the pull request, and only a real URL', async () => {
    // `pr_url` is the key (`runs/publish.go:1016`), and it is a string the
    // control plane copied out of a forge's JSON — so the scheme is checked
    // rather than assumed. All three rows below are the same code path with one
    // field changed, which is what stops the exact-list assertion passing
    // because no row was ever rendered.
    //
    // THE THIRD ROW IS THE ONE THAT PROVES THE GUARD. React 19.2.8 rewrites a
    // `javascript:` href to a thrower of its own accord — MEASURED — so a
    // fixture carrying only that scheme would stay green with the check
    // deleted, testing React rather than this file. A relative `pr_url` is the
    // case nothing else catches: it renders a link straight back to the page
    // the reader is already on.
    const payload = (url: unknown) => ({
      node_id: 'publish',
      branch: 'caesar/run-1',
      pr_number: 42,
      pr_url: url,
      state: 'open',
      adopted: false,
    })
    renderApp('/runs/run-1', [
      run(threePhases()),
      events([
        {
          id: 1,
          kind: 'publish.pr_opened',
          payload: payload('https://forge.test/pr/42'),
          occurred_at: AT,
        },
        {
          id: 2,
          kind: 'publish.pr_opened',
          payload: payload('javascript:alert(1)'),
          occurred_at: AT,
        },
        { id: 3, kind: 'publish.pr_opened', payload: payload('/runs/run-1'), occurred_at: AT },
      ]),
    ])
    const log = await screen.findByTestId('event-log')
    const links = within(log).getAllByRole('link')
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['https://forge.test/pr/42'])
    expect(links[0]).toHaveTextContent('#42')
  })

  it('links the publish.adopted that adopted a PR, and not the one that adopted a branch', async () => {
    // ONE KIND, TWO EMITTERS, AND ONLY ONE OF THEM NAMES A PULL REQUEST.
    // `openPublication` picks `publish.adopted` over `publish.pr_opened` when
    // the branch already had a PR and this run took it over, on the same
    // payload with the same `pr_url` (`runs/publish.go:993-1017`) — so a log
    // that linked only `pr_opened` would show a reader a PR they cannot reach
    // for a reason invisible from the row. But `pushPublication` writes the
    // same kind when the push found the branch already at the wanted head
    // (`runs/publish.go:807-810`, the crash-after-push recovery), over
    // `{node_id, branch, head_sha, tree_hash, created}` — no `pr_url` at all.
    // Both rows are below because the kind cannot tell them apart and the
    // payload can: the second one is the honest absence, a line with no link.
    //
    // The `adopted: true` flag in the payload is NOT what is read — the KIND is.
    // Asserting on the flag instead would pass against an implementation that
    // never looked at `publish.adopted` at all, because `pr_opened` carries the
    // same key set with the flag false.
    renderApp('/runs/run-1', [
      run(threePhases()),
      events([
        {
          id: 1,
          kind: 'publish.adopted',
          payload: {
            node_id: 'publish',
            branch: 'caesar/run-1',
            pr_number: 7,
            pr_url: 'https://forge.test/pr/7',
            state: 'open',
            adopted: true,
          },
          occurred_at: AT,
        },
        {
          id: 2,
          kind: 'publish.adopted',
          payload: {
            node_id: 'publish',
            branch: 'caesar/run-1',
            head_sha: 'c0ffee1',
            tree_hash: 'deadbee',
            created: false,
          },
          occurred_at: AT,
        },
      ]),
    ])
    const log = await screen.findByTestId('event-log')
    expect(within(log).getAllByRole('listitem')).toHaveLength(2)
    const link = within(log).getByRole('link')
    expect(link).toHaveAttribute('href', 'https://forge.test/pr/7')
    expect(link).toHaveTextContent('#7')
  })

  it('leaves every other event row exactly as it was — a line, not a link', async () => {
    // §2.4 names a gate decision and a pull request in this log — three kinds,
    // since a pull request arrives under two — and Task 8 owns the third link
    // class from the phase card. A lifecycle transition's whole content is the
    // sentence in the row, and a `gate.decided` whose payload lost a key is a
    // row with no link rather than a link to nowhere.
    renderApp('/runs/run-1', [
      run(threePhases()),
      events([
        ...threeEvents(),
        { id: 4, kind: 'gate.decided', payload: { node_id: 'gate' }, occurred_at: AT },
        // THE KIND IS A FILTER THE PAYLOAD SHAPE CANNOT REPLACE, and this row
        // is what says so: a perfectly usable `pr_url` under a kind §2.4 does
        // not name. `publish.halted` carries no `pr_url` today — nothing but
        // `openPublication`'s event does (`runs/publish.go:1016`) — so without
        // this row an implementation that linked ANY payload holding a URL
        // would be green, and would start linking the first kind that ever
        // gains one. MEASURED: dropping the kind test entirely survived until
        // this row existed. The converse — that the kind cannot replace the
        // payload check either, because `publish.adopted` is written by an
        // emitter that names no PR — is the branch-adoption row two tests up.
        {
          id: 5,
          kind: 'publish.halted',
          payload: { node_id: 'publish', pr_url: 'https://forge.test/pr/9', pr_number: 9 },
          occurred_at: AT,
        },
      ]),
    ])
    const log = await screen.findByTestId('event-log')
    expect(within(log).getAllByRole('listitem')).toHaveLength(5)
    expect(within(log).queryAllByRole('link')).toEqual([])
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
