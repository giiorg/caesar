import type { components } from '../api/schema'
import type { Run } from './run-query'

export type Phase = components['schemas']['ApiPhaseResponse']

/**
 * Is `a` a newer attempt of its node than `b`?
 *
 * BY (generation, attempt) AND NOT BY POSITION. request_changes bumps the run's
 * generation and supersedes every live row below the gate
 * (`runs/revisions.go:22-40`), so the gate comes back under the same node id at
 * a new generation and one run really does carry several phases for it.
 * Trusting the array's order instead would make the answer depend on a SQL
 * `ORDER BY` no client file can see.
 *
 * THE ATTEMPT ORDINAL WOULD ANSWER THE SAME QUESTION TODAY and the pair is
 * still what is compared. A rematerialized node CONTINUES its attempt ordinal
 * rather than restarting — `store/queries.sql:719-723` says so in as many words
 * and `revisions.go:187-198` materializes the revision target at
 * `MaxAttemptForNode + 1` — so a higher generation currently implies a higher
 * attempt and either comparison picks the same row. Comparing generation first
 * is what keeps this right if that ever stops being true, and it is the reading
 * `attempt` cannot express: generation is the run's revision round.
 */
export function isNewerAttempt(a: Phase, b: Phase): boolean {
  if (a.generation !== b.generation) return a.generation > b.generation
  return a.attempt > b.attempt
}

/**
 * The run's phase graph as ONE ROW PER NODE — the newest attempt of each, in
 * the order the node first appears in `run.phases`.
 *
 * The insertion order is the Map's, and that is load-bearing rather than
 * incidental: `Map.prototype.set` on a key that already exists replaces the
 * value and leaves the entry where it was, so a node whose newest attempt
 * arrives late keeps the position its FIRST attempt gave it. That first
 * position is materialization order (`store/queries.sql:226` orders by
 * `created_at, node_id, attempt`), which is the run's own reading of its graph;
 * ordering by the newest attempt instead would make a retried node jump to the
 * bottom of a timeline the reader is watching.
 */
export function newestPhasePerNode(run: Run): Phase[] {
  const newest = new Map<string, Phase>()
  for (const phase of run.phases ?? []) {
    const seen = newest.get(phase.node_id)
    if (seen === undefined || isNewerAttempt(phase, seen)) newest.set(phase.node_id, phase)
  }
  return [...newest.values()]
}

/**
 * The row one attempt BELOW `phase` for the same node, or null.
 *
 * WHY A CLIENT WANTS IT: a row that is waiting has no outcome yet, so the code
 * that parked it — the `rate_limited` behind a "resumable" line — is on the
 * attempt before. `phaseStatusLine` is the caller.
 *
 * KEYED ON (node_id, attempt) AND NOT ON GENERATION, unlike `isNewerAttempt` above,
 * because that pair already identifies one row: `phase_runs` carries
 * `UNIQUE (run_id, node_id, attempt)` (`schema.sql:267`) and a rematerialized
 * node CONTINUES the ordinal rather than restarting it
 * (`store/queries.sql:719-723`). Adding `generation` would not narrow the
 * match, it would break it — the wait that pushed this attempt past its cap
 * may well have been charged a revision round ago.
 */
export function priorAttemptOf(run: Run, phase: Phase): Phase | null {
  const prior = (run.phases ?? []).find(
    (p) => p.node_id === phase.node_id && p.attempt === phase.attempt - 1,
  )
  return prior ?? null
}

/** The newest attempt of ONE node, or null when the run has no such node. */
export function newestPhaseOf(run: Run, nodeId: string): Phase | null {
  let best: Phase | null = null
  for (const phase of run.phases ?? []) {
    if (phase.node_id !== nodeId) continue
    if (best === null || isNewerAttempt(phase, best)) best = phase
  }
  return best
}

/**
 * The phase a LINK names: one attempt of one node, or the node's newest when
 * the link names no attempt at all.
 *
 * WHY THE NEWEST IS NOT ENOUGH. `newestPhasePerNode` collapses a node to one
 * row, which is what a timeline wants and what every `?phase=` link got until
 * now. A `gate.decided` event is the opposite case: it records a decision made
 * on ONE attempt, and by the time a reader clicks it the gate has usually come
 * back at a higher generation — so "open this node" would show them a different
 * decision than the row they clicked, made by somebody else, about a different
 * patch. The event carries `attempt` and `generation`
 * (`runs/gates.go:228-237`, `runs/revisions.go:212-221`), so the link can say
 * which one.
 *
 * GENERATION IS A CONSISTENCY CHECK ON THE LINK, NOT A SECOND KEY — and
 * `priorAttemptOf`'s paragraph in this file, which says adding `generation` to
 * a lookup buys no narrowing, is right about that and is not being contradicted
 * here. The pair in the URL was stamped on the event by the very code that
 * wrote the phase row, so a URL naming a (generation, attempt) combination the
 * row does not carry is malformed or hand-mangled, and answering null — an
 * honest absence the caller renders as a sentence — beats rendering a row the
 * link did not name. It never rejects a well-formed link: a rematerialized node
 * writes a NEW row at a NEW attempt rather than editing an old row's generation
 * (`runs/revisions.go:187-198`, over `MaxAttemptForNode` at
 * `store/queries.sql:719-723`), so (node_id, attempt) → generation never moves
 * once it is written.
 *
 * BOTH HALVES OR NEITHER. Half a selector is not a target — `generation` alone
 * names every attempt of that round — so a partial one falls through to the
 * newest, which is exactly what a link with no selector at all means. That
 * fall-through is also why nothing on the screen moves until a selector is in
 * the URL: `newestPhaseOf` and `newestPhasePerNode` run one comparator over one
 * input and answer the same row for the same node.
 */
export function phaseForSelector(
  run: Run,
  nodeId: string,
  generation?: number,
  attempt?: number,
): Phase | null {
  if (generation === undefined || attempt === undefined) return newestPhaseOf(run, nodeId)
  for (const phase of run.phases ?? []) {
    if (phase.node_id === nodeId && phase.generation === generation && phase.attempt === attempt) {
      return phase
    }
  }
  return null
}
