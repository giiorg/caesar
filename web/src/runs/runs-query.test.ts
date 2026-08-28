import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../test/msw'
import { runsQueryOptions } from './runs-query'

/**
 * What this file is for: the SPELLINGS on the wire, and nothing else.
 *
 * `runsQueryOptions` maps camelCase filters onto the query parameters the
 * endpoint declares (`dto.go`'s `ListRunsQuery`), and a wrong name there is
 * invisible from every screen — the server IGNORES a parameter it does not
 * know and answers with the unfiltered list, which renders as a list. Task 15
 * adds `workflow_id` and `repo_id` before any screen reads them, so without
 * this the mapping ships untested until the screen that first depends on it.
 *
 * It drives the real `queryFn` through MSW rather than reading the object it
 * builds, because the parameter names only become a URL inside openapi-fetch:
 * asserting the intermediate object would test this test's own copy of the
 * mapping.
 */
const capture = () => {
  const seen: URLSearchParams[] = []
  server.use(
    http.get('/api/v1/runs', ({ request }) => {
      seen.push(new URL(request.url).searchParams)
      return HttpResponse.json({ runs: [], next_cursor: null })
    }),
  )
  return seen
}

// The queryFn takes a react-query context this call never reads; `queryOptions`
// types it as required, so the cast is what lets the function be driven
// directly rather than through a mounted component.
const fetchWith = async (filters: Parameters<typeof runsQueryOptions>[0]) => {
  const seen = capture()
  const options = runsQueryOptions(filters)
  await options.queryFn?.({} as never)
  return seen[0]
}

describe('the runs query', () => {
  it('sends each filter under the name the endpoint declares', async () => {
    const params = await fetchWith({
      status: 'running',
      taskId: 't-1',
      workflowId: 'w-1',
      repoId: 'r-1',
    })
    expect(params?.get('status')).toBe('running')
    expect(params?.get('task_id')).toBe('t-1')
    expect(params?.get('workflow_id')).toBe('w-1')
    expect(params?.get('repo_id')).toBe('r-1')
  })

  it('sends no parameter for a filter that is absent', async () => {
    // The other half, and the one that catches a mapping written with `??`:
    // `?workflow_id=` is not the same request as no `workflow_id` at all —
    // an empty string is a value the server would try to parse as a uuid and
    // reject with a 400, turning an unfiltered list into an error screen.
    const params = await fetchWith({ status: 'running' })
    expect(params?.get('status')).toBe('running')
    expect(params?.has('workflow_id')).toBe(false)
    expect(params?.has('repo_id')).toBe(false)
    expect(params?.has('task_id')).toBe(false)
  })

  it('keys each combination separately, under the prefix the bridge invalidates', async () => {
    // The filters have to ride the SECOND key element: the cache bridge
    // invalidates the bare `['runs']` prefix, and react-query matches by
    // prefix. Two variants must not collide, and both must sit under `runs`.
    const a = runsQueryOptions({ workflowId: 'w-1' }).queryKey
    const b = runsQueryOptions({ repoId: 'r-1' }).queryKey
    expect(a[0]).toBe('runs')
    expect(b[0]).toBe('runs')
    expect(a).not.toEqual(b)
  })
})
