import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { ApiError } from '../api/errors'
import { ApiErrorPanel } from '../errors/api-error-panel'
import { FormError } from '../forms/form-error'
import { TextField } from '../forms/text-field'
import { useAppForm } from '../forms/use-app-form'
import { type Run, runQueryOptions } from '../runs/run-query'
import {
  type Decision,
  type GateDecisionSummary,
  type GateEvidence,
  useDecideGate,
} from './decide-gate'

/**
 * Every refusal `cannot_decide_reason` can carry, and each says WHICH refusal
 * it is. Two refusals sharing one message is a message that tells nobody what
 * to do: the first is fixed by a grant, the second by a policy on a run already
 * created, and the third by nothing the reader can do at all.
 *
 * The three keys are exactly what `decidability()` returns
 * (`backend/internal/runs/api/api.go:516-527`), in that function's own order,
 * which is decideGate's order — so the reason names the refusal the caller
 * would actually receive rather than some other true thing about the gate.
 */
const REASONS: Record<string, string> = {
  not_approver:
    'You can see this gate but not decide it — deciding needs the approve grant on this project.',
  self_approval_forbidden:
    'You requested this run, and its policy does not let you decide your own gate.',
  evidence_missing:
    'This gate examines an artifact the run never produced, so nobody can decide it — the run has to be started again.',
}

/**
 * THE FALLBACK IS NOT DEAD CODE. `cannot_decide_reason` is a bare string on the
 * wire and not an OpenAPI enum (`schema.d.ts`: `cannot_decide_reason: string`),
 * so a reason added server-side reaches this client as a value it has never
 * heard of. An exhaustive match would render nothing at all for it — a
 * suppressed control with no explanation, which is the one outcome §5 exists to
 * prevent. Degrading to something true is the requirement; naming one of the
 * three specific refusals instead would send the reader after a fix that does
 * not apply.
 */
const GENERIC_REFUSAL = 'You cannot decide this gate.'

/**
 * Past tense, for the sentence naming whoever decided a gate first. Keyed by
 * `string` and not by `Decision` for the same reason REASONS is: the wire type
 * is `decision: string`, so a decision type added server-side has to degrade to
 * the raw token rather than render `undefined`.
 */
const DECIDED: Record<string, string> = {
  approve: 'approved',
  request_changes: 'requested changes on',
  reject: 'rejected',
}

/**
 * 8192 is `maxGateNoteLen` (`backend/internal/runs/gates.go:40`, `8 << 10`), so
 * the client refuses the same note the server would.
 *
 * THE TWO COUNTS ARE NOT THE SAME COUNT, and the difference is why the server's
 * field error still has to reach this field. `maxGateNoteLen` bounds BYTES
 * (`len(in.Note)`); `z.string().max` bounds JavaScript string length, which is
 * UTF-16 code units. A note of 5000 emoji passes here and is refused there.
 * That refusal arrives as problem+json naming `note`, and `useAppForm` routes
 * it onto this field through `problem-to-fields.ts` — which is the whole reason
 * the submit is allowed to throw rather than being swallowed here.
 */
const noteSchema = z.object({
  note: z
    .string()
    .min(1, "A note is required — it is the revision's only new input")
    .max(8192, 'At most 8192 bytes'),
})

/** Which of the two second steps is open. Neither is a decision yet. */
type Mode = 'idle' | 'note' | 'confirm-reject'

/**
 * Approve · request changes · reject, and the rule that they are ALL THREE or
 * NONE.
 *
 * `backend/internal/runs/gates.go:190` returns ClassForbidden on the requester
 * check BEFORE it branches on decision type at `:204`, so request_changes and
 * reject are refused identically to approve. Suppressing only approve is the
 * intuitive design and it ships two controls guaranteed to 403.
 *
 * SUPPRESSED, NOT DISABLED. A greyed-out button says "this is yours to do,
 * later"; these are refusals with a reason, and the reason is what a person can
 * act on.
 *
 * The evidence still renders around this component wherever it is hosted —
 * reading is a view grant and is unaffected. The host also owns the
 * positioning: this supplies the controls, not the thumb-zone placement.
 */
