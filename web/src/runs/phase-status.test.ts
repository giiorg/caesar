import { describe, expect, it } from 'vitest'
import type { Phase } from './phase-spine'
import { phaseStatusLine } from './phase-status'

const AT = '2026-08-20T00:00:00Z'

/**
 * `created_at`/`finished_at` are REQUIRED on `ApiPhaseResponse`
 * (`schema.d.ts:877-893`) and neither reaches the derivation — they are here
 * only so the fixture is a `Phase` rather than a shape the compiler has to be
 * talked out of. Same factory shape as the route suite's `phase()`.
 */
const base: Phase = {
  node_id: 'impl',
  generation: 1,
  status: 'running',
  attempt: 1,
  max_attempts: 3,
  created_at: AT,
  finished_at: null,
}

/**
 * The eight `PhaseStatus` constants (`runs/status.go:52-66`), mirrored by hand
 * because the generated `status` is a bare `string`: the Go side declares no
 * enum, so nothing in the type system relates this list to that one.
 */
const ALL_STATUSES = [
  'pending',
  'running',
  'waiting_decision',
  'reconciling',
  'succeeded',
  'failed',
  'dead',
  'superseded',
]

/** The four a counter can honestly describe: a row that may still advance. */
const CAN_ADVANCE = ['pending', 'running', 'reconciling', 'failed']

