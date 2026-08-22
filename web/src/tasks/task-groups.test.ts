import { describe, expect, it } from 'vitest'
import { ALL_RUN_STATUSES, groupTasks, type TaskGroup } from './task-groups'
import type { Task } from './tasks-query'

/**
 * One task, annotated `Task` so `tsc --noEmit` forces every key the contract
 * declares. Plan 12a marked `TaskResponse` required throughout, so a fixture
 * missing a property no longer compiles — which is what stops these tests from
 * describing a wire shape the server cannot send.
 */
const t = (status: string): Task => ({
  id: `task-${status}`,
  project_id: 'p1',
  repo_id: 'repo-1',
  workflow_version_id: 'wv-1',
  title: `a ${status} task`,
  inputs: null,
  requester_id: 'u1',
  status,
  latest_run_id: 'run-1',
  created_at: '2026-08-20T00:00:00Z',
})

describe('groupTasks', () => {
  it('places every one of the seven run statuses', () => {
    // THE WHOLE POINT. There is no task lifecycle to lean on, so this mapping
    // IS the design — and a status that falls through the switch does not land
    // in a wrong group, it takes its task off the screen entirely.
    const want: Record<string, TaskGroup> = {
      waiting: 'needs-you',
      running: 'in-flight',
      pending: 'up-next',
      succeeded: 'recently-done',
      failed: 'recently-done',
      dead: 'recently-done',
      superseded: 'recently-done',
    }
    expect(Object.keys(want).sort()).toEqual([...ALL_RUN_STATUSES].sort())
    for (const [status, group] of Object.entries(want)) {
      const got = groupTasks([t(status)])
      expect(got[group]).toHaveLength(1)
      // And in exactly one group.
      expect(Object.values(got).flat()).toHaveLength(1)
    }
  })

  it('never drops a task, whatever the server sends', () => {
    // A status added server-side must degrade to VISIBLE. Disappearing from
    // the list is the one behaviour a person cannot diagnose: there is nothing
    // on screen to be wrong about.
    const got = groupTasks([t('a_status_from_the_future')])
    expect(Object.values(got).flat()).toHaveLength(1)
  })

  it('keeps ALL_RUN_STATUSES honest against the contract', () => {
    // Hand-maintained lists rot. This one mirrors schema.sql's CHECK
    // constraint on runs.status (schema.sql:216), and the exhaustiveness
    // assertion above is only as good as it is.
    expect([...ALL_RUN_STATUSES]).toEqual([
      'pending',
      'running',
      'waiting',
      'succeeded',
      'failed',
      'superseded',
      'dead',
    ])
  })

  it('keeps each group in the order the server sent them', () => {
    // The server orders `GET /tasks` and the screen does not re-sort, so the
    // rows inside a section are in the order the list arrived. Asserted because
    // a `for` loop over four accumulator arrays is one `unshift` away from
    // reversing every section, and nothing else here would notice.
    const got = groupTasks([t('running'), { ...t('running'), id: 'second' }])
    expect(got['in-flight'].map((task) => task.id)).toEqual(['task-running', 'second'])
  })
})
