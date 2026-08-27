import type { PlanView, VerdictView } from './artifact-query'

/**
 * THE TWO TYPED PAYLOADS, AS PROSE — the presentation half of `readPlan` and
 * `readVerdict`, with nothing in it that knows about a gate.
 *
 * Both were local to `approval-surface.tsx` until the artifact view became
 * their second host. A `plan` artifact and a `review_verdict` artifact are the
 * same payloads whether a gate is examining them or somebody followed a link to
 * one, and two renderings of them is how the two screens come to disagree about
 * what the gate is deciding on — the reason `GateDecision` and `ApprovalSurface`
 * are each one component with two hosts.
 *
 * They sit in `approvals/` beside the readers that produce their input rather
 * than in `patch/` or `ui/`: `PlanView` and `VerdictView` are declared in
 * `artifact-query.ts`, and a module at its own group root satisfies `web:arch`'s
 * misplacement clamp with importers in any group
 * (`tools/arch/import-graph.ts:126-140`).
 *
 * NEITHER TAKES A QUERY OR AN ERROR. A null reader result is "this payload
 * could not be read" and that is all either of them says about it; whether a
 * FETCH failed, and what to say about that, stays with the host that started
 * it — which is why `SummaryBlock` keeps its own error branch and its
 * `agent-summary` frame.
 */
export function PlanProse(props: { plan: PlanView | null }) {
  if (props.plan === null) {
    return <p className="text-text-muted text-sm">This plan could not be read.</p>
  }
  return (
    <section className="flex flex-col gap-2">
      <p className="text-text">{props.plan.summary}</p>
      <ol className="flex list-decimal flex-col gap-1 pl-5">
        {props.plan.steps.map((step, i) => (
          // A plan step is a title and an optional detail (`ApiPlanStep`) and
          // nothing else — two steps may legitimately share a title, so the
          // index is the only unique key. Stable for the same reason as the
          // findings list below: one read of one immutable artifact.
          // biome-ignore lint/suspicious/noArrayIndexKey: no identity on the wire
          <li key={i}>
            <p className="text-text text-sm">{step.title}</p>
            {step.detail !== undefined && step.detail !== '' && (
              <p className="text-text-muted text-sm">{step.detail}</p>
            )}
          </li>
        ))}
      </ol>
    </section>
  )
}

/** A read verdict as its sentence and its findings. */
export function VerdictProse(props: { verdict: VerdictView }) {
  const { verdict } = props
  return (
    <>
      <p className="text-text font-medium">
        {VERDICT_SENTENCE[verdict.verdict] ?? verdict.verdict}
      </p>
      {verdict.findings.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1">
          {verdict.findings.map((f, i) => (
            <li
              // A finding carries no id on the wire (`ApiReviewFinding` is
              // severity, message and an optional file/line), so nothing in it
              // is guaranteed unique. The index is the only key available, and
              // it is stable here: this list is one read of one immutable
              // artifact and is never reordered or filtered.
              // biome-ignore lint/suspicious/noArrayIndexKey: no identity on the wire
              key={i}
              className="text-text-muted text-sm"
            >
              <span className="text-text font-mono text-xs">{f.severity}</span> {f.message}
              {f.file !== undefined && f.file !== '' && (
                <span className="font-mono text-xs">
                  {' '}
                  {f.file}
                  {f.line !== undefined && f.line > 0 ? `:${f.line}` : ''}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

/**
 * The agent's verdict as a sentence, keyed by `string` and not by a union: the
 * wire type is `verdict: string`, so a verdict added server-side has to degrade
 * to the raw token rather than render `undefined`. The vocabulary is
 * deliberately the one a human gate decision uses (`payload_dto.go:27-29`), so
 * a reader is not learning two.
 */
const VERDICT_SENTENCE: Record<string, string> = {
  approve: 'The review agent approved this change.',
  request_changes: 'The review agent requested changes.',
  reject: 'The review agent rejected this change.',
}