describe('phaseStatusLine', () => {
  it('renders the counter while transient retry budget remains', () => {
    expect(phaseStatusLine({ ...base, attempt: 2 }, null)).toBe('attempt 2/3')
  })

  // THE ROUND-1 BLOCKER, pinned. An exhausted node's phase row keeps its
  // TRANSIENT outcome — `finishPhase(…, PhaseDead, workerID, rawOut)`
  // (orchestrator.go:1631) writes the raw one, while only the RUN's is
  // rewritten to attempts_exhausted (orchestrator.go:1666-1670) — and its
  // status is `dead`. A formatter that reads the outcome class first prints
  // "attempt 3/3" here, which is exactly the lying counter this task exists to
  // remove.
  it('never prints a counter on a dead row, even with a transient outcome at the cap', () => {
    const p: Phase = {
      ...base,
      status: 'dead',
      attempt: 3,
      outcome: { class: 'transient', code: 'harness_error', message: 'exit 1' },
    }
    const line = phaseStatusLine(p, null)
    expect(line).not.toMatch(/attempt \d/)
    expect(line).not.toContain('resumable')
    expect(line).toContain('harness_error') // the cause survives; the promise of a retry does not
    // …and the row says so in words. Without this the `dead` arm's
    // transient/final ternary is unfalsifiable: both of its branches name the
    // code, so every other assertion here holds whichever one runs.
    expect(line).toContain('no attempts remain')
  })

  // Same row shape, ordinal PAST the cap (earlier uncharged waits pushed it
  // there — `retry := out.Retryable() && newCount < c.MaxAttempts` counts
  // CHARGEABLE failures, orchestrator.go:1532, while the ordinal counts all of
  // them). "resumable" is a promise of a successor; a dead row has none.
  it('never says resumable on a dead row whose ordinal outran the cap', () => {
    const p: Phase = {
      ...base,
      status: 'dead',
      attempt: 5,
      outcome: { class: 'transient', code: 'rate_limited', message: 'cap window' },
    }
    const line = phaseStatusLine(p, null)
    expect(line).not.toContain('resumable')
    expect(line).not.toContain('5/3')
  })

  // `failed` is the mirror case and proves the discriminator is STATUS, not
  // just "is there an outcome": same class, same code, opposite reading.
  it('a failed row with the same outcome still reads as a retry in progress', () => {
    const p: Phase = {
      ...base,
      status: 'failed',
      attempt: 2,
      outcome: { class: 'transient', code: 'harness_error', message: 'exit 1' },
    }
    expect(phaseStatusLine(p, null)).toBe('attempt 2/3')
  })

  // `toBe`, not `toContain`: the code alone is the whole line on a final
  // outcome. "auth_expired — no attempts remain" would be true and useless —
  // no attempt was ever going to fix an expired credential — and a `toContain`
  // here cannot tell the two apart.
  it('names the terminal code on a permanent outcome, never a counter', () => {
    const p: Phase = {
      ...base,
      status: 'dead',
      outcome: {
        class: 'permanent',
        code: 'auth_expired',
        message: 'credential claude-sub: token expired',
      },
    }
    // "1/3" here would promise retries that will never come.
    expect(phaseStatusLine(p, null)).toBe('auth_expired')
  })

  it('says resumable — not chargeable past the cap, with the wait reason from the prior attempt', () => {
    // The prior row is `failed`, not `dead`: the retry branch inserts the
    // successor in the same transaction as the failure
    // (orchestrator.go:1591 fails the row, :1609-1610 inserts the
    // successor). A `dead` prior would mean the node ended
    // and there is no attempt 4 to describe.
    const prior: Phase = {
      ...base,
      attempt: 3,
      status: 'failed',
      outcome: {
        class: 'transient',
        code: 'rate_limited',
        message: 'cap window',
        retry_at: '2026-08-26T12:00:00Z',
      },
    }
    const p: Phase = { ...base, attempt: 4, status: 'pending' }
    const line = phaseStatusLine(p, prior)
    expect(line).toContain('resumable')
    expect(line).toContain('not chargeable')
    expect(line).toContain('rate_limited')
    expect(line).not.toContain('4/3')
  })

  // The reason is the prior row's, so it is missing exactly when that row is:
  // `newestPhasePerNode` hands this function the node's newest attempt, and a
  // run whose earlier rows were never fetched has none to name.
  it('still says resumable when no prior attempt explains the wait', () => {
    const line = phaseStatusLine({ ...base, attempt: 4, status: 'pending' }, null)
    expect(line).toBe('resumable — not chargeable')
  })

  // A REJECTED GATE, and it is why the live-row outcome check exists.
  // "failed rows always have successors" is the RETRY BRANCH's invariant, not
  // the status's: `gates.go:241-247` finishes a rejected gate as PhaseFailed
  // with a PERMANENT outcome and inserts no successor at all — the run is
  // failed in the same transaction. Status-first alone renders "attempt 1/3"
  // under a gate a person just rejected.
  it('a rejected gate names the rejection, not a counter', () => {
    const p: Phase = {
      ...base,
      node_id: 'gate',
      status: 'failed',
      outcome: { class: 'permanent', code: 'gate_rejected', message: 'gate "gate" was rejected' },
    }
    expect(phaseStatusLine(p, null)).toBe('gate_rejected')
  })

  // The two statuses a three-branch function silently mishandles. A gate has
  // no retry clock at all (status.go:55-60) and a succeeded phase has no
  // outcome (orchestrator.go:1109) — both fall through to "attempt 1/3"
  // unless the switch is exhaustive.
  it('a gate awaiting a human says so, never a counter', () => {
    expect(phaseStatusLine({ ...base, status: 'waiting_decision', attempt: 1 }, null)).toBe(
      'awaiting a decision',
    )
  })

  it('a succeeded phase says succeeded, not a counter', () => {
    // No outcome at all on this path — the success writer passes nil
    // (orchestrator.go:1109). The key is OMITTED rather than set to
    // `undefined`: `exactOptionalPropertyTypes` (tsconfig.json:14) rejects an
    // explicit undefined on an optional property.
    expect(phaseStatusLine({ ...base, status: 'succeeded', attempt: 1 }, null)).toBe('succeeded')
  })

  it('a superseded phase says so — a revision round is not a retry', () => {
    expect(phaseStatusLine({ ...base, status: 'superseded' }, null)).toBe(
      'superseded by a newer generation',
    )
  })

  // Exhaustiveness, asserted rather than assumed — and asserted so that it can
  // FAIL. `toBeTruthy` alone cannot: a status dropped from the switch falls
  // past it to `attempt 1/3`, which is truthy, so the guard would pass on
  // exactly the regression it names. The counter rule is the falsifiable half.
  it('every status produces a line, and only an advancing row shows a counter', () => {
    for (const status of ALL_STATUSES) {
      const line = phaseStatusLine({ ...base, status }, null)
      expect(line, status).toBeTruthy()
      if (!CAN_ADVANCE.includes(status)) expect(line, status).not.toMatch(/attempt \d/)
    }
  })

  // policy_violation on a LIVE row is what makes the class test a
  // `!== transient` rather than an `=== permanent`: `Outcome.Retryable()` is
  // exactly `Class == transient` (outcome.go:80), so every other class — named
  // or not — is final, and an `=== permanent` check hands this one back to the
  // counter.
  it('policy_violation on a live row renders its code like permanent (final is final)', () => {
    const p: Phase = {
      ...base,
      status: 'failed',
      outcome: { class: 'policy_violation', code: 'secret_in_artifact', message: 'x' },
    }
    expect(phaseStatusLine(p, null)).toBe('secret_in_artifact')
  })

  it('policy_violation on a dead row renders its code too', () => {
    const p: Phase = {
      ...base,
      status: 'dead',
      outcome: { class: 'policy_violation', code: 'secret_in_artifact', message: 'x' },
    }
    expect(phaseStatusLine(p, null)).toBe('secret_in_artifact')
  })

  // `class`, `code` and `message` are all optional in the generated type and
  // are Go strings with no `required:"true"` (`runs/api/dto.go:167-172`), so
  // "" is what an unset one looks like on the wire. A codeless outcome must
  // not render an empty line, and must not fall back to the counter: an empty
  // class is not transient, so nothing follows this row either.
  it('a row whose outcome names no code still says something', () => {
    const dead = phaseStatusLine({ ...base, status: 'dead', outcome: { class: 'permanent' } }, null)
    expect(dead).toBe('ended without a stated cause')
    const live = phaseStatusLine({ ...base, status: 'failed', outcome: { class: '' } }, null)
    expect(live).toBe('ended without a stated cause')
  })
})
