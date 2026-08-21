import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import type { components } from '../../api/schema'
import { ApprovalSurface } from '../../approvals/approval-surface'
import { type Run, runQueryOptions } from '../../runs/run-query'

type Phase = components['schemas']['ApiPhaseResponse']

/**
 * The approval surface, full-screen. A URL, not a modal: this is the screen a
 * person is sent to by a notification, and a gate under review is exactly the
 * thing they will want to send to somebody else.
 *
 * It loads the RUN rather than re-reading the inbox, because the run carries
 * `execution_used_ms` for the hard-facts row and the gate phase carries the
 * same evidence the inbox does — so a deep link works with no inbox in the
 * cache at all.
 */
export const Route = createFileRoute('/_app/approvals/$runId/$nodeId')({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(runQueryOptions(params.runId)),
  component: ApprovalScreen,
})

function ApprovalScreen() {
  const { runId, nodeId } = Route.useParams()
  const { data: run } = useSuspenseQuery(runQueryOptions(runId))
  const phase = latestPhase(run, nodeId)

  return (
    <section className="flex flex-col gap-4">
      <header>
        <h1 className="text-text font-medium">{run.task_title}</h1>
        <p className="text-text-muted font-mono text-xs">
          {run.repo_slug} · {nodeId}
        </p>
      </header>
      <GateBody run={run} nodeId={nodeId} phase={phase} />
    </section>
  )
}

function GateBody(props: { run: Run; nodeId: string; phase: Phase | null }) {
  const { phase } = props
  if (phase === null) {
    // A node id that is not a phase of this run at all — a mistyped or stale
    // link. `decideGate` answers the same way for it (`runs/gates.go:167`), so
    // saying "already decided" here would be a guess about which of the two
    // this is.
    return <p className="text-text-muted text-sm">This run has no gate called {props.nodeId}.</p>
  }
  // TRUTHY, not `!== undefined`. `evidence` is a nullable `$ref`, which this
  // toolchain renders optional-and-non-nullable, so the runtime value can be
  // `null` where the type says it cannot — and `exactOptionalPropertyTypes`
  // makes `!== undefined` narrow to the object while the value is null.
  //
  // ABSENT EVIDENCE IS THE ORDINARY CASE, not an error: the DTO builds it only
  // for a gate that is still waiting, so this is what a notification opened a
  // minute too late looks like. An empty approval screen would read as a gate
  // waiting on the reader.
  if (!phase.evidence) return <Decided phase={phase} />
  return (
    <ApprovalSurface
      runId={props.run.id}
      nodeId={props.nodeId}
      evidence={phase.evidence}
      executionUsedMs={props.run.execution_used_ms}
    />
  )
}

/** Past tense, degrading to the raw token: `decision` is a bare string on the wire. */
const DECIDED: Record<string, string> = {
  approve: 'approved',
  request_changes: 'requested changes on',
  reject: 'rejected',
}

function Decided(props: { phase: Phase }) {
  const decision = props.phase.decision
  if (!decision) {
    // No evidence and no decision: the gate is neither waiting nor decided —
    // a superseded attempt, or one swept when its run ended. Naming a decision
    // that is not there is the one thing this must not do.
    return (
      <p className="text-text-muted text-sm">
        This gate is no longer waiting, and no decision is recorded against this attempt.
      </p>
    )
  }
  return (
    <section className="border-border bg-surface rounded-md border p-3">
      {/* One sentence, one element. testing-library matches a node's DIRECT
          text children, so wrapping the address in a <span> would split the
          sentence and stop `findByText(/alice@example.com/)` resolving to it. */}
      <p className="text-text">
        {decision.acted_by_email} {DECIDED[decision.decision] ?? decision.decision} this gate.
      </p>
      {decision.note !== '' && <p className="text-text-muted text-sm">{decision.note}</p>}
      <p className="text-text-muted text-sm">
        <time dateTime={decision.acted_at}>{decision.acted_at}</time>
      </p>
    </section>
  )
}

/**
 * The newest attempt of one gate node, by (generation, attempt).
 *
 * NOT BY POSITION: request_changes bumps the run's generation and supersedes
 * every live row below the gate (`runs/revisions.go:22-40`), so the gate comes
 * back under the same node id at a new generation and one run really can carry
 * several phases for it. Trusting the array's order would make the answer
 * depend on a SQL `ORDER BY` this file cannot see.
 */
function latestPhase(run: Run, nodeId: string): Phase | null {
  let best: Phase | null = null
  for (const phase of run.phases ?? []) {
    if (phase.node_id !== nodeId) continue
    if (
      best === null ||
      phase.generation > best.generation ||
      (phase.generation === best.generation && phase.attempt > best.attempt)
    ) {
      best = phase
    }
  }
  return best
}