export function GateControls(props: {
  runId: string
  nodeId: string
  evidence: GateEvidence
  /**
   * Fired on a decision the SERVER ACCEPTED, and on nothing else.
   *
   * A 409 also means this gate is gone, but it arrives on the error path and
   * does not call this — so a host that closes a drawer here leaves it open on
   * a conflict, deliberately: the panel naming whoever decided first renders
   * inside these controls and closing the surface would take it with it.
   */
  onDecided?: () => void
}) {
  const [mode, setMode] = useState<Mode>('idle')
  const decide = useDecideGate(props.runId)

  const settled = () => {
    setMode('idle')
    props.onDecided?.()
  }

  const decideNow = (decision: Decision) => {
    decide.mutate(
      { nodeId: props.nodeId, decision },
      {
        onSuccess: settled,
        // A failure closes the confirmation step because the surface below
        // replaces it: keeping "Reject this run" on screen under an error
        // invites a second click at a gate that has just refused one.
        onError: () => setMode('idle'),
      },
    )
  }

  const form = useAppForm({
    schema: noteSchema,
    defaultValues: { note: '' },
    onSubmit: async (values) => {
      try {
        await decide.mutateAsync({
          nodeId: props.nodeId,
          decision: 'request_changes',
          note: values.note,
        })
      } catch (e) {
        // A 409 closes the form: the gate is no longer waiting, so there is
        // nothing left on this form to correct and the conflict surface below
        // is the answer. Everything else is rethrown for `useAppForm` to place
        // — a 400 naming `note` belongs ON the note field, not in a banner.
        if (e instanceof ApiError && e.status === 409) {
          setMode('idle')
          return
        }
        throw e
      }
      settled()
    },
  })

  // FIRST, and before can_decide, because the two can BOTH be true and only
  // one of them is terminal: a grant can be raised and a policy changed, but a
  // run past its wall deadline is finished for everybody. `gates.go:181`
  // refuses on the deadline before the policy check at `:190` for the same
  // reason, and gate-controls.test.tsx pins the order with the fixture in which
  // both apply — the only fixture that can see it, since with either condition
  // alone the two checks are interchangeable.
  //
  // That this check exists at all is separate and load-bearing: `can_decide`
  // deliberately ignores the wall clock (dto.go:309-315), so a gate can arrive
  // decidable and expired, and nothing but this line would say so.
  if (new Date(props.evidence.wall_deadline) <= new Date()) {
    return (
      <p className="text-text-muted text-sm">
        This run's wall deadline has passed, so its gates can no longer be decided.
      </p>
    )
  }

  if (!props.evidence.can_decide) {
    return (
      <p className="text-text-muted text-sm">
        {REASONS[props.evidence.cannot_decide_reason] ?? GENERIC_REFUSAL}
      </p>
    )
  }

  // `decide.error` is NOT always an ApiError: `unwrap` throws one, but
  // openapi-fetch propagates fetch's own TypeError from a request that never
  // reached the server, and `ApiErrorPanel` deliberately rethrows anything that
  // is not ours. Narrowing here is what keeps a dropped connection from
  // becoming a render-time throw in whichever route is hosting these controls.
  const failure = decide.error instanceof ApiError ? decide.error : null
  const conflict = failure !== null && failure.status === 409 ? failure : null
  const busy = decide.isPending

  return (
    <div className="flex flex-col gap-3">
      {mode === 'idle' && (
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            disabled={busy}
            onClick={() => decideNow('approve')}
            className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
          >
            Approve
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => setMode('note')}
            className="border-border text-text rounded-md border px-4 py-2 font-medium"
          >
            Request changes
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => setMode('confirm-reject')}
            className="border-danger text-danger rounded-md border px-4 py-2 font-medium"
          >
            Reject
          </button>
        </div>
      )}

      {mode === 'note' && (
        <FormProvider {...form}>
          {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
          <form {...form.formProps} className="flex flex-col gap-3">
            <FormError message={form.formError} correlationId={form.correlationId} />
            <TextField name="note" label="Note" />
            <div className="flex gap-2">
              <button
                type="submit"
                disabled={form.formState.isSubmitting}
                className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
              >
                Send
              </button>
              <button
                type="button"
                onClick={() => setMode('idle')}
                className="border-border text-text rounded-md border px-4 py-2"
              >
                Cancel
              </button>
            </div>
          </form>
        </FormProvider>
      )}

      {mode === 'confirm-reject' && (
        <div className="border-danger flex flex-col gap-3 rounded-md border p-3">
          {/* Reject is terminal — it finishes the run failed — and it sits in
              the thumb zone beside two controls that are not. The second step
              is explicitly labelled rather than a bare "Confirm" so that the
              button a mis-tap lands on still says what it does. */}
          <p className="text-text text-sm">
            Rejecting ends this run. It cannot be undone, and the work is not kept.
          </p>
          <div className="flex gap-2">
            <button
              type="button"
              disabled={busy}
              onClick={() => decideNow('reject')}
              className="bg-danger text-accent-contrast rounded-md px-4 py-2 font-medium"
            >
              Reject this run
            </button>
            <button
              type="button"
              onClick={() => setMode('idle')}
              className="border-border text-text rounded-md border px-4 py-2"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {conflict !== null && (
        <ConflictPanel error={conflict} runId={props.runId} nodeId={props.nodeId} />
      )}

      {/* While the note form is open its own FormError carries the failure,
          per-field where the server named a field. Rendering a panel as well
          would say the same thing twice, once in the wrong place. */}
      {conflict === null && mode !== 'note' && decide.error !== null && (
        <ErrorSurface failure={failure} />
      )}
    </div>
  )
}

/**
 * Whatever went wrong that was not a 409.
 *
 * The `null` arm is a request that never completed — a dropped connection, a
 * backend restarting mid-click — where there is no status, no problem document
 * and therefore no correlation id to render. The wording is `useAppForm`'s,
 * deliberately: it names no cause, because a transport failure, an unparseable
 * 200 and a bug in the mutation all arrive here and only the first is a
 * connection problem.
 */
function ErrorSurface(props: { failure: ApiError | null }) {
  if (props.failure !== null) return <ApiErrorPanel error={props.failure} />
  return (
    <p role="alert" className="text-danger text-sm">
      the request could not be completed — check your connection and try again
    </p>
  )
}

/**
 * Who won, on a 409.
 *
 * The decision comes from a REFETCH of the run, not from the problem document:
 * the 409 body carries a sentence and a correlation id and no decision at all,
 * while `GateDecisionSummary` on the gate's phase carries the decision, the
 * approver's address, the note and the time. The mutation has just invalidated
 * `['run', runId]` on settle, so this query mounts onto a key that is already
 * known stale and really does go back to the server.
 *
 * It is a refetch and not a reconstruction: nothing here writes the outcome
 * into the cache.
 */
function ConflictPanel(props: { error: ApiError; runId: string; nodeId: string }) {
  const run = useQuery(runQueryOptions(props.runId))
  const summary = latestDecision(run.data, props.nodeId)

  if (summary === null) {
    // A 409 does not always mean somebody decided it: `gates.go:167` answers
    // the same way for a node that is not a gate of this run and for an attempt
    // already superseded or swept. With no decision to name, the honest surface
    // is the server's own sentence plus the correlation id — never an empty
    // block shaped like a summary.
    return run.isPending ? (
      <p className="text-text-muted text-sm">
        This gate is no longer waiting — reading back who decided it…
      </p>
    ) : (
      <ApiErrorPanel error={props.error} />
    )
  }

  return (
    <section role="alert" className="border-border bg-surface text-text rounded-md border p-3">
      {/* No nested element inside the sentence. testing-library matches on a
          node's DIRECT text children, so the enclosing <section> — whose
          textContent also contains "already" — is not a second match, and the
          409 test's `findByText(/already/i)` resolves to exactly this <p>
          rather than throwing on two. A <b> around the verb would split the
          sentence and break the same query the other way. */}
      <p>
        Already decided: {summary.acted_by_email} {DECIDED[summary.decision] ?? summary.decision}{' '}
        this gate.
      </p>
      {summary.note !== '' && <p className="text-text-muted text-sm">{summary.note}</p>}
      <p className="text-text-muted text-sm">
        Reference: <code>{props.error.correlationId}</code>
      </p>
    </section>
  )
}

/**
 * The newest decided attempt of one gate node.
 *
 * NEWEST BY (generation, attempt) RATHER THAN BY POSITION: request_changes
 * bumps the run's generation and supersedes every live row in the slice below
 * the gate (`revisions.go:22-40`, steps 6 and 8), so the gate comes back under
 * the same node id at a new generation and one run can carry several decided
 * phases for it. Trusting the array's order would make the answer depend on a
 * SQL `ORDER BY` this file cannot see. Nothing in the suite pins the
 * tie-break: today's fixtures carry one decided phase each.
 */
function latestDecision(run: Run | undefined, nodeId: string): GateDecisionSummary | null {
  let best: GateDecisionSummary | null = null
  let bestGeneration = -1
  let bestAttempt = -1
  for (const phase of run?.phases ?? []) {
    // Truthy, not `!== undefined`: `decision` is a nullable $ref, which this
    // toolchain renders as optional-and-non-nullable, so the runtime value can
    // be `null` where the type says it cannot.
    if (phase.node_id !== nodeId || !phase.decision) continue
    const newer =
      phase.generation > bestGeneration ||
      (phase.generation === bestGeneration && phase.attempt > bestAttempt)
    if (!newer) continue
    best = phase.decision
    bestGeneration = phase.generation
    bestAttempt = phase.attempt
  }
  return best
}
