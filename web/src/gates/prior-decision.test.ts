import { describe, expect, it } from 'vitest'
import type { Phase } from '../runs/phase-spine'
import { priorRejectedArtifactId } from './prior-decision'

/** An attempt with no decision on it — a gate that ran and is still waiting. */
const undecided = (nodeId: string, generation: number, attempt: number): Phase => ({
  node_id: nodeId,
  attempt,
  max_attempts: 3,
  status: 'superseded',
  generation,
  created_at: '2026-01-01T00:00:00Z',
  finished_at: '2026-01-01T00:00:00Z',
})

/**
 * A decided gate attempt, as `GET /runs/{id}` carries it.
 *
 * `verb` is typed `string` on purpose, matching the wire
 * (`api/schema.d.ts:823` — `decision: string`, not a union). The three values
 * the control plane actually writes are `approve`, `request_changes` and
 * `reject` (`runs/gates.go:28-30`).
 */
const decided = (
  nodeId: string,
  generation: number,
  attempt: number,
  verb: string,
  examinedArtifactId: string,
): Phase => ({
  ...undecided(nodeId, generation, attempt),
  decision: {
    decision: verb,
    note: '',
    acted_by_id: 'u1',
    acted_by_email: 'reviewer@example.com',
    acted_at: '2026-01-01T00:00:00Z',
    self_approval: false,
    examined_artifact_id: examinedArtifactId,
  },
})

/**
 * A phase whose `decision` is the explicit `null` a Go nil pointer marshals to.
 *
 * THE CAST IS THE FIXTURE, not a convenience. `decision` is a nullable `$ref`,
 * which this toolchain renders optional-and-NON-nullable
 * (`api/schema.d.ts:882`), so the shape production really sends cannot be
 * written in the generated type at all. Building it any other way would pin a
 * value the server never emits.
 */
const nullDecision = (nodeId: string, generation: number, attempt: number): Phase =>
  ({ ...undecided(nodeId, generation, attempt), decision: null }) as unknown as Phase

