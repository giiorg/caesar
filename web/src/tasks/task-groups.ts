import type { Task } from './tasks-query'

/**
 * The seven run statuses, mirroring the CHECK constraint on `runs.status`
 * (backend/internal/platform/pg/schema.sql:216). Named here so the mapping
 * below can be asserted EXHAUSTIVE rather than merely correct on the cases
 * someone thought of.
 *
 * NOT the eight-status set at schema.sql:254 — that one is `phase_runs`, and a
 * phase's states are not a run's.
 */
export const ALL_RUN_STATUSES = [
  'pending',
  'running',
  'waiting',
  'succeeded',
  'failed',
  'superseded',
  'dead',
] as const

export type TaskGroup = 'needs-you' | 'in-flight' | 'up-next' | 'recently-done'

/** The four locked groups, in the order the screen renders them. */
export const TASK_GROUPS: readonly TaskGroup[] = [
  'needs-you',
  'in-flight',
  'up-next',
  'recently-done',
]

/**
 * Seven run statuses onto four locked groups.
 *
 * `TaskResponse.Status` IS the latest run's status (the DTO says so at
 * runs/api/dto.go:22-23) — `tasks` has no status column at all, and a task is
 * born already running because createTask makes a run. So the groups are
 * projections of run state, and the mockup's `draft → ready → in review`
 * lifecycle has nothing behind it (§10.8).
 *
 * TWO ENTRIES ARE NOT OBVIOUS AND BOTH ARE DELIBERATE:
 *
 *   `pending` is UP NEXT — a run intake created that the orchestrator has not
 *   started. On a single-operator system that lasts seconds, so this group is
 *   usually empty and its empty state is what is on screen most of the time.
 *
 *   `superseded` is RECENTLY DONE — a run replaced by a re-run. The tempting
 *   alternative is to skip it, and skipping takes the whole TASK off the list,
 *   because a task's status is its newest run's and a re-run supersedes the
 *   previous one at the moment the new one is created.
 *
 * The default arm is not defensive padding: a status added server-side must
 * degrade to VISIBLE, because a task that vanishes leaves nothing on screen to
 * be wrong about. Deleting it is the mutant that reddens "places every one of
 * the seven run statuses" on four of the seven.
 */
export function groupTasks(tasks: Task[]): Record<TaskGroup, Task[]> {
  const out: Record<TaskGroup, Task[]> = {
    'needs-you': [],
    'in-flight': [],
    'up-next': [],
    'recently-done': [],
  }
  for (const task of tasks) {
    switch (task.status) {
      case 'waiting':
        out['needs-you'].push(task)
        break
      case 'running':
        out['in-flight'].push(task)
        break
      case 'pending':
        out['up-next'].push(task)
        break
      default:
        out['recently-done'].push(task)
    }
  }
  return out
}
