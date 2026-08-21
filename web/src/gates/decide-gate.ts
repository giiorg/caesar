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
 * WHAT IT INVALIDATES IS THE WHOLE OF ITS CACHE CONTRACT, and it is deliberately
 * the same three keys the stream's bridge invalidates
 * (`realtime/cache-bridge.ts:84-92`): the inbox (this gate left it), the run
 * list (the run's status changed) and this run. Writing the new state into the cache instead would be the client
 * reconstructing an orchestrator transition it did not perform — approve
 * resumes the run, request_changes opens a generation and rewinds a slice, and
 * neither is derivable from the response.
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
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['run', runId] })
    },
  })
}
