import type { GateDecisionSummary } from './decide-gate'

/** Past tense, degrading to the raw token: `decision` is a bare string on the wire. */
const DECIDED: Record<string, string> = {
  approve: 'approved',
  request_changes: 'requested changes on',
  reject: 'rejected',
}

/**
 * What a gate that is no longer waiting looks like: who decided it, what they
 * decided, what they said, and when.
 *
 * ONE COMPONENT, TWO HOSTS, for the same reason `ApprovalSurface` is one: the
 * deep approval link and the run-detail gate phase both land on a decided gate
 * routinely — a notification opened a minute late, and a timeline read after
 * the fact — and two renderings of the same four fields is how the two screens
 * come to disagree about which of them they show.
 *
 * It takes the DECISION rather than the phase, because the two hosts disagree
 * about what a phase with neither evidence nor decision means. On the approval
 * route that is a gate in a third state and gets its own sentence; on the
 * timeline it is any ordinary phase and gets its artifacts.
 */
export function GateDecision(props: { decision: GateDecisionSummary }) {
  const { decision } = props
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
