import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { type RunSummary, runsQueryOptions } from '../runs/runs-query'
import { type Task, tasksQueryOptions } from '../tasks/tasks-query'
import { paletteEntries, RECENT_LIMIT } from './palette-entries'

const AT = '2026-08-20T00:00:00Z'

const taskRow = (over: Partial<Task> & Pick<Task, 'id'>): Task => ({
  project_id: 'p1',
  repo_id: 'repo-1',
  workflow_version_id: 'wv-1',
  title: 'a task',
  inputs: null,
  requester_id: 'u1',
  status: 'running',
  latest_run_id: 'run-1',
  created_at: AT,
  ...over,
})

const runRow = (over: Partial<RunSummary> & Pick<RunSummary, 'id'>): RunSummary => ({
  task_id: 't1',
  task_title: 'a run',
  project_id: 'p1',
  project_name: 'caesar',
  repo_slug: 'acme/app',
  workflow_name: 'default',
  status: 'running',
  attempt: 1,
  generation: 1,
  requester_id: 'u1',
  created_at: AT,
  started_at: AT,
  finished_at: null,
  phase_summary: [],
  ...over,
})

/**
 * A cache seeded the way the app's own screens seed it — `setQueryData` under
 * the SAME key objects the query options carry, never a hand-written literal.
 * A test that spelled `['runs', {}]` itself would keep passing if the module
 * peeked at a key nothing writes, which is the one failure the peek design can
 * actually have.
 */
function cache(seed: { tasks?: Task[]; runs?: RunSummary[] } = {}): QueryClient {
  const qc = new QueryClient()
  if (seed.tasks !== undefined) qc.setQueryData(tasksQueryOptions.queryKey, seed.tasks)
  if (seed.runs !== undefined) qc.setQueryData(runsQueryOptions().queryKey, seed.runs)
  return qc
}

const labels = (qc: QueryClient, query: string) => paletteEntries(qc, query).map((e) => e.label)

describe('paletteEntries', () => {
  it('lists the seven pages and New task, and nothing else, against a cold cache', () => {
    // THE HONEST COLD ANSWER. The peek never fetches, so a palette opened
    // before any list has loaded has no recents to offer — and says so by
    // offering none, rather than by showing a spinner for data it will never
    // ask for.
    expect(labels(cache(), '')).toEqual([
      'Home',
      'Runs',
      'Approvals',
      'Tasks',
      'Workflows',
      'Repos',
      'Admin',
      'New task',
    ])
  })

  it('does not offer More, which is a phone affordance and not one of the seven pages', () => {
    // decisions §7's page map is seven pages; `More` is the fifth phone TAB,
    // holding exactly what Workflows/Repos/Admin already are above. The plan
    // said to consume PHONE_TABS + SECONDARY, which is this row plus eight.
    expect(labels(cache(), '')).not.toContain('More')
    expect(labels(cache(), 'mor')).toEqual([])
  })

  it('matches on a substring, case-insensitively', () => {
    // 'app' is inside 'Approvals' and nowhere else in the page map, so this
    // asserts WHICH page matched rather than that some page did.
    expect(labels(cache(), 'app')).toEqual(['Approvals'])
    expect(labels(cache(), 'APP')).toEqual(['Approvals'])
  })

  it('surfaces a task from the cache by a substring of its title', () => {
    const qc = cache({
      tasks: [
        taskRow({ id: 't1', title: 'Add revocation', latest_run_id: 'run-a' }),
        taskRow({ id: 't2', title: 'Rename the column', latest_run_id: 'run-b' }),
      ],
    })
    expect(labels(qc, 'revoc')).toEqual(['Add revocation'])
    const entry = paletteEntries(qc, 'revoc')[0]
    // The DESTINATION is the assertion that matters: a task's row goes to its
    // latest run, because a task has no screen of its own.
    expect(entry).toEqual({ kind: 'task', label: 'Add revocation', runId: 'run-a' })
  })

  it('reaches a run by its repo, which no task label carries', () => {
    // The run label is `title · repo`, and the repo half is the reason a run is
    // its own entry class at all: `RunSummary` knows the repo and `Task` does
    // not, so this query is answerable only from the runs cache.
    const qc = cache({ runs: [runRow({ id: 'run-a', task_title: 'Add revocation' })] })
    expect(labels(qc, 'acme')).toEqual(['Add revocation · acme/app'])
    expect(paletteEntries(qc, 'acme')[0]).toMatchObject({ kind: 'run', runId: 'run-a' })
  })

  it('yields ONE row for a task and the run it points at', () => {
    // THE DUPLICATE IS THE COMMON CASE, not an edge: a recent task's latest run
    // is on the one page of runs almost by definition, and both entries
    // navigate to the same place. Two rows with one destination is the bug.
    const qc = cache({
      tasks: [taskRow({ id: 't1', title: 'Add revocation', latest_run_id: 'run-a' })],
      runs: [runRow({ id: 'run-a', task_title: 'Add revocation' })],
    })
    const rows = paletteEntries(qc, 'revocation')
    expect(rows).toHaveLength(1)
    // AND THE SURVIVOR IS THE TASK, which carries the title the operator typed
    // rather than the summary's copy of it.
    expect(rows[0]).toMatchObject({ kind: 'task' })
  })

  it('keeps a run whose task is NOT in the filtered set', () => {
    // The other half of dedup, and the half a naive implementation loses:
    // deduping against every task in the cache (rather than against the ones
    // this query actually matched) would delete this run and answer 'acme'
    // with nothing at all.
    const qc = cache({
      tasks: [taskRow({ id: 't1', title: 'Add revocation', latest_run_id: 'run-a' })],
      runs: [runRow({ id: 'run-a', task_title: 'Add revocation' })],
    })
    expect(labels(qc, 'acme')).toEqual(['Add revocation · acme/app'])
  })

  it('caps the recents it offers', () => {
    // The query matches NOTHING in the page map — 'zeta' is in no page label
    // and not in 'New task' — so the length below counts task rows and only
    // task rows. A query like 'task' would have counted the Tasks page and the
    // New task command too and asserted a number about three lists at once.
    const qc = cache({
      tasks: Array.from({ length: RECENT_LIMIT + 3 }, (_, i) =>
        taskRow({ id: `t${i}`, title: `zeta ${i}`, latest_run_id: `run-${i}` }),
      ),
    })
    expect(labels(qc, 'zeta')).toHaveLength(RECENT_LIMIT)
  })

  it('FILTERS BEFORE IT CAPS', () => {
    // The mutant this exists for is `slice()` then `filter()`, which passes the
    // cap test above and makes everything past the cap unreachable through the
    // one feature whose purpose is reaching it. The needle is deliberately last.
    const qc = cache({
      tasks: [
        ...Array.from({ length: RECENT_LIMIT + 3 }, (_, i) =>
          taskRow({ id: `t${i}`, title: `filler ${i}`, latest_run_id: `run-${i}` }),
        ),
        taskRow({ id: 'needle', title: 'Add revocation', latest_run_id: 'run-needle' }),
      ],
    })
    expect(labels(qc, 'revocation')).toEqual(['Add revocation'])
  })
})
