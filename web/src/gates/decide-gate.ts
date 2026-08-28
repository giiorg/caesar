import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'

export type Decision = 'approve' | 'request_changes' | 'reject'

/** The gate's half of an approval row or of a gate phase. */
export type GateEvidence = components['schemas']['ApiGateEvidenceResponse']

/** What a decided gate looks like once it is projected onto its phase. */
export type GateDecisionSummary = components['schemas']['ApiGateDecisionSummary']

type DecisionBody = components['schemas']['ContractsGateDecisionParams']

export type DecisionInput = { nodeId: string; decision: Decision; note?: string }

/**
 * One gate decision.
 *
 * WHAT IT INVALIDATES IS THE WHOLE OF ITS CACHE CONTRACT: the same five keys,
 * spelled in the same order, that the stream's bridge sends out on its
 * LIFECYCLE tier (`fire()` in `realtime/cache-bridge.ts`) — and then one more
 * that the bridge deliberately does NOT carry. The five: the inbox
 * (this gate left it), this run, the run list (the run's status changed), the
 * TASK list — `TaskResponse.Status` IS the latest run's status, so a decision
 * moves the task between the Tasks screen's four attention groups — and this
 * run's EVENT LOG. Writing the new state into the cache instead would be the
 * client reconstructing an orchestrator transition it did not perform — approve
 * resumes the run, request_changes opens a generation and rewinds a slice, and
 * neither is derivable from the response.
 *
 * THE LOG IS NAMED, AND THE RUN KEY IS EXACT, since Task 12. `['run', id,
 * 'events']` used to arrive for free by prefix under `['run', id]`; the bridge's
 * tier split made that invalidation exact, so a key that wants refreshing now
 * has to say so. A gate decision is exactly the event that rewrites the log it
 * is displayed beside, and a decision that left it stale would be the client
 * showing a log that stops one row before the thing the user just did.
 *
 * The frames arriving from the stream a moment later are then idempotent with
 * this, which is why both paths invalidate rather than one of them patching.
 */
export function useDecideGate(runId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (v: DecisionInput) => {
      // The note is ASSIGNED, never passed as `note: v.note`, and the reason
      // is the COMPILER, not the wire. `web/tsconfig.json:14` sets
      // exactOptionalPropertyTypes, under which `note?: string` refuses an
      // explicit `undefined` — MEASURED: `{ decision: v.decision, note: v.note }`
      // is a tsc error in this file, which is what forced this shape.
      //
      // On the wire the two are indistinguishable and no test separates them:
      // JSON.stringify drops an undefined value, so an approve posts
      // `{"decision":"approve"}` either way (MEASURED as a surviving mutant).
      // What the suite DOES pin is that the note is omitted rather than
      // DEFAULTED — `note: v.note ?? ''` puts `"note":""` on the wire and fails
      // "approve posts the decision and nothing else".
      const body: DecisionBody = { decision: v.decision }
      if (v.note !== undefined) body.note = v.note
      return unwrap(
        await api.POST('/api/v1/runs/{id}/gates/{node}/decision', {
          params: { path: { id: runId, node: v.nodeId } },
          body,
        }),
      )
    },
    onSettled: () => {
      // ON SETTLED, not onSuccess. A 409 means somebody else's decision won,
      // and the screen showing the stale "waiting" state is exactly the case
      // that needs the refetch most — it is also what lets the conflict surface
      // name the decision that beat this caller to it.
      void qc.invalidateQueries({ queryKey: ['approvals'] })
      void qc.invalidateQueries({ queryKey: ['run', runId], exact: true })
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      void qc.invalidateQueries({ queryKey: ['run', runId, 'events'] })
      // THE SIXTH KEY IS THE ONE THE BRIDGE DOES NOT CARRY, and it is last so
      // that the five above stay readable as the bridge's set in the bridge's
      // order. `['decisions']` is the Approvals History tab
      // (`decisions/decisions-query.ts`, where the twin half of this note
      // lives): the row this mutation is about to create belongs at the top of
      // it, and this client is the one place that knows a decision just
      // happened rather than merely that a run changed. The stream is
      // deliberately not extended to it — history moves only when a human
      // decides, and somebody else's decision arrives on the refresh floor.
      void qc.invalidateQueries({ queryKey: ['decisions'] })
    },
  })
}
