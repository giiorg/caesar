import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { queryClient } from '../api/query-client'
import type { Run } from '../runs/run-query'
import { server } from '../test/msw'
import type { GateEvidence } from './decide-gate'
import { expectDecisionRules } from './decision-rules.testkit'
import { GateControls } from './gate-controls'

const decidable: GateEvidence = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: null,
  can_decide: true,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

/** The server's own 409 for a gate somebody else got to first (`gates.go:221`). */
const conflict = () =>
  HttpResponse.json(
    {
      title: 'Conflict',
      status: 409,
      detail: 'this gate attempt has already been decided',
      correlation_id: 'cid-409',
    },
    { status: 409, headers: { 'content-type': 'application/problem+json' } },
  )

const serverError = (correlationId: string) =>
  HttpResponse.json(
    { title: 'Internal error', status: 500, correlation_id: correlationId },
    { status: 500, headers: { 'content-type': 'application/problem+json' } },
  )

/**
 * `GET /runs/r1` as it reads once alice's approval has landed on the gate.
 *
 * Annotated `Run` rather than left to inference so that a field added to or
 * renamed on ApiRunResponse reddens `tsc --noEmit` here — where the fixture is
 * WRITTEN — instead of arriving at `latestDecision` as a missing property at
 * runtime, on a path only the 409 tests reach.
 */
const decidedByAlice = (): Run => ({
  id: 'r1',
  task_id: 't1',
  task_title: 'Add a thing',
  repo_slug: 'acme/app',
  workflow_name: 'default',
  status: 'running',
  definition_hash: 'h1',
  base_sha: 'abc',
  policy_snapshot: {},
  generation: 1,
  execution_used_ms: 0,
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
      status: 'succeeded',
      generation: 1,
      created_at: '2026-08-20T00:00:00Z',
      finished_at: '2026-08-20T00:01:00Z',
      decision: {
        decision: 'approve',
        note: 'looks right to me',
        acted_by_id: 'u2',
        acted_by_email: 'alice@example.com',
        acted_at: '2026-08-20T00:01:00Z',
        self_approval: false,
        examined_artifact_id: 'a1',
      },
    },
  ],
})

/**
 * The controls with a real query client and a real network.
 *
 * The POST body is captured by an MSW HANDLER rather than by stubbing the
 * generated client: the shape openapi-fetch puts on the wire — which key is
 * present, which is absent — is half of what the mutation's tests assert, and
 * a hand-rolled double would prove the double instead.
 *
 * GateControls is a component and not a route, so the router is left out; the
 * query client is not, because the 409 path really does refetch the run.
 */
function renderControls(opts: {
  evidence: GateEvidence
  onPost?: (body: unknown) => void
  /**
   * Spread conditionally below rather than passed as `onDecided={opts.onDecided}`:
   * under `exactOptionalPropertyTypes` an optional prop refuses an explicit
   * `undefined` (MEASURED: `TS2375 … Type 'undefined' is not assignable to type
   * '() => void'`), and the tests that omit it are testing a host that did not
   * wire it.
   */
  onDecided?: () => void
  /**
   * The POST's reply, as a plain `Response`.
   *
   * `HttpResponse` is generic in its body type — MEASURED: naming it bare here
   * is `TS2314: Generic type 'HttpResponse<BodyType>' requires 1 type
   * argument(s)` — and `HttpResponse.json` returns `HttpResponse<BodyType>`
   * narrowed to whatever literal it was handed
   * (`msw/lib/core/HttpResponse-DL-P1EeG.d.ts:353`). Either spelling would make
   * this parameter's type follow the fixtures instead of constraining them.
   */
  reply?: Response
  refetched?: Run
  /**
   * A `['run', runId]` entry planted BEFORE the render — a host that is already
   * showing this run when the decision is posted.
   */
  seedRun?: Run
}) {
  queryClient.clear()
  if (opts.seedRun !== undefined) queryClient.setQueryData(['run', 'r1'], opts.seedRun)
  server.use(
    http.post('/api/v1/runs/:id/gates/:node/decision', async ({ request }) => {
      opts.onPost?.(await request.json())
      return (
        opts.reply ??
        HttpResponse.json({
          authorization_id: '',
          decision: 'approve',
          decision_id: 'd1',
          generation: 1,
          run_status: 'running',
        })
      )
    }),
    http.get('/api/v1/runs/:id', () => HttpResponse.json(opts.refetched ?? decidedByAlice())),
  )
  render(
    <QueryClientProvider client={queryClient}>
      <GateControls
        runId="r1"
        nodeId="gate"
        evidence={opts.evidence}
        {...(opts.onDecided === undefined ? {} : { onDecided: opts.onDecided })}
      />
    </QueryClientProvider>,
  )
}

