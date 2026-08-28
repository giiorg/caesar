import { useQuery } from '@tanstack/react-query'
import { QueryFailure } from '../errors/query-failure'
import type { GateEvidence } from '../gates/decide-gate'
import { GateControls } from '../gates/gate-controls'
import { priorRejectedArtifactId } from '../gates/prior-decision'
import { decodePatch, type ParsedPatch } from '../patch/decode-patch'
import { FileAccordion } from '../patch/file-accordion'
import type { Phase } from '../runs/phase-spine'
import {
  artifactQueryOptions,
  type PlanView,
  readPatchDiff,
  readPlan,
  readVerdict,
  type VerdictView,
} from './artifact-query'
import { PlanProse, VerdictProse } from './payload-prose'

/**
 * ONE COMPONENT, TWO HOSTS. This task mounts it full-screen under
 * `/approvals/$runId/$nodeId`; Task 16 mounts it inside the run-detail gate
 * phase. decisions §7 decides twice that a gate is decidable where its evidence
 * is shown, and building the screen once is what stops the two decisions
 * drifting apart — including the §5 rule about the controls, which
 * `decision-rules.testkit.ts` then re-runs against every host.
 *
 * The host supplies position and nothing else: this component takes the
 * evidence it is given rather than looking it up, because the two hosts find it
 * in different places (an inbox row and a run's phase). `phases` arrives the
 * same way and for the same reason — both hosts already hold the run at the
 * mount site, and a `runQueryOptions` fetch started in here would put a run
 * request inside every test that renders this component standalone.
 */