describe('priorRejectedArtifactId', () => {
  it('returns the examined artifact of the NEWEST decided attempt', () => {
    // Mutant: answering the first match instead of the newest. Two
    // request-changes rounds on one gate is what `max_rounds > 1` buys, and
    // the banner must compare against the round just decided rather than the
    // first one ever decided.
    const phases = [
      decided('gate', 1, 1, 'request_changes', 'old'),
      decided('gate', 2, 2, 'request_changes', 'recent'),
      undecided('gate', 3, 3),
    ]
    expect(priorRejectedArtifactId(phases, 'gate')).toBe('recent')
  })

  it('does not trust the array order', () => {
    // The same two rounds with the NEWER one first. Mutant: `find`-first, or a
    // last-wins loop with no comparison. `ListPhasesForRun` orders by
    // `created_at, node_id, attempt` (`store/queries.sql:225-226`), and a
    // client that leaned on that would depend on a SQL clause no file in
    // `web/` can see — `runs/phase-spine.ts:9-14`'s stated reason for
    // comparing rather than indexing.
    const phases = [
      decided('gate', 2, 2, 'request_changes', 'recent'),
      decided('gate', 1, 1, 'request_changes', 'old'),
      undecided('gate', 3, 3),
    ]
    expect(priorRejectedArtifactId(phases, 'gate')).toBe('recent')
  })

  it('answers null when an APPROVAL is newer than the request-changes', () => {
    // THE CASE THAT SEPARATES THIS RULE FROM "newest request_changes".
    // Filtering to request-changes first and then taking the newest answers
    // 'old' here, and the caller's sentence — "the previous request-changes
    // note was not acted on" — would be false: it was acted on at generation
    // 2, and approved. Measured: this is the only case in the file that dies
    // under that rule.
    //
    // DEFENSIVE, AND NOT A LIVE PRODUCTION SHAPE — the honest reading, because
    // a comment claiming otherwise would invent a bug to justify a line. An
    // approve finishes the gate as `succeeded` (`runs/gates.go:254`), a node
    // whose newest row succeeded is never rematerialized
    // (`runs/orchestrator.go:1236`), and the only statement that moves a row
    // out of `succeeded` is scoped to the revising gate's own slice
    // (`store/queries.sql:739-741`), which V1 keeps disjoint from every other
    // gate's (`workflows/validate.go:328-343`). So a waiting gate cannot today
    // look back on its own approval. What this pins is that the rule does not
    // DEPEND on that five-file chain, none of which is visible from here.
    const phases = [
      decided('gate', 1, 1, 'request_changes', 'old'),
      decided('gate', 2, 2, 'approve', 'approved'),
      undecided('gate', 3, 3),
    ]
    expect(priorRejectedArtifactId(phases, 'gate')).toBeNull()
  })

  it('ignores decided attempts of OTHER nodes', () => {
    // Mutant: dropping the `node_id` filter. `run.phases` is the whole run —
    // every node, every attempt (`store/queries.sql:225-226`) — so a second
    // gate's request-changes is sitting right beside this one's, and without
    // the filter the banner would compare this gate's patch against an
    // artifact nobody examined for it.
    const phases = [
      decided('other-gate', 2, 2, 'request_changes', 'not-ours'),
      undecided('gate', 2, 1),
    ]
    expect(priorRejectedArtifactId(phases, 'gate')).toBeNull()
  })

  it('answers null for a decision that is neither request-changes nor approval', () => {
    // Mutant: `!== 'approve'` written where `=== 'request_changes'` belongs.
    // `reject` is a real verb (`runs/gates.go:30`) and not an invented one,
    // and it is not a note anybody was asked to act on — it ends the run
    // (`runs/gates.go:245-248`). Under the mutant its artifact comes back and
    // the banner accuses an operator over a gate that was never sent back.
    const phases = [decided('gate', 1, 1, 'reject', 'rejected'), undecided('gate', 2, 2)]
    expect(priorRejectedArtifactId(phases, 'gate')).toBeNull()
  })

  it('answers null when this node has no decided attempt, and on an empty list', () => {
    // A FIRST-ROUND GATE, which is the ordinary case: nobody has decided this
    // node yet, so there is no prior artifact and nothing to say. The empty
    // list is the same shape from the other side — `phases` is required and
    // nullable on the wire (`api/schema.d.ts:1011`, a Go nil slice marshals to
    // `null`) and both hosts coalesce it.
    expect(priorRejectedArtifactId([undecided('gate', 1, 1)], 'gate')).toBeNull()
    expect(priorRejectedArtifactId([], 'gate')).toBeNull()
  })

  it('skips the attempt on screen, which the wire sends with an explicit null', () => {
    // THE SHAPE THE SURFACE ACTUALLY RENDERS: this gate was sent back at
    // generation 1, came back at generation 2, and is waiting again. The
    // NEWEST attempt is the undecided one, and the answer must reach past it
    // to the round that was decided — which is also why no "strictly earlier"
    // comparison is needed anywhere: "carries a decision" already excludes the
    // attempt on screen.
    //
    // Mutant: `phase.decision !== undefined` in place of the truthy narrowing.
    // It admits this null-bearing newest row as the winner and the function
    // answers null — the banner silently stops appearing on exactly the
    // revision it exists to describe. The type says this row cannot exist
    // (`api/schema.d.ts:882` renders the nullable `$ref` non-nullable) and
    // `exactOptionalPropertyTypes` (`tsconfig.json:14`) makes `!== undefined`
    // narrow to the object while the value is null; the server sends it
    // anyway.
    const phases = [decided('gate', 1, 1, 'request_changes', 'prior'), nullDecision('gate', 2, 2)]
    expect(priorRejectedArtifactId(phases, 'gate')).toBe('prior')
  })
})
