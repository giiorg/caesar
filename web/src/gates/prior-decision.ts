import { isNewerAttempt, type Phase } from '../runs/phase-spine'

/**
 * The artifact the last human to decide this gate SENT BACK, by id, or null.
 *
 * READ FROM THE RECORD, NOT INFERRED FROM ARTIFACT METADATA. A gate decision
 * carries the artifact the person actually looked at —
 * `GateDecisionSummary.ExaminedArtifactID`, `required:"true"`
 * (`runs/api/dto.go:363`) — so the predecessor is something the control plane
 * stated rather than something this file reconstructs. What it replaces walked
 * `run.artifacts` for the newest row below the examined one's generation, which
 * was sound only while a node's rounds and a gate's rounds stayed in step.
 *
 * DECIDED ATTEMPTS ARE ON THE WIRE ALREADY, so this costs no request.
 * `ListPhasesForRun` is `SELECT * FROM phase_runs WHERE run_id = $1 ORDER BY
 * created_at, node_id, attempt` (`store/queries.sql:225-226`) — no generation
 * filter and no attempt filter — and decisions are attached per ATTEMPT row
 * rather than per node (`runs/tasks.go:584-596`, keyed on `d.PhaseRunID`). The
 * query says why in as many words: "after a revision both gate attempts carry
 * the same node id" (`store/queries.sql:456-460`). So the superseded gate
 * attempt from the previous round is already in the `phases` this screen holds.
 *
 * FILTERED ON THE GATE'S OWN `node_id`, which is `props.nodeId` at the only
 * caller. The rule it replaces filtered on the PRODUCER's node id, and a
 * producer feeding two gates was genuinely ambiguous there. Two gates are two
 * node ids, so there is nothing here to disambiguate.
 *
 * NEWEST DECIDED, THEN REQUIRE `request_changes` — and not the other way
 * round. Filtering to `request_changes` first and then taking the newest would
 * skip past an intervening approval, and the sentence the caller renders
 * ("the previous request-changes note was not acted on") would then be false:
 * it WAS acted on, and approved. An approval is not an unacted-on note, so it
 * answers null and the banner stays off.
 *
 * THAT ORDERING IS A DEFENCE, NOT A LIVE CASE, and saying otherwise here would
 * be inventing a bug to justify a line. An approve finishes the gate's phase as
 * `succeeded` (`runs/gates.go:254`); a node whose newest attempt row succeeded
 * is `live` and is never rematerialized (`runs/orchestrator.go:1236`); and the
 * one statement that can move a row out of `succeeded` is scoped to the
 * revising gate's own slice (`store/queries.sql:739-741`), which invariant V1
 * keeps disjoint from every other gate's (`workflows/validate.go:328-343`). So
 * an approved gate does not run again and a WAITING gate never looks back on an
 * approval. The point of preferring this order is that none of that chain is
 * visible from `web/` — this file can check the decision it found instead of
 * trusting five backend files to stay as they are.
 *
 * NO "STRICTLY EARLIER" COMPARISON, deliberately, and step 2 is what makes it
 * unnecessary. Evidence is built only for a phase whose status is
 * `waiting_decision` (`runs/tasks.go:717-719`), the caller renders only under
 * evidence, and a phase never carries both a decision and evidence
 * (`runs/api/dto.go:187-198` states it in both directions). So the attempt on
 * screen has no decision, "carries a decision" already excludes it, and every
 * candidate here is necessarily an earlier one. A guard for it could not be
 * killed by any legal fixture.
 *
 * NARROWED TRUTHY, never `!== undefined`. `decision` is a nullable `$ref`,
 * which this toolchain renders optional-and-non-nullable
 * (`api/schema.d.ts:882`), so the runtime value really can be `null` where the
 * type says it cannot — and `exactOptionalPropertyTypes` (`tsconfig.json:14`)
 * makes `!== undefined` narrow to the object while the value is null. The two
 * hosts already read `evidence` this way for the same reason.
 *
 * COMPARED `=== 'request_changes'` AND NOT `!== 'approve'`: the wire type is
 * `decision: string` and not a union (`api/schema.d.ts:823`), so a third value
 * is a shape this function must answer for rather than one it may assume away.
 */
export function priorRejectedArtifactId(phases: readonly Phase[], nodeId: string): string | null {
  // ORDERED BY (generation, attempt) AND NOT BY POSITION, through the spine's
  // own helper (`runs/phase-spine.ts:25-28`) rather than a second copy of the
  // rule. Trusting the array's order would make the answer depend on a SQL
  // `ORDER BY` no client file can see — the reason that helper compares at all.
  let newest: Phase | null = null
  for (const phase of phases) {
    if (phase.node_id !== nodeId || !phase.decision) continue
    if (newest === null || isNewerAttempt(phase, newest)) newest = phase
  }
  const decision = newest?.decision
  if (!decision || decision.decision !== 'request_changes') return null
  return decision.examined_artifact_id
}