export function ApprovalSurface(props: {
  runId: string
  nodeId: string
  evidence: GateEvidence
  /**
   * The run's phases — `run.phases ?? []`. Required and nullable on the wire
   * (`api/schema.d.ts:1011`, a Go nil slice marshals to `null`), which is why
   * the coalesce is at both hosts rather than optional-chained in here.
   *
   * THE WHOLE RUN'S, not this gate's: the decided attempts this reads are
   * superseded rows for the same node id, and `ListPhasesForRun` carries every
   * one of them (`store/queries.sql:225-226`).
   */
  phases: readonly Phase[]
  /** The run's `execution_used_ms`, for the duration cell. */
  executionUsedMs: number
}) {
  const { evidence } = props
  const summary = useQuery(artifactQueryOptions(props.runId, evidence.summary_artifact_id ?? ''))
  // Branched on the TYPE before the fetch, which is the reason
  // `examined_artifact_type` is on the wire at all. A gate examines exactly one
  // plan or patch (`workflows/validate.go:209`), so a third value is not
  // reachable through a validated definition — and asking for it anyway would
  // spend a request to learn what the evidence already said.
  const examinable = evidence.examined_artifact_type === 'patch' || isPlan(evidence)
  const examined = useQuery(
    artifactQueryOptions(props.runId, examinable ? evidence.examined_artifact_id : ''),
  )
  // THE ARTIFACT THIS GATE ALREADY SENT BACK, resolved from the decision record
  // the host already has, so this fetch starts beside the other two instead of
  // behind them. It is null on a first round, and `artifactQueryOptions` is
  // disabled on the empty id, so an ordinary gate spends no request here.
  //
  // THE `patch` GUARD BUYS A REQUEST, NOT CORRECTNESS, and the distinction is
  // the whole reason it is worth a comment. The rule this replaced needed its
  // type guard to stay correct — it compared artifact rows, so without one a
  // plan gate reached `undefined === undefined` and accused the operator. Here
  // correctness is already held one layer down by `patch` and `identical`
  // below: `patch` is null for any gate not examining a patch, and `identical`
  // requires `patch !== null`. What this guard buys is keeping a plan gate from
  // spending a request to learn the banner cannot fire.
  //
  // Nor is the artifact it names a `patch` row by construction any more — the
  // id comes from a decision rather than from a type-filtered artifact list,
  // and a gate may legally examine a plan (`workflows/validate.go:209`). What
  // stops a plan payload reaching the comparison is `priorPatch` below, where
  // `readPatchDiff` answers null for any payload carrying no `diff`, and the
  // `priorPatch !== null` conjunct of `identical` beside it.
  const priorId =
    evidence.examined_artifact_type === 'patch'
      ? priorRejectedArtifactId(props.phases, props.nodeId)
      : null
  const prior = useQuery(artifactQueryOptions(props.runId, priorId ?? ''))

  // ALL THREE QUERIES, ONE GATE, and it is about ORDER rather than about
  // coupling. The locked order is summary-then-facts-then-evidence, and a
  // surface that revealed each block as its own query landed would put the facts
  // row on screen first whenever the diff came back first. Failure stays
  // independent: `isLoading` is `isPending && isFetching` (query-core
  // `queryObserver.js:310`), so a query that has ERRORED is ready and a query
  // that is DISABLED — no artifact id, therefore pending forever and never
  // fetching — is ready too. Each block below then renders from its own
  // query's state, and a 404 on one leaves the other on screen.
  //
  // The prior patch is inside the gate for a second reason of its own: the
  // banner it decides sits ABOVE everything, and a warning that arrives after a
  // person has begun reading the diff both moves the page under them and misses
  // the moment it exists for.
  if (summary.isLoading || examined.isLoading || prior.isLoading) {
    return <p className="text-text-muted text-sm">Reading this gate's evidence…</p>
  }

  const verdict = readVerdict(summary.data)
  const patch = evidence.examined_artifact_type === 'patch' ? readPatchDiff(examined.data) : null
  // TWO READABLE DIFFS, THEN EQUALITY — never equality alone. `readPatchDiff`
  // answers null for a payload whose `diff` is missing, non-string or empty, so
  // `readPatchDiff(a) === readPatchDiff(b)` is `null === null` for two payloads
  // that carry no diff at all: TRUE, and a banner accusing an operator's
  // revision of changing nothing on a gate that never had a diff to compare.
  // That is the `undefined === undefined` this whole comparison exists to avoid,
  // wearing the reader's clothes. The base64 is compared as it arrived — decode
  // nothing; identical bytes have identical base64.
  const priorPatch = readPatchDiff(prior.data)
  const identical = patch !== null && priorPatch !== null && patch === priorPatch
  // DECODED EXACTLY ONCE, here, and never re-encoded: a patch is arbitrary
  // bytes and the base64 is the contract, not an encoding choice.
  const parsed = patch === null ? null : decodePatch(patch)
  const plan = isPlan(evidence) ? readPlan(examined.data) : null

  return (
    <div className="flex flex-col gap-4">
      {identical && (
        // ABOVE THE SUMMARY, not merely above the diff. Everything below this
        // reads differently once it is true — the agent's fresh-sounding verdict
        // included — so it is the first sentence on the screen or it is decoration.
        <div role="status" className="border-warning text-text bg-surface border px-3 py-2 text-sm">
          This revision is byte-identical to what was already reviewed — the previous
          request-changes note was not acted on.
        </div>
      )}
      <SummaryBlock evidence={evidence} query={summary} verdict={verdict} />
      <HardFacts
        evidence={evidence}
        summaryFailed={evidence.summary_artifact_id !== null && verdict === null}
        verdict={verdict}
        parsed={parsed}
        executionUsedMs={props.executionUsedMs}
      />
      <ExaminedArtifact evidence={evidence} query={examined} parsed={parsed} plan={plan} />
      {/* The thumb zone the locked design asks for. GateControls supplies the
          controls and this supplies the position — the split its own doc
          comment states. `bg-ground` rather than a transparent bar: the diff
          scrolls underneath it.

          `bottom-tab-bar` and not `bottom-0`: on a phone the tab bar is fixed
          over the bottom of the viewport, and a decision bar that stuck to 0
          would sit under it — the three controls this whole screen exists for.
          The same token the bar's own height comes from, so the two cannot
          drift. `md:bottom-0` puts it back where the bar is display:none.
          `z-10` matches the run-detail sticky header and stays under the
          bar's z-20.

          THIS OFFSET IS NOT CURRENTLY FALSIFIABLE, and the honest record of
          that belongs here rather than in a report nobody reads next. MEASURED
          on both hosts: swapping this back to `bottom-0` leaves every e2e
          assertion green, because a bottom-sticky element only leaves its flow
          position once its containing block is taller than the viewport, and
          neither host's fixture gate is — forcing this element's `bottom` to
          0px, 64px and 200px in the live page moved its box not at all. So the
          class is written from the invariant it states, NOT from a lane that
          holds it: the first real gate with a long diff is where it starts
          mattering, and a shorter viewport would too. Recorded as a geometry
          debt for Task 32, which owns exactly this class of hole. */}
      <div className="bg-ground border-border sticky bottom-tab-bar z-10 border-t py-3 md:bottom-0">
        <GateControls runId={props.runId} nodeId={props.nodeId} evidence={evidence} />
      </div>
    </div>
  )
}

const isPlan = (evidence: GateEvidence) => evidence.examined_artifact_type === 'plan'

/**
 * 1. THE AGENT SUMMARY, and the block that is absent rather than empty.
 *
 * `summary_artifact_id` is null on a gate that consumes no review verdict, and
 * that is a legal workflow rather than a degenerate one:
 * `workflows/validate.go:212` caps a gate at one verdict and permits zero. A
 * block rendered anyway — a heading over nothing — reads as an agent that had
 * nothing to say, which is a different and false statement.
 */
function SummaryBlock(props: {
  evidence: GateEvidence
  query: { isError: boolean; error: unknown }
  verdict: VerdictView | null
}) {
  if (props.evidence.summary_artifact_id === null) return null
  if (props.query.isError) {
    return (
      <QueryFailure error={props.query.error} fallback="The review summary could not be read." />
    )
  }
  if (props.verdict === null) {
    return <p className="text-text-muted text-sm">The review summary could not be read.</p>
  }
  // THE FRAME STAYS HERE, THE PROSE DOES NOT. `agent-summary` marks the block
  // this gate consumes as its summary, which is a statement about the GATE —
  // the artifact view renders the same verdict with no gate in sight and must
  // not claim to be one.
  return (
    <section data-testid="agent-summary" className="border-border bg-surface rounded-md border p-3">
      <VerdictProse verdict={props.verdict} />
    </section>
  )
}

