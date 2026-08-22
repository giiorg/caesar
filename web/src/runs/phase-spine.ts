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
function isNewer(a: Phase, b: Phase): boolean {
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
    if (seen === undefined || isNewer(phase, seen)) newest.set(phase.node_id, phase)
  }
  return [...newest.values()]
}

/** The newest attempt of ONE node, or null when the run has no such node. */
export function newestPhaseOf(run: Run, nodeId: string): Phase | null {
  let best: Phase | null = null
  for (const phase of run.phases ?? []) {
    if (phase.node_id !== nodeId) continue
    if (best === null || isNewer(phase, best)) best = phase
  }
  return best
}
