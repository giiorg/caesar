import type { components } from '../api/schema'

export type PhaseSummary = components['schemas']['ApiPhaseSummaryResponse']

/**
 * The run's shape as a row of dots — done, active, gate, not yet reached —
 * which is what locked Home and Tasks both show. A scalar "current phase"
 * cannot render this, and that is why `phase_summary` is an array on the wire
 * (`dto.go:127-131`: every node of the compiled definition, materialized or
 * not, in topological order).
 *
 * `data-state` and `data-node-type` are on the DOM because they are the only
 * way a test can assert what a person SEES here: the whole content of this
 * component is colour and shape, and a test that read class names would be
 * asserting Tailwind's spelling rather than the design.
 *
 * A gate dot is drawn differently from a work dot, and the type comes off the
 * wire rather than being inferred from the node id — a client that guessed
 * "the node called gate is the gate" would be wrong on the first workflow that
 * names one `approval`. The fixtures in `routes/_app/index.test.tsx` spell the
 * id and the type differently for exactly that reason, which is what makes the
 * `data-node-type` assertion falsifiable.
 */
export function PhaseDots(props: { phases: PhaseSummary[] | null }) {
  // NULL AND EMPTY ARE THE SAME ANSWER AND IT IS NOT "no work". `phase_summary`
  // is required AND nullable (a Go nil slice marshals to null), and the plan
  // this list is built from always has at least one node — so neither shape
  // describes a run, it describes a run whose shape did not arrive. An empty
  // <ul> would draw as a card claiming a run with nothing in it, which is the
  // plausible empty content a stub never fakes.
  if (props.phases === null || props.phases.length === 0) {
    return <p className="text-text-muted text-xs">No phases reported for this run.</p>
  }
  return (
    <ul data-testid="phase-dots" aria-label="Phases" className="flex flex-wrap items-center gap-1">
      {props.phases.map((p) => (
        <li
          // Keyed by node id: `phase_summary` carries one entry per node of the
          // compiled definition, so the ids are unique within a run by
          // construction.
          key={p.node_id}
          // `?? 'not-started'` and NOT `?? 'pending'`, which is the distinction
          // the contract went out of its way to make nullable: `pending` means
          // a row exists and is claimable, null means the orchestrator has not
          // reached this node at all (`dto.go:141-146`).
          data-state={p.status ?? 'not-started'}
          data-node-type={p.node_type}
          title={`${p.node_id} — ${p.status ?? 'not started'}`}
          className={dotClass(p)}
        />
      ))}
    </ul>
  )
}

/**
 * Semantic tokens only — `web:arch` rejects a Tailwind hue name outright, and
 * `tools/arch/tokens.ts` is what makes that mechanical rather than remembered.
 *
 * The two shapes that are NOT a filled circle carry the two meanings colour
 * alone cannot: an unfilled outline is `pending` (a row exists, nobody has
 * claimed it) and a square is a gate (a decision, not work). Nothing here is
 * asserted by a test — `data-state` and `data-node-type` are — so this is
 * presentation and is free to change without one.
 */
function dotClass(p: PhaseSummary): string {
  const shape = p.node_type === 'gate' ? 'rounded-xs' : 'rounded-full'
  return `size-2.5 shrink-0 ${shape} ${FILL[p.status ?? 'not-started'] ?? 'bg-border'}`
}

const FILL: Record<string, string> = {
  // A dim dot for a node with no row, an OUTLINE for one that has a row and no
  // claimant. Same colour, different weight, because the two are one step apart
  // and the contract's whole point is that they are not the same state.
  'not-started': 'bg-border',
  pending: 'border-border border',
  running: 'bg-accent ring-accent/40 ring-2',
  reconciling: 'bg-accent',
  waiting_decision: 'bg-warning',
  succeeded: 'bg-success',
  failed: 'bg-danger',
  dead: 'bg-danger',
  // Superseded is not a failure: request_changes rewinds a slice of the run and
  // the old rows keep their place in the shape.
  superseded: 'bg-border',
}
