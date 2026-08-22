import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { Drawer } from 'vaul'
import { useStreamStatus } from '../../realtime/stream-status'
import { PhaseDots } from '../../runs/phase-dots'
import { type RunSummary, runsQueryOptions } from '../../runs/runs-query'
import { NewTaskForm } from '../../tasks/new-task-form'
import { groupTasks, TASK_GROUPS, type TaskGroup } from '../../tasks/task-groups'
import { type Task, tasksQueryOptions } from '../../tasks/tasks-query'

/**
 * This route HAD no loader, deliberately, and `_app.test.tsx` navigated here
 * because of it: a route that issues no request is the case a redirect-on-401
 * design cannot catch. Plan 12a gives it one, and the guard test is rehomed
 * onto this route in the next commit rather than being allowed to quietly
 * assert less. See `_app.test.tsx`'s first case.
 */
export const Route = createFileRoute('/_app/tasks')({
  loader: ({ context }) =>
    // BOTH, IN PARALLEL, AND BOTH MAY REJECT — unlike Home, which suspends on
    // nothing. `/` is where `/login`'s guard sends every authenticated visitor,
    // so a screen that threw from there could loop between the two forever;
    // `/tasks` is not that resting place, and a 401 here reaching `_app`'s
    // boundary ends at `/` after one round trip. `/runs` and `/approvals`
    // already take this shape.
    Promise.all([
      context.queryClient.ensureQueryData(tasksQueryOptions),
      context.queryClient.ensureQueryData(runsQueryOptions()),
    ]),
  component: Tasks,
})

const GROUP_LABEL: Record<TaskGroup, string> = {
  'needs-you': 'Needs you',
  'in-flight': 'In flight',
  'up-next': 'Up next',
  'recently-done': 'Recently done',
}

/**
 * Four empty states, one per group, and they are four different sentences
 * because they are four different facts. "Up next" is the one that matters:
 * `pending` is a run intake created that the orchestrator has not started, which
 * on a single-operator system lasts seconds — so this group is empty almost
 * always and this sentence is the default appearance of a quarter of the
 * screen. It says what the group is FOR rather than that it is empty, because
 * "nothing here" on a section nobody can explain is a section that reads as
 * broken.
 */
const GROUP_EMPTY: Record<TaskGroup, string> = {
  'needs-you': 'Nothing is waiting on a decision from you.',
  'in-flight': 'Nothing is in flight.',
  'up-next': 'Nothing queued — a run sits here between intake and its first phase.',
  'recently-done': 'Nothing has finished yet.',
}

function Tasks() {
  const { data: tasks } = useSuspenseQuery(tasksQueryOptions)
  const { data: runs } = useSuspenseQuery(runsQueryOptions())
  // Read from context, never by calling `useAttentionStream()` again: that hook
  // opens a connection and a second caller would open a second stream per tab.
  const { scopeEmpty } = useStreamStatus()

  const grouped = groupTasks(tasks)
  /**
   * The runs page, by id. `GET /tasks` is unpaginated and `GET /runs` is ONE
   * page, so a task's latest run is present or it is not, and a Map says which
   * in one pass instead of a `find` per row.
   */
  const byRun = new Map(runs.map((run) => [run.id, run]))

  return (
    <div className="flex flex-col gap-6">
      {tasks.length === 0 && scopeEmpty ? (
        /* THE PAIR EVERY LIST IN 12a OWES, and the argument is `InboxEmpty`'s
           exactly. "Nothing is in flight" told to somebody with no grants is
           false in the way that matters: it describes the system as idle when
           what is really true is that they cannot see any of it. So the four
           group sentences are replaced by the one about access, rather than
           being stacked on top of it. */
        <p className="text-text-muted text-sm">No projects are shared with you yet.</p>
      ) : (
        TASK_GROUPS.map((group) => (
          <TaskSection key={group} group={group} tasks={grouped[group]} byRun={byRun} />
        ))
      )}
      <NewTaskFab />
    </div>
  )
}

