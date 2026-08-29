import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Workflow = components['schemas']['ApiWorkflowResponse']
export type WorkflowVersionMeta = components['schemas']['ApiWorkflowVersionMetaResponse']
export type WorkflowVersion = components['schemas']['ApiWorkflowVersionResponse']

/**
 * Every workflow, and the key was already spoken for.
 *
 * PROMOTED, NOT RE-SPELLED. This was a local const in
 * `tasks/new-task-form.tsx` — same `['workflows']` key, same `?? []`, same
 * auth-scoped options — and the form imports it from here now. A second
 * spelling of a cache key is not a duplicated string, it is a SECOND CACHE
 * ENTRY: the form's workflow select and this screen's card list would hold
 * independently stale copies of one list, and creating a workflow here would
 * refresh exactly one of them. `projects-query.ts` records the same move for
 * the same reason, made by the same form.
 *
 * `tools/arch/import-graph.ts` clamps a module with two or more consumers to
 * its own group root, and this file sits at `src/workflows/` — that root — so
 * it is exempt at any consumer count.
 *
 * `workflows` is required AND nullable: required and nullable are independent,
 * and a Go nil slice marshals to null. `handleListWorkflows` happens to build a
 * non-nil slice today (`workflows/api/api.go`), so the coalesce covers what the
 * CONTRACT permits rather than what today's handler sends — and
 * `render-app.tsx`'s default answers `workflows: null` so that branch sits on a
 * real test's path.
 */
export const workflowsQueryOptions = queryOptions({
  queryKey: ['workflows'],
  queryFn: async () => (await unwrap(await api.GET('/api/v1/workflows'))).workflows ?? [],
  ...AUTH_SCOPED_QUERY_OPTIONS,
})

/**
 * One workflow, for the detail header.
 *
 * SEPARATE FROM THE LIST RATHER THAN A `find` OVER IT, for the reason
 * `projectQueryOptions` states: `/workflows/$workflowId` is a linkable address,
 * and arriving there cold there is no list in the cache to search. The
 * endpoint answers 404 for an id that is not there, which `ApiErrorPanel`
 * already knows how to say.
 */
export const workflowQueryOptions = (workflowId: string) =>
  queryOptions({
    queryKey: ['workflow', workflowId],
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/workflows/{id}', { params: { path: { id: workflowId } } })),
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })

/**
 * A workflow's version metadata, newest first — the server's order, kept.
 *
 * `ListVersions` orders by version descending in SQL and the client does not
 * re-sort. Sorting here would be a second opinion about an ordering the server
 * already holds, and the two would disagree the day the server's changes.
 *
 * AUTH-SCOPED LIKE THE LIST, and unlike the individual versions below: this
 * collection GROWS — every save through the editor adds a row — so the refresh
 * floor is also how a version registered in another tab shows up here.
 */
export const workflowVersionsQueryOptions = (workflowId: string) =>
  queryOptions({
    queryKey: ['workflow', workflowId, 'versions'],
    queryFn: async () =>
      (
        await unwrap(
          await api.GET('/api/v1/workflows/{id}/versions', {
            params: { path: { id: workflowId } },
          }),
        )
      ).versions ?? [],
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })

/**
 * ONE VERSION, DOCUMENT INCLUDED — and the one query on this screen that takes
 * no refresh interval.
 *
 * A VERSION IS IMMUTABLE. `CreateVersion` inserts and never updates; a changed
 * definition is a NEW version number, which is a new cache entry. Re-polling a
 * document every sixty seconds would be bandwidth spent to learn it is
 * unchanged — the same trade `artifactQueryOptions` makes, and the precedent
 * this follows rather than re-argues. The authorization CAN still narrow, and
 * that is covered where it matters: the list and the detail both carry the
 * floor, so a reader who loses their session leaves those screens within the
 * stated interval.
 *
 * `version` IS A STRING because both ends of it are: it arrives as a URL
 * segment from the route params and the generated client declares the path
 * parameter `version: string`. Routing it through `Number` would add the one
 * branch this function otherwise has none of — `Number('abc')` is `NaN`, and a
 * request for `/versions/NaN` is a 404 the client manufactured. The server
 * parses and bounds it (`pathVersion`: int32, `>= 1`, 404 otherwise), which is
 * the check that has the schema behind it.
 */
export const workflowVersionQueryOptions = (workflowId: string, version: string) =>
  queryOptions({
    queryKey: ['workflow', workflowId, 'version', version],
    queryFn: async () =>
      unwrap(
        await api.GET('/api/v1/workflows/{id}/versions/{version}', {
          params: { path: { id: workflowId, version } },
        }),
      ),
    staleTime: Number.POSITIVE_INFINITY,
  })
