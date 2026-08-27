import type { Phase } from './phase-spine'

type PhaseOutcome = NonNullable<Phase['outcome']>

/**
 * The outcome's code, or a stand-in for a row that ended without naming one.
 *
 * TRUTHINESS, NOT `!== undefined`, and the type is not being humoured. All
 * three of `ApiOutcomeResponse`'s scalars are optional because `OutcomeResponse`
 * tags none of them `required:"true"` (`runs/api/dto.go:167-172`) — unlike
 * every scalar on the `PhaseResponse` directly below it — so the compiler will
 * not let a caller return `out.code` from a `: string` function. And they are
 * plain Go strings rather than pointers, so an outcome that never set one
 * marshals it as `""`, not as absent: `!== undefined` would narrow to a value
 * that renders as nothing at all.
 */
function codeOf(out: PhaseOutcome): string {
  return out.code || 'ended without a stated cause'
}

/**
 * `Outcome.Retryable()` is exactly `Class == transient` (`runs/outcome.go:80`),
 * so this asks the same question the engine asked when it decided whether to
 * insert a successor attempt. Every OTHER class is final — `permanent`,
 * `policy_violation`, and anything a later Go release adds. Testing for
 * `permanent` instead would quietly hand each of those back to the counter,
 * which is the bug this module exists to remove.
 */
function retryable(out: PhaseOutcome): boolean {
  return out.class === 'transient'
}

/**
 * §2.2's one rule: a phase's status line derives from WHAT HAPPENS NEXT, never
 * from a bare counter. `attempt N/M` and the word "resumable" are both promises
 * about a further attempt, and a row that has ended has none to promise.
 *
 * STATUS FIRST, OUTCOME CLASS SECOND. The engine facts that forces:
 *
 *  - An exhausted node keeps its TRANSIENT phase outcome. The terminal writer
 *    passes the raw one through — `finishPhase(…, PhaseDead, workerID, rawOut)`
 *    (`orchestrator.go:1627`) — while `attempts_exhausted` is written to a
 *    separate `runOut` for the RUN (`orchestrator.go:1662-1666`). So class
 *    alone cannot tell exhausted from mid-retry, and a class-first formatter
 *    printed "attempt 3/3" on the commonest failure there is.
 *  - `dead` is the status that means nothing follows this row, and it is the
 *    only one that means it reliably. "Failed rows always have successors" is
 *    the RETRY BRANCH's invariant (`orchestrator.go:1584-1586`), scoped to that
 *    transaction — it is NOT a property of the status. A rejected gate is
 *    finished `PhaseFailed` with a permanent `gate_rejected` outcome and no
 *    successor at all (`gates.go:241-247`), which is why a live row's outcome
 *    is still consulted below before any counter is reached.
 *  - `attempt` outruns `max_attempts` only via UNCHARGED waits: the retry
 *    budget is spent by chargeable failures (`retry := out.Retryable() &&
 *    newCount < c.MaxAttempts`, `orchestrator.go:1532`) while the ordinal
 *    counts every attempt. On a row that can still advance, `attempt >
 *    max_attempts` therefore means "waiting, and this wait costs nothing".
 *  - `auth_expired` and every other terminal code lives on the PHASE outcome
 *    (the run merely goes `failed`), so this reads the phase's own outcome and
 *    never the run's.
 *
 * `priorAttempt` is this node's attempt N-1 (`priorAttemptOf` in
 * `phase-spine.ts`), read only for the wait reason: the row that is waiting has
 * no outcome yet, so the code that parked it is on the one before.
 */
export function phaseStatusLine(phase: Phase, priorAttempt: Phase | null): string {
  const out = phase.outcome
  // EXHAUSTIVE over all eight statuses (`runs/status.go:52-66`), and written as
  // a switch with no `default` so that the four advancing ones have to be named
  // rather than fallen into. A row that reaches the counter reaches it because
  // this file says it may.
  switch (phase.status) {
    case 'succeeded':
      // No outcome at all on this path — the success writer passes nil
      // (`orchestrator.go:1109`) — so there is nothing here to report but the
      // fact itself.
      return 'succeeded'
    case 'waiting_decision':
      // The only status whose next event is a PERSON: rows are INSERTED
      // waiting and the claim CTE predicates on `pending`, so a gate has no
      // worker, no lease and no retry clock (`runs/status.go:55-60`). A
      // counter here would count against a budget nothing is spending.
      return 'awaiting a decision'
    case 'superseded':
      // A revision round threw this row away; its attempt ordinal survives
      // into the rematerialized node (`store/queries.sql:719-723`), which is
      // precisely why the counter must not be shown as if it were still live.
      return 'superseded by a newer generation'
    case 'dead':
      // Over. Report the cause and never a counter: there is no next attempt
      // to count toward. `attempts_exhausted` is a RUN outcome and
      // deliberately not reachable here — the phase carries the outcome that
      // actually ended it, and on an exhausted node that outcome still reads
      // `transient`, hence the disclaimer.
      if (!out) return 'ended without a stated cause'
      return retryable(out) ? `${codeOf(out)} — no attempts remain` : codeOf(out)
    case 'pending':
    case 'running':
    case 'reconciling':
    case 'failed':
      break // a row that MAY still advance — but see the outcome check below
  }
  if (out && !retryable(out)) {
    // A live status with a final outcome: the rejected gate above, and
    // whatever else the engine later ends without a `dead`. The code IS the
    // status, and the counter would be a promise nothing intends to keep.
    return codeOf(out)
  }
  if (phase.attempt > phase.max_attempts) {
    const reason = priorAttempt?.outcome?.code
    return reason ? `resumable — not chargeable (${reason})` : 'resumable — not chargeable'
  }
  return `attempt ${phase.attempt}/${phase.max_attempts}`
}