function TaskSection(props: { group: TaskGroup; tasks: Task[]; byRun: Map<string, RunSummary> }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-text text-lg font-medium">{GROUP_LABEL[props.group]}</h2>
      {props.tasks.length === 0 ? (
        <p className="text-text-muted text-sm">{GROUP_EMPTY[props.group]}</p>
      ) : (
        <ul data-testid={`group-${props.group}`} className="flex flex-col gap-2">
          {props.tasks.map((task) => (
            <li key={task.id}>
              <TaskRow task={task} run={props.byRun.get(task.latest_run_id)} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * One task as a LIST ROW, and deliberately not `RunCard`.
 *
 * `RunCard` takes a `RunSummary` and this list is keyed on the TASK — whose
 * latest run may not be on the one page of runs this screen holds. There is no
 * `RunSummary` to hand it for those rows, and a list that switched card shapes
 * halfway down would be answering a data question with a visual one.
 *
 * SO THE DOTS ARE CONDITIONAL AND NOTHING FILLS IN FOR THEM. `PhaseDots`
 * renders "No phases reported for this run" for a null or empty array, which is
 * the honest answer for a run whose SHAPE did not arrive — but a task whose run
 * is simply not on this page has not made that claim, and drawing the sentence
 * (or a row of grey dots) would invent one. The same rule governs the label
 * line: `repo_slug` and `workflow_name` live on the run summary and never on
 * the task, so a row without its run does not name them.
 *
 * The whole row is the link, matching `RunCard` and `ApprovalList`: on the
 * phone this list exists for, a target the size of a card is the difference
 * between one tap and an aimed one. `latest_run_id` is always a real id —
 * `createTask` makes a run, so a task is born with one, and `taskResponse`
 * marshals it unconditionally (runs/api/dto.go).
 */
function TaskRow(props: { task: Task; run: RunSummary | undefined }) {
  const { task, run } = props
  return (
    <Link
      to="/runs/$runId"
      params={{ runId: task.latest_run_id }}
      data-testid="task-row"
      className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3"
    >
      <p className="text-text font-medium">{task.title}</p>
      {run !== undefined && <PhaseDots phases={run.phase_summary} />}
      <p className="text-text-muted font-mono text-xs">
        {run !== undefined && `${run.repo_slug} · ${run.workflow_name} · `}
        {task.status} ·{' '}
        {/* Raw ISO for V1, in a <time> so the machine-readable value is where a
            reader (and a later formatter) expects it — the same shape `RunCard`
            and `ApprovalList` already use. */}
        <time dateTime={task.created_at}>{task.created_at}</time>
      </p>
    </Link>
  )
}

/**
 * The one thing this screen is for besides reading, so it is a button that is
 * always reachable rather than a link at the bottom of a list.
 *
 * A BOTTOM SHEET, because the locked mobile-first decision names bottom sheets
 * as the default modal, and `vaul` is the dependency this project took for
 * them. `fixed bottom-20` clears the phone tab bar; `md:bottom-6` drops it back
 * down on a viewport where the nav is a left rail and there is nothing to
 * clear. `size-14` is 56px — comfortably past the 44px minimum target, which
 * `min-h-11` alone would only just meet.
 *
 * NAVIGATION IS THE SUCCESS STATE. Creating a task creates its first run, so
 * the honest landing place is that run: a toast over an unchanged list would
 * make the operator go find what they just made.
 */
function NewTaskFab() {
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()
  return (
    <Drawer.Root open={open} onOpenChange={setOpen}>
      <Drawer.Trigger className="bg-accent text-accent-contrast fixed right-4 bottom-20 flex size-14 items-center justify-center rounded-full text-sm font-medium md:bottom-6">
        New task
      </Drawer.Trigger>
      <Drawer.Portal>
        <Drawer.Overlay className="bg-ground/70 fixed inset-0" />
        <Drawer.Content className="bg-surface border-border fixed inset-x-0 bottom-0 max-h-[90dvh] overflow-y-auto rounded-t-xl border-t p-4">
          {/* Radix requires both, and neither is decoration: the title names the
              dialog for a screen reader and the description is what
              `aria-describedby` points at when it opens. */}
          <Drawer.Title className="text-text mb-1 text-lg font-medium">New task</Drawer.Title>
          <Drawer.Description className="text-text-muted mb-4 text-sm">
            Caesar creates the task and starts its first run.
          </Drawer.Description>
          <NewTaskForm
            onCreated={(task) => {
              setOpen(false)
              void navigate({ to: '/runs/$runId', params: { runId: task.latest_run_id } })
            }}
          />
          <Drawer.Close className="text-text-muted mt-3 w-full px-4 py-2 text-sm">
            Cancel
          </Drawer.Close>
        </Drawer.Content>
      </Drawer.Portal>
    </Drawer.Root>
  )
}