/**
 * 2. THE HARD FACTS — files · ±lines · tests · duration.
 *
 * COST IS ABSENT BECAUSE IT DOES NOT EXIST. decisions §7's locked Approval (A)
 * reads "files · ±lines · tests · cost", and the fourth of those has no field
 * behind it — MEASURED, and the measurement is one grep: `cost`, `usage`,
 * `token` and `price` appear as a property name nowhere in `api/schema.d.ts`.
 * The only number in this neighbourhood is `execution_used_ms`, which is time.
 * Restoring the cell needs usage accounting nothing in this system performs,
 * and a number invented here would be worse than its absence.
 */
function HardFacts(props: {
  evidence: GateEvidence
  /** A summary artifact was named and could not be read — not the same as none. */
  summaryFailed: boolean
  verdict: VerdictView | null
  parsed: ParsedPatch | null
  executionUsedMs: number
}) {
  const cells: string[] = []
  // The files cell belongs to a PATCH gate. A plan gate has no files and no ±
  // lines, and "0 files" would be a measurement rather than the absence of one.
  if (props.evidence.examined_artifact_type === 'patch') {
    cells.push(
      props.parsed === null
        ? 'files not available'
        : `${props.parsed.files.length} ${props.parsed.files.length === 1 ? 'file' : 'files'}`,
    )
    if (props.parsed !== null) cells.push(`+${props.parsed.added}`, `−${props.parsed.removed}`)
  }
  cells.push(testsCell(props))
  cells.push(formatDuration(props.executionUsedMs))

  return (
    <div data-testid="hard-facts" className="text-text-muted flex flex-wrap gap-2 text-sm">
      {cells.map((cell, i) => (
        // Same reasoning as the findings list: the cells are computed strings
        // with no identity of their own, and the row is never reordered.
        // biome-ignore lint/suspicious/noArrayIndexKey: computed row, fixed order
        <span key={i}>{i === 0 ? cell : `· ${cell}`}</span>
      ))}
    </div>
  )
}

/**
 * NO TICK WHERE THERE IS NO RESULT. `tests: null` means the harness reported
 * nothing, `not_run` means it reported that it ran none, and the two are
 * different sentences (`payload_dto.go:61-64`). A summary artifact that could
 * not be read is a third thing again — nobody said anything about tests
 * because nobody could read what was said.
 */
function testsCell(props: { summaryFailed: boolean; verdict: VerdictView | null }): string {
  if (props.summaryFailed) return 'tests unknown'
  const tests = props.verdict?.tests ?? null
  if (tests === null) return 'tests not reported'
  const counts: string[] = []
  if (tests.passed !== undefined) counts.push(`${tests.passed} passed`)
  if (tests.failed !== undefined) counts.push(`${tests.failed} failed`)
  return counts.length === 0
    ? `tests ${tests.status}`
    : `tests ${tests.status} — ${counts.join(', ')}`
}

/**
 * `execution_used_ms` as a human duration.
 *
 * Rounded to whole seconds because it is a budget reading and not a benchmark,
 * and truncated at the two largest units for the same reason: `2h 5m` is what a
 * person deciding a gate wants, and the trailing seconds are noise at that
 * scale.
 */
function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000))
  if (total < 60) return `${total}s`
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m ${total % 60}s`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

/** 3. THE EXAMINED ARTIFACT, branched on the type the evidence declared. */
function ExaminedArtifact(props: {
  evidence: GateEvidence
  query: { isError: boolean; error: unknown }
  parsed: ParsedPatch | null
  plan: PlanView | null
}) {
  const { evidence } = props
  // Empty id, or a type this screen has no renderer for. The first is the state
  // `decidability()` reports as `evidence_missing`: the pinned definition binds
  // an artifact the run never produced, and `runs/api/api.go:512-515` records
  // that decideGate answers 500 for it rather than a refusal — undecidable by
  // anybody. The second is unreachable through a validated definition
  // (`workflows/validate.go:209`), which is why both get the one honest
  // sentence rather than a second message nothing can produce.
  if (
    evidence.examined_artifact_id === '' ||
    (evidence.examined_artifact_type !== 'patch' && !isPlan(evidence))
  ) {
    return <p className="text-text-muted text-sm">The evidence for this gate is not available.</p>
  }
  if (props.query.isError) {
    return (
      <QueryFailure error={props.query.error} fallback="The examined artifact could not be read." />
    )
  }
  if (isPlan(evidence)) return <PlanProse plan={props.plan} />
  if (props.parsed === null) {
    return <p className="text-text-muted text-sm">The examined patch could not be read.</p>
  }
  return <FileAccordion parsed={props.parsed} />
}
