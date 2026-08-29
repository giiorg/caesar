import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { Drawer } from 'vaul'
import { z } from 'zod'
import { useStreamStatus } from '../../realtime/stream-status'
import { PhaseDots } from '../../runs/phase-dots'
import { type RunSummary, runsQueryOptions } from '../../runs/runs-query'
import { NewTaskForm } from '../../tasks/new-task-form'
import { groupTasks, TASK_GROUPS, type TaskGroup } from '../../tasks/task-groups'
import { type Task, tasksQueryOptions } from '../../tasks/tasks-query'

/**
 * THE NEW-TASK DRAWER IS URL STATE, and `1` is a NUMBER here rather than a
 * string — measured on this tree, not assumed:
 *
 *     PROBE search= {"new":1} typeof new = number
 *
 * The router `JSON.parse`s each value, which `runs.$runId.tsx` records at
 * length for `generation`. `z.string()` would reject the only value this
 * feature ever writes and the drawer would never open.
 *
 * WHY THE DRAWER MOVED OUT OF `useState` AT ALL: the command palette offers
 * "New task" as one of its three entry classes, and an entry that navigates to
 * `/tasks` and stops is a command that does not perform its command. Local
 * state is unreachable from the shell. This is the plan's URL-as-state
 * constraint applied once more, and it buys two things the previous version did
 * not have — the open drawer is linkable, and it survives a reload.
 *
 * `z.literal(1)` and not `z.number()`: one value opens it. A `?new=7` is as
 * meaningless as a `?new=banana`, and both must land in the same place.
 */
const searchSchema = z.object({ new: z.literal(1).optional() })

/**
 * The read-site re-narrow, and it is load-bearing rather than defensive.
 *
 * `validateSearch` CANNOT DROP A KEY — measured twice in this tree
 * (`runs.index.tsx` carries the transcript, `runs.$runId.tsx` repeats it): a
 * route's search is its ancestors' search with its own validated result merged
 * OVER it, and nothing between `/` and here declares one. So a rejected value
 * still arrives, and `search.new` really can hold `2`, `true` or a string by
 * the time it reaches this component. `=== 1` is what makes every one of those
 * land where `?new=banana` lands — closed — instead of opening a drawer
 * because something was present.
 */
const drawerOpen = (value: unknown): boolean => value === 1

/**
 * This route HAD no loader, deliberately, and `_app.test.tsx` navigated here
 * because of it: a route that issues no request is the case a redirect-on-401
 * design cannot catch. Plan 12a gives it one, so that case moved to /more,
 * which asks the server for nothing, and the test that stayed here asserts
 * what only a loader-bearing route can show instead — that the child loader
 * really does run and really is rejected on the same load the layout redirects.
 * See `_app.test.tsx`'s first two cases.
 */
export const Route = createFileRoute('/_app/tasks')({
  validateSearch: (search: Record<string, unknown>) => {
    const parsed = searchSchema.safeParse(search)
    return parsed.success ? parsed.data : {}
  },
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
 * them.
 *
 * `bottom-tab-bar mb-4` clears the phone tab bar, which is now genuinely fixed
 * — the previous `bottom-20` was written against a bar that never was, so it
 * cleared nothing and happened to look right. `md:bottom-6 md:mb-0` drops it
 * back down where the nav is a left rail and there is nothing to clear.
 *
 * A PILL RATHER THAN A CIRCLE, and that is a deviation from a locked mockup:
 * `01-app-skeleton.html` and `05-tasks.html` both draw this as border-radius
 * 50%. The label is what makes the control legible, and a circle cannot hold
 * it. Recorded as deliberate; the taste call is sofo's at the Task 34
 * checkpoint. The pill drops `size-14`'s own 56px guarantee, so the target
 * floor falls back to app.css's rule — `Drawer.Trigger` renders a <button>, so
 * it is covered — at 44px, which `py-3` plus `text-sm` meets almost exactly.
 *
 * NAVIGATION IS THE SUCCESS STATE. Creating a task creates its first run, so
 * the honest landing place is that run: a toast over an unchanged list would
 * make the operator go find what they just made.
 */
function NewTaskFab() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const open = drawerOpen(search.new)
  // repositionInputs OFF: vaul's default keyboard handling transforms the
  // sheet against the visual viewport, and on iOS Safari that moved the whole
  // form out of view the moment a field was focused (operator checkpoint,
  // 2026-08-26, real iPhone — invisible in desktop emulation). With it off the
  // sheet stays put and scrolls under the keyboard.
  return (
    <Drawer.Root
      open={open}
      /* EVERY WAY OUT OF THE SHEET COMES THROUGH HERE — Cancel, Escape, the
         swipe, and the overlay tap are all `onOpenChange(false)` in vaul — so
         writing the URL here is what keeps the address honest for all four
         without a handler each.

         A PLAIN OBJECT AND NOT AN UPDATER FUNCTION: `search` given an object
         REPLACES, which is how `{}` clears the param. `(prev) => ({...prev,
         new: undefined})` would be the shape that merges, and it does not
         compile under `exactOptionalPropertyTypes` anyway. */
      onOpenChange={(next) => {
        void navigate({ search: next ? { new: 1 } : {} })
      }}
      repositionInputs={false}
    >
      <Drawer.Trigger className="bg-accent text-accent-contrast fixed right-4 bottom-tab-bar z-10 mb-4 flex items-center justify-center rounded-xl px-5 py-3 text-sm font-medium md:bottom-6 md:mb-0">
        New task
      </Drawer.Trigger>
      <Drawer.Portal>
        {/* z-30 BECAUSE THE TAB BAR IS z-20. vaul sets no z-index anywhere and
            ships no stylesheet, so both of these are `z-index: auto` by
            default — and a positioned z-20 element paints above every
            positioned auto element regardless of DOM order or portal. This
            sheet is bottom-anchored, exactly where the bar now is, so without
            this the bar covers its last ~64px: Cancel and part of the form. */}
        <Drawer.Overlay className="bg-ground/70 fixed inset-0 z-30" />
        <Drawer.Content className="bg-surface border-border fixed inset-x-0 bottom-0 z-30 max-h-[90dvh] overflow-y-auto rounded-t-xl border-t p-4">
          {/* Radix requires both, and neither is decoration: the title names the
              dialog for a screen reader and the description is what
              `aria-describedby` points at when it opens. */}
          <Drawer.Title className="text-text mb-1 text-lg font-medium">New task</Drawer.Title>
          <Drawer.Description className="text-text-muted mb-4 text-sm">
            Caesar creates the task and starts its first run.
          </Drawer.Description>
          <NewTaskForm
            /* NO EXPLICIT CLOSE ANY MORE. `open` is derived from the URL, and
               this navigation leaves `/tasks` entirely — so the param goes with
               it. The old `setOpen(false)` beside this line would now be a
               second navigation racing this one to define the next address. */
            onCreated={(task) => {
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