describe('gate controls', () => {
  it('offers all three when the caller may decide', async () => {
    renderControls({ evidence: decidable })
    for (const name of [/^approve$/i, /request changes/i, /^reject$/i]) {
      expect(await screen.findByRole('button', { name })).toBeEnabled()
    }
  })

  it('enforces §5 — all three or none, with the reason named', async () => {
    // The rule lives in decision-rules.testkit.ts because Tasks 12, 13 and 16
    // re-run it verbatim from their own surfaces. Consuming it here rather than
    // restating it is what proves the kit works before three hosts depend on
    // it — including its `cleanup()` between cases. MEASURED with those
    // `cleanup()` calls deleted: this test fails with `Found multiple elements
    // with the text: /approve grant/i`, the third case tripping over the first
    // case's DOM. The count is left unstated on purpose — the kit gained a
    // fourth case with `deadline_expired` and this sentence went stale once
    // already.
    await expectDecisionRules((evidence) => {
      renderControls({ evidence })
    })
  })

  it('names the last refusal too, and it is nobody-can-decide', async () => {
    // evidence_missing is not a permission problem: the gate examines an
    // artifact the run never produced, decideGate answers 500 rather than a
    // refusal, and no grant anywhere fixes it. A message about grants, about
    // self-approval or about a deadline would send the reader after the wrong
    // thing — and `decidable`'s wall_deadline is in 2099, so the deadline
    // sentence has no business on this screen at all.
    renderControls({
      evidence: { ...decidable, can_decide: false, cannot_decide_reason: 'evidence_missing' },
    })
    expect(await screen.findByText(/never produced/i)).toBeInTheDocument()
    expect(screen.queryByText(/approve grant/i)).toBeNull()
    expect(screen.queryByText(/your own gate/i)).toBeNull()
    expect(screen.queryByText(/wall deadline/i)).toBeNull()
  })

  it('degrades to something true on a reason it has never heard of', async () => {
    // cannot_decide_reason is a bare string on the wire, not an OpenAPI enum
    // (schema.d.ts: `cannot_decide_reason: string`). A FIFTH reason added
    // server-side must reach the user as an honest generic refusal, not as an
    // empty block — and never as one of the four specific messages, each of
    // which tells the reader to go and do something that would not help.
    renderControls({
      evidence: { ...decidable, can_decide: false, cannot_decide_reason: 'quota_exhausted' },
    })
    expect(await screen.findByText(/cannot decide this gate/i)).toBeInTheDocument()
    expect(screen.queryByText(/approve grant/i)).toBeNull()
    expect(screen.queryByText(/your own gate/i)).toBeNull()
    expect(screen.queryByText(/never produced/i)).toBeNull()
    // And not the deadline sentence either. `decidable`'s wall_deadline is in
    // 2099, so this line is what catches REASONS keyed on the wrong token —
    // a `deadline_expired` entry spelt as a catch-all would show a reader a
    // deadline that has not passed.
    expect(screen.queryByText(/wall deadline/i)).toBeNull()
  })

  it('approve posts the decision and nothing else', async () => {
    const posted: unknown[] = []
    renderControls({ evidence: decidable, onPost: (b) => posted.push(b) })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    // `{ decision: 'approve' }` and nothing beside it. The assertion is on the
    // JSON that reached the server, so what it catches is a note DEFAULTED:
    // `note: v.note ?? ''` posts `"note":""`, which on request_changes is the
    // empty note `gates.go:98` rejects. It does NOT catch `note: undefined` —
    // JSON.stringify omits an undefined value and that mutant survives, which
    // decide-gate.ts records beside the line it would change.
    await waitFor(() => expect(posted).toEqual([{ decision: 'approve' }]))
  })

  it('request changes REQUIRES a note before it will post', async () => {
    // Not a UI nicety: `openRevision` seeds the revision target with the note
    // as an artifact of its own (`revisions.go:22-40` step 9, kind
    // ArtifactKindGateNote at `:280`), and a revision with no note re-runs the
    // same work with the same information and reaches the same patch. The note
    // IS the new input, which is why `gates.go:98` refuses an empty one
    // server-side too.
    const posted: unknown[] = []
    renderControls({ evidence: decidable, onPost: (b) => posted.push(b) })
    await userEvent.click(await screen.findByRole('button', { name: /request changes/i }))
    const send = await screen.findByRole('button', { name: /send/i })
    await userEvent.click(send)
    expect(posted).toEqual([])
    expect(await screen.findByRole('alert')).toHaveTextContent(/note/i)

    await userEvent.type(screen.getByLabelText(/note/i), 'use the existing helper')
    await userEvent.click(send)
    await waitFor(() =>
      expect(posted).toEqual([{ decision: 'request_changes', note: 'use the existing helper' }]),
    )
  })

  it('reject asks for confirmation before it ends a run', async () => {
    // Reject is terminal — DecisionReject finishes the run failed. It is the
    // one control in the thumb zone whose mis-tap cannot be undone.
    const posted: unknown[] = []
    renderControls({ evidence: decidable, onPost: (b) => posted.push(b) })
    await userEvent.click(await screen.findByRole('button', { name: /^reject$/i }))
    expect(posted).toEqual([])
    await userEvent.click(await screen.findByRole('button', { name: /reject this run/i }))
    await waitFor(() => expect(posted).toHaveLength(1))
  })

  it('a 409 shows the decision that WON, not just a failure', async () => {
    // ApiGateDecisionSummary carries acted_by_email, decision, note and
    // acted_at, so the screen can say what happened. "Something went wrong" on
    // a gate somebody else just approved is the least useful true statement
    // available.
    //
    // The winner comes from a REFETCH of GET /runs/{id}, not from the 409 body:
    // the problem document carries a sentence and a correlation id and no
    // decision at all, and the mutation has just invalidated ['run', runId]
    // for exactly this reason.
    renderControls({ evidence: decidable, reply: conflict(), refetched: decidedByAlice() })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText(/alice@example.com/)).toBeInTheDocument()
    expect(await screen.findByText(/already/i)).toBeInTheDocument()
  })

  it('falls back to the generic error surface when the winner cannot be read back', async () => {
    // A 409 does not always mean "somebody decided it": `gates.go:167` answers
    // the same way for a node that is not a gate of this run and for an attempt
    // already swept. Naming a decision the refetch does not contain would be
    // the client inventing one, so this path renders the server's own sentence
    // and the correlation id instead.
    renderControls({
      evidence: decidable,
      reply: conflict(),
      refetched: { ...decidedByAlice(), phases: [] },
    })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText('cid-409')).toBeInTheDocument()
    // Anchored, because the SERVER's own sentence — which this panel renders —
    // is "this gate attempt has already been decided". What must be absent is
    // the client's claim to know WHO, and that claim opens the line.
    expect(screen.queryByText(/^already decided/i)).toBeNull()
  })

  it('says when the wall deadline has already passed, before anything is posted', async () => {
    // `can_decide` is TRUE here, which is what makes this an assertion about
    // the CLIENT's own clock and not about the flag. The server does fold the
    // deadline into `can_decide` (api.go:536) — but only as of the instant it
    // composed the response, so a response held open past the deadline looks
    // exactly like this one, and this refusal exists only if the client renders
    // it. The other direction, where the server says expired and the client's
    // clock does not, is decision-rules.testkit.ts case 4. Which refusal wins
    // when both apply is the next test.
    renderControls({ evidence: { ...decidable, wall_deadline: '2020-01-01T00:00:00Z' } })
    expect(await screen.findByText(/deadline/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
  })

  it('puts the deadline ahead of every other refusal', async () => {
    // BOTH refusals are true here, which is the only fixture in which the order
    // is observable at all — with either condition alone the two checks are
    // interchangeable (MEASURED: the swap mutant survived until this test
    // existed).
    //
    // THIS ORDER IS NOT decideGate's ORDER, and this exact state is where the
    // two diverge. The server puts the GRANT first: handleDecideGate authorizes
    // GrantApprove before DecideGate opens its transaction, so for a caller
    // short of approve on an expired run it answers `not_approver` and the
    // endpoint really returns 403 — `api.go:510-518` documents the choice and
    // "not_approver still wins" in gateevidence_integration_test.go pins it
    // against the live endpoint. This surface renders the deadline anyway.
    //
    // That is deliberate, and the reason is not "the deadline is more
    // terminal". It is that this check is NOT a refusal-naming step at all: it
    // is a staleness guard on the response in hand, the only check on this
    // screen that can see time pass after the response was composed, and it
    // stops being unconditional the moment anything is allowed to run ahead of
    // it. Pre-empting `not_approver` is the price of that, and it is a cheap
    // one — no control is offered under either branch, both sentences are true,
    // and `cannot_decide_reason` names what a POST would receive, which is a
    // fact about a request this surface is not going to make.
    //
    // The two orders agree everywhere else: against `self_approval_forbidden`
    // and `evidence_missing` the deadline wins on the server too
    // (`gates.go:176` refuses before the policy at `:189`).
    renderControls({
      evidence: {
        ...decidable,
        can_decide: false,
        cannot_decide_reason: 'not_approver',
        wall_deadline: '2020-01-01T00:00:00Z',
      },
    })
    expect(await screen.findByText(/deadline/i)).toBeInTheDocument()
    expect(screen.queryByText(/approve grant/i)).toBeNull()
  })

  it('invalidates the run on FAILURE, so the conflict surface is not read off a stale cache', async () => {
    // The mutation invalidates in onSettled rather than onSuccess, and this is
    // the only fixture that can tell those apart: the run is already in the
    // cache and FRESH (staleTime is 5s, `api/query-client.ts:9`), so mounting
    // an observer on ['run', 'r1'] serves the cached copy and issues no
    // request. The seeded copy shows no decision on the gate — which is exactly
    // the stale state a 409 means this client is holding — so alice can only
    // appear if the ERROR path invalidated the key.
    //
    // MEASURED: with onSettled changed to onSuccess, this test fails and every
    // other test in the file still passes.
    renderControls({
      evidence: decidable,
      reply: conflict(),
      seedRun: { ...decidedByAlice(), phases: [] },
      refetched: decidedByAlice(),
    })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText(/alice@example.com/)).toBeInTheDocument()
  })

  it('tells its host when a decision was accepted', async () => {
    // The three hosts close a drawer, a sheet or a row on this. Without it the
    // surface stays open on a gate that is no longer waiting.
    const decided: string[] = []
    renderControls({ evidence: decidable, onDecided: () => decided.push('fired') })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    await waitFor(() => expect(decided).toEqual(['fired']))
  })

  it('does NOT tell its host on a 409, because the answer renders inside it', async () => {
    // A 409 means this gate is gone too, so firing here reads as the obviously
    // consistent thing to do — and it would close the host's surface over the
    // panel naming whoever decided first, which is a child of these controls.
    // The wait is what makes the absence assertion mean anything: alice on
    // screen proves the mutation has settled and the error path has run.
    const decided: string[] = []
    renderControls({
      evidence: decidable,
      reply: conflict(),
      refetched: decidedByAlice(),
      onDecided: () => decided.push('fired'),
    })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText(/alice@example.com/)).toBeInTheDocument()
    expect(decided).toEqual([])
  })

  it('says something when the request never reaches the server', async () => {
    // openapi-fetch propagates fetch's own TypeError, so the mutation's error
    // is not always an ApiError — and `ApiErrorPanel` deliberately RETHROWS
    // anything that is not one. Without a narrowing branch a dropped connection
    // is a render-time throw inside whichever route hosts these controls; with
    // one and no message it is a button that visibly does nothing. There is no
    // correlation id to render here and the surface does not invent one.
    renderControls({ evidence: decidable, reply: HttpResponse.error() })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not be completed/i)
  })

  it('renders the correlation id when the server 500s', async () => {
    renderControls({ evidence: decidable, reply: serverError('cid-77') })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText('cid-77')).toBeInTheDocument()
  })

  it('invalidates all five keys of its cache contract, the EVENT LOG among them', async () => {
    // A SPY on the key set, where every other test in this file asserts through
    // a rendered surface, and the reason is that four of these five keys have
    // no host here to observe them: `['tasks']` belongs to a screen these
    // controls never mount, so a behavioural proof would mean rendering the
    // Tasks list around a gate decision to watch one refetch. What the contract
    // actually says is which keys go out, and that is what this reads.
    //
    // The set is asserted WHOLE rather than with a `toContainEqual` per key.
    // The keys are a set the stream's bridge must match — a decision and the
    // frames that follow it are idempotent only if both invalidate the same
    // things — so a key silently dropped from either side is the failure worth
    // catching, and only an equality assertion catches a drop.
    //
    // THE WHOLE FILTER OBJECT, not `c[0]?.queryKey`, and that is Task 12's
    // doing. `exact: true` on the run key is what keeps `['run', id, 'events']`
    // off the prefix — which is why the log had to be added explicitly on the
    // line below it — and a reader that projected the key alone would leave the
    // flag unfalsifiable here: delete it and the five keys still come out in
    // this order.
    const spy = vi.spyOn(queryClient, 'invalidateQueries')
    renderControls({ evidence: decidable })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    await waitFor(() =>
      expect(spy.mock.calls.map((c) => c[0])).toEqual([
        { queryKey: ['approvals'] },
        { queryKey: ['run', 'r1'], exact: true },
        { queryKey: ['runs'] },
        { queryKey: ['tasks'] },
        { queryKey: ['run', 'r1', 'events'] },
      ]),
    )
  })
})
