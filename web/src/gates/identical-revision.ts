/**
 * The artifact metadata `GET /runs/{id}` already carries.
 *
 * `ListArtifactsForRun` (`store/queries.sql:288-293`) selects every artifact of
 * the run with NO generation filter — contrast the query above it, which pins
 * the run's current generation — so a request-changes revision arrives on the
 * same screen as the round it revised, and finding its predecessor costs no
 * request.
 */
export type ArtifactMeta = {
  id: string
  node_id: string
  type: string
  generation: number
  attempt: number
}

/**
 * The patch this node last wrote in an EARLIER round, by id, or null.
 *
 * PATCH GATES ONLY, and the guard is the point rather than a defensive habit. A
 * gate may legally examine a PLAN — `workflows/validate.go:209` accepts
 * `plan→gate` — and a plan payload has no `diff` field at all, so a caller that
 * got an answer here for one would go on to compare `undefined` with
 * `undefined`, find them equal, and tell an operator that the revision they
 * asked for changed nothing. Byte-equality of plan CONTENT is a different
 * feature with a different payload shape, and it is not this one.
 *
 * The type compared is the ROW's, not `evidence.examined_artifact_type`: the
 * latter is the type the workflow edge declared, and it is the payload actually
 * stored that either has a `diff` or does not.
 *
 * STRICTLY EARLIER, not `generation - 1`, and the difference is a banner that
 * would otherwise go missing. A revision rewinds one gate's slice
 * (`runs/revisions.go:47`), so a node OUTSIDE that slice writes nothing for that
 * round and its patches skip a generation entirely; `generation - 1` then finds
 * no row and says nothing, on exactly the revision it exists to describe.
 *
 * NO `generation <= 1` EARLY EXIT, deliberately. Every artifact table declares
 * `generation integer NOT NULL DEFAULT 1 CHECK (generation >= 1)`
 * (`platform/pg/schema.sql:224,260,286,350,396`), so a first-round gate's search
 * for anything below its own generation is empty on its own and the extra line
 * could never change an answer — it was measured surviving its own deletion
 * before this was written. "Returns null on generation 1" in the suite is a test
 * of the comparison below, which is the live guard.
 */
export function previousExaminedArtifactId(
  artifacts: readonly ArtifactMeta[],
  examinedId: string,
): string | null {
  const examined = artifacts.find((a) => a.id === examinedId)
  if (examined === undefined || examined.type !== 'patch') return null
  let best: ArtifactMeta | null = null
  for (const a of artifacts) {
    if (a.node_id !== examined.node_id || a.type !== examined.type) continue
    if (a.generation >= examined.generation) continue
    if (best === null || outranks(a, best)) best = a
  }
  return best?.id ?? null
}

/**
 * GENERATION FIRST, THEN ATTEMPT — the order `NewestArtifact` already uses
 * (`store/queries.sql:275`), and not a style match: attempt alone gets this
 * wrong on the one row a revision is guaranteed to write.
 *
 * Opening a revision copies the examined artifact under the target node at the
 * NEW generation with `attempt = 0` (`runs/revisions.go:19,286-297` — zero is
 * unreachable for a produced artifact, so the ordinal itself says the control
 * plane wrote it). A round whose target was seeded and then produced nothing
 * therefore leaves a patch with a LOWER attempt than the round before it, and an
 * attempt-only comparison would reach past it to older work.
 *
 * `created_at` is not a tiebreak here even though the wire carries it, and the
 * query's own comment says why: a seed is stamped from the database clock and a
 * produced artifact from the application clock, so a host running slightly
 * behind sorts a later round's patch first. A tie on both integers goes to the
 * later row in the list instead — it arrives `ORDER BY created_at, id`
 * (`store/queries.sql:293`), which makes that a decision rather than an accident
 * of iteration order.
 */
const outranks = (a: ArtifactMeta, b: ArtifactMeta): boolean =>
  a.generation !== b.generation ? a.generation > b.generation : a.attempt >= b.attempt
