import { describe, expect, it } from 'vitest'
import { type ArtifactMeta, previousExaminedArtifactId } from './identical-revision'

const art = (
  id: string,
  generation: number,
  attempt: number,
  type = 'patch',
  node_id = 'impl',
): ArtifactMeta => ({ id, node_id, type, generation, attempt })

describe('previousExaminedArtifactId', () => {
  it('finds the newest prior-generation patch of the same node', () => {
    const arts = [art('a', 1, 1), art('b', 1, 2), art('c', 2, 3)]
    expect(previousExaminedArtifactId(arts, 'c')).toBe('b') // newest attempt of gen 1
  })

  it('returns null on generation 1 (nothing to compare)', () => {
    // NOT the proof of an early-exit line — there is none. The `generation <= 1`
    // exit the plan proposed was measured surviving its own deletion, because
    // `CHECK (generation >= 1)` on the artifacts table
    // (`platform/pg/schema.sql:286`) already makes the search below it empty.
    // What this pins is the LOOP's generation filter: drop `a.generation >=
    // examined.generation` and this case returns 'a', which is the examined
    // artifact reported as its own predecessor.
    expect(previousExaminedArtifactId([art('a', 1, 1)], 'a')).toBeNull()
  })

  it('reaches past a round in which this node wrote nothing', () => {
    // A revision at ANOTHER gate. `generation` is run-level
    // (`store/queries.sql:520-527`) and revision loops are pairwise disjoint
    // (`workflows/validate.go:328-343`), so a request-changes on a parallel
    // branch bumps the number while this node contributes no row. Under a strict
    // `generation - 1` this is where the banner silently stops appearing — on a
    // revision, which is the only time it was ever going to appear.
    const arts = [art('p1', 1, 1), art('c', 3, 2)]
    expect(previousExaminedArtifactId(arts, 'c')).toBe('p1')
  })

  it('prefers the nearer round over the higher attempt', () => {
    // `seed` is the shape `openRevision` writes: a copy of the examined
    // artifact, under the target node, at the NEW generation, at attempt 0
    // (`runs/revisions.go:19,286-297`). A round that was seeded and then
    // produced nothing leaves exactly this — a newer round holding a LOWER
    // attempt than the round before it — and an attempt-only comparison answers
    // 'p1', reaching past the revision to the work before it.
    const arts = [art('p1', 1, 1), art('seed', 2, 0), art('c', 3, 3)]
    expect(previousExaminedArtifactId(arts, 'c')).toBe('seed')
  })

  it('ignores other nodes and other types', () => {
    const arts = [art('x', 1, 1, 'plan'), art('y', 1, 1, 'patch', 'other'), art('c', 2, 2)]
    expect(previousExaminedArtifactId(arts, 'c')).toBeNull()
  })

  // A gate CAN examine a plan (`workflows/validate.go:209` accepts plan→gate).
  // Plan payloads carry no `diff`, so without this guard the surface compares
  // undefined to undefined, finds them equal, and accuses the operator's
  // revision of changing nothing.
  it('refuses to answer for a plan-examining gate', () => {
    const arts = [art('p1', 1, 1, 'plan'), art('p2', 2, 1, 'plan')]
    expect(previousExaminedArtifactId(arts, 'p2')).toBeNull()
  })

  it('returns null when the examined artifact is not in the list', () => {
    // `artifacts` is required AND nullable on the wire (`schema.d.ts:998` — a Go
    // nil slice marshals to null), and the hosts pass `?? []`, so an empty list
    // beside a real `examined_artifact_id` is a shape production reaches. There
    // is nothing to compare and nothing to say about it.
    const arts = [art('a', 1, 1), art('b', 2, 1)]
    expect(previousExaminedArtifactId(arts, 'nope')).toBeNull()
    expect(previousExaminedArtifactId([], 'b')).toBeNull()
  })

  it('breaks an attempt tie towards the later row', () => {
    // Two patches written under one attempt is not the shape a node normally
    // produces, but nothing forbids it, and "newest" has to mean something
    // rather than "whichever the loop met first".
    const arts = [art('first', 1, 1), art('second', 1, 1), art('c', 2, 1)]
    expect(previousExaminedArtifactId(arts, 'c')).toBe('second')
  })
})
