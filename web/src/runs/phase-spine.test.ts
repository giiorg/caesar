import { describe, expect, it } from 'vitest'
import { type Phase, phaseForSelector } from './phase-spine'
import type { Run } from './run-query'

/** One phase row, as `GET /runs/{id}` carries it (`api/schema.d.ts:877-893`). */
const phase = (nodeId: string, generation: number, attempt: number): Phase => ({
  node_id: nodeId,
  attempt,
  max_attempts: 3,
  status: 'succeeded',
  generation,
  created_at: '2026-01-01T00:00:00Z',
  finished_at: '2026-01-01T00:00:00Z',
})

/**
 * A run carrying nothing but a phase list.
 *
 * The cast is the fixture: `phaseForSelector` reads `run.phases` and nothing
 * else, and spelling out the other seventeen required fields of `ApiRunResponse`
 * would put a wall of irrelevant literals between a reader and the one field
 * every assertion below is about.
 */
const withPhases = (phases: Phase[]): Run => ({ phases }) as unknown as Run

/**
 * THE FIXTURE SEPARATES GENERATION FROM ATTEMPT, AND THE GATE FROM ANOTHER NODE,
 * because a fixture where they move together cannot tell a three-key match from
 * a one-key one. Two rows of one node at `(1,1)` and `(2,2)` are answered
 * identically by a match on `attempt` alone, by a match on `generation` alone,
 * and by an implementation that never looks at `node_id` at all — three mutants
 * green under a test that reads as if it pins the triple.
 *
 * So: the gate is RETRIED INSIDE generation 1 (`a1` then `a2`) and then revised
 * into generation 2 at `a3` — which is what `revisions.go:187-198` does, since a
 * rematerialized node continues the attempt ordinal — and `impl` shares the
 * `(1, 1)` pair with the gate's first attempt.
 */
const run = withPhases([
  phase('impl', 1, 1),
  phase('gate', 1, 1),
  phase('gate', 1, 2),
  phase('gate', 2, 3),
])

describe('phaseForSelector', () => {
  it('answers the node’s newest attempt when the URL names no selector', () => {
    // Which is what every link that predates this selector means, and what
    // `PhaseRail` still means: "open this node's newest".
    expect(phaseForSelector(run, 'gate', undefined, undefined)?.attempt).toBe(3)
    expect(phaseForSelector(run, 'impl', undefined, undefined)?.attempt).toBe(1)
  })

  it('resolves a targeted generation/attempt, bypassing the newest-per-node collapse', () => {
    // The whole point: `?phase=gate` alone opens attempt 3, so a gate.decided
    // event for attempt 1 could only ever link to somebody else's decision.
    expect(phaseForSelector(run, 'gate', 1, 1)?.attempt).toBe(1)
    // A RETRY INSIDE ONE GENERATION. An implementation that matched on
    // generation alone would answer attempt 1 here — the first row of
    // generation 1 — and would be green on any fixture whose two counters move
    // together.
    expect(phaseForSelector(run, 'gate', 1, 2)?.attempt).toBe(2)
    expect(phaseForSelector(run, 'gate', 2, 3)?.attempt).toBe(3)
  })

  it('needs BOTH halves of the selector, and falls back to newest with either missing', () => {
    // Half a selector is not a target: `?phase=gate&generation=1` names four
    // rows on some runs and one on others. The rule is both or neither.
    expect(phaseForSelector(run, 'gate', 1, undefined)?.attempt).toBe(3)
    expect(phaseForSelector(run, 'gate', undefined, 2)?.attempt).toBe(3)
  })

  it('answers null rather than a row the selector did not name', () => {
    // `(gate, 1, 3)` names a real attempt of a real node at the WRONG
    // generation — attempt 3 is generation 2 — and it is the assertion that
    // separates the triple from a match on `attempt` alone.
    expect(phaseForSelector(run, 'gate', 1, 3)).toBeNull()
    // `(impl, 1, 2)` is the gate's retry with the wrong node on it, and it is
    // the assertion that separates the triple from a match that never filters
    // by node at all.
    expect(phaseForSelector(run, 'impl', 1, 2)).toBeNull()
    // And the ordinary stale link: a generation and attempt this run never had.
    expect(phaseForSelector(run, 'gate', 9, 9)).toBeNull()
    // A node this run does not have, with no selector — the `newestPhaseOf`
    // half of the same honesty.
    expect(phaseForSelector(run, 'nonexistent', undefined, undefined)).toBeNull()
  })

  it('survives a run whose phase list is the null a Go nil slice marshals to', () => {
    // `phases` is required AND nullable on the wire (`api/schema.d.ts:1011`),
    // and a run created but not yet compiled really sends it.
    const empty = withPhases(null as unknown as Phase[])
    expect(phaseForSelector(empty, 'gate', 1, 1)).toBeNull()
    expect(phaseForSelector(empty, 'gate', undefined, undefined)).toBeNull()
  })
})
