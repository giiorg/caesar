import type { QueryClient } from '@tanstack/react-query'
import { runsQueryOptions } from '../runs/runs-query'
import { tasksQueryOptions } from '../tasks/tasks-query'
import { PAGES } from './nav'

/**
 * Where a page entry goes. Derived from `PAGES` rather than restated, so the
 * compiler still checks every destination against Task 1's `Register`
 * augmentation — a literal union here would be a second list to keep in step,
 * and the kind that goes wrong silently.
 */
type PageTo = (typeof PAGES)[number]['to']

/**
 * ONE ROW OF THE PALETTE, AS A DISCRIMINATED UNION rather than the plan's
 * `{label, kind, to, params?}`.
 *
 * That shape cannot express its own "New task" row: that row navigates to
 * `/tasks` WITH `search: {new: 1}`, and there is no `search` field in it. The
 * union carries the same information, and the switch that consumes it hands
 * `navigate` a concrete object per kind — so every destination is checked
 * against the route tree with no cast anywhere.
 *
 * `task` and `run` both hold a `runId` because neither has a screen of its own:
 * a task's row goes to its latest run, a run's row goes to the run. They stay
 * distinct kinds because they are reached from different caches and, in the
 * dedup below, one outranks the other.
 */
export type Entry =
  | { kind: 'page'; label: string; to: PageTo }
  | { kind: 'new-task'; label: string }
  | { kind: 'task'; label: string; runId: string }
  | { kind: 'run'; label: string; runId: string }

/**
 * How many recent tasks, and how many recent runs, the palette will offer.
 *
 * `GET /tasks` is unpaginated (tasks-query.ts), so without a cap a warm cache
 * and an empty query would render every task in the deployment. Exported so the
 * test states the cap by reading it rather than by re-asserting the number,
 * which would then have to be corrected in two places.
 */
export const RECENT_LIMIT = 5

export const NEW_TASK_LABEL = 'New task'

const matches = (label: string, query: string): boolean =>
  label.toLowerCase().includes(query.trim().toLowerCase())

/**
 * The palette's rows, for a cache and a query.
 *
 * PURE GIVEN THE CACHE READS, and the reads are PEEKS: `getQueryData` returns
 * what is already there and never fetches. That is the honest behaviour for a
 * palette — a cold cache offers no recents rather than a spinner for a request
 * nobody made — and it is why this function needs no suspense, no loading state
 * and no test that awaits anything.
 *
 * THE KEYS COME FROM THE QUERY OPTIONS THEMSELVES, not from literals. Two
 * things follow. `queryOptions()` brands `queryKey` with `DataTag`, so both
 * peeks are typed (`Task[] | undefined`, `RunSummary[] | undefined`) with no
 * cast; and `runsQueryOptions()` with no argument IS the `['runs', {}]` entry
 * the unfiltered list writes, so this reads the same cache line the Runs screen
 * fills rather than a second spelling that would quietly always be empty.
 */
export function paletteEntries(qc: QueryClient, query: string): Entry[] {
  const tasks = qc.getQueryData(tasksQueryOptions.queryKey) ?? []
  const runs = qc.getQueryData(runsQueryOptions().queryKey) ?? []

  const pages: Entry[] = PAGES.filter((p) => matches(p.label, query)).map((p) => ({
    kind: 'page',
    label: p.label,
    to: p.to,
  }))

  const newTask: Entry[] = matches(NEW_TASK_LABEL, query)
    ? [{ kind: 'new-task', label: NEW_TASK_LABEL }]
    : []

  // FILTER, THEN SLICE — never the other way round. Capping first would make a
  // task past the cap unreachable through the one feature whose purpose is
  // reaching it, and the cap test alone cannot tell the two orders apart.
  const taskEntries: Entry[] = tasks
    .filter((t) => matches(t.title, query))
    .slice(0, RECENT_LIMIT)
    .map((t) => ({ kind: 'task', label: t.title, runId: t.latest_run_id }))

  /**
   * A run says its repo and a task cannot: `repo_slug` lives on `RunSummary`
   * and never on `Task`. So the label carries it, and searching by repo is
   * answerable — which is the only thing making `run` a separate entry class
   * rather than a duplicate of `task`.
   */
  const runLabel = (r: { task_title: string; repo_slug: string }) =>
    `${r.task_title} · ${r.repo_slug}`

  /**
   * DEDUP AGAINST THE MATCHED TASKS, NOT AGAINST EVERY TASK IN THE CACHE.
   *
   * A recent task's latest run is on the one page of runs almost by
   * definition, so the same run arrives from both caches and the two rows
   * navigate to the same place. The task wins: it carries the title the
   * operator typed rather than the summary's copy.
   *
   * Deduping against every task instead would be the wrong set, and would fail
   * exactly where the run entry earns its place: a query matching a run by its
   * REPO matches no task at all, so the task entry is absent and its run must
   * survive — otherwise searching `acme/app` answers with nothing while the
   * matching run sits in the cache.
   */
  const taken = new Set(taskEntries.map((e) => (e.kind === 'task' ? e.runId : '')))
  const runEntries: Entry[] = runs
    .filter((r) => matches(runLabel(r), query) && !taken.has(r.id))
    .slice(0, RECENT_LIMIT)
    .map((r) => ({ kind: 'run', label: runLabel(r), runId: r.id }))

  // Section order is fixed and does not re-rank on the query. A palette whose
  // rows move under a keystroke is one where the arrow key you already pressed
  // meant something else by the time you pressed Enter.
  return [...pages, ...newTask, ...taskEntries, ...runEntries]
}
