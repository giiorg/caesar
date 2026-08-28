import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { api, unwrap } from '../api/client'

/**
 * Re-run a task, from the run detail of one of its runs.
 *
 * IT TAKES A TASK ID, NOT A RUN ID, and that is the whole shape of the feature
 * rather than a parameter choice. `rerunTask` mints a NEW run from the task's
 * pinned workflow version (`runs/tasks.go:356-420`); it does not resume, repair
 * or reopen the run the operator is looking at. So the run on screen stays
 * failed forever and the success has to navigate away — a refresh in place
 * would leave the operator watching a corpse while their re-run runs elsewhere.
 * The caller reads the id off `run.task_id`, which the run DTO carries
 * (`dto.go` ApiRunResponse) precisely so a run detail can reach its task.
 *
 * WHAT IT INVALIDATES:
 *
 *  - `['runs']` — the new run belongs in every list, and `runs-query.ts` keys
 *    on `['runs', filters]`, which this prefix reaches.
 *  - `['tasks']` — `TaskResponse.Status` IS the latest run's status, so the
 *    task moves out of whichever attention group `failed` put it in.
 *
 * NOT the old run's own `['run', id]`. From a `failed` run the service takes
 * its terminal fall-through and writes nothing to the run we came from — only
 * a `pending` predecessor gets superseded (`tasks.go:367-390`), and a button
 * that only appears on `failed` cannot reach that branch. If a race puts us
 * there anyway, the `run.superseded` frame is what corrects the cache, which is
 * the same path every other supersede already travels.
 *
 * The navigation does NOT wait on a stream frame. The bridge hears
 * `run.created` a moment later and invalidates the same keys idempotently, but
 * an address bar that only moves once an SSE frame lands is an address bar that
 * never moves on a dropped connection.
 */
export function useRetryRun(taskId: string) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  return useMutation({
    mutationFn: async () =>
      unwrap(await api.POST('/api/v1/tasks/{id}/runs', { params: { path: { id: taskId } } })),
    // ON SUCCESS, not onSettled — the opposite of `useDecideGate`, and for the
    // opposite reason. A failed decision means somebody else moved the run and
    // the stale screen needs refetching most; a failed re-run created nothing,
    // so there is no new state for any list to learn about and a refetch would
    // only make the conflict panel flicker under a re-render.
    onSuccess: (run) => {
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      void navigate({ to: '/runs/$runId', params: { runId: run.id } })
    },
  })
}
