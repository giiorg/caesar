import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Project = components['schemas']['ApiProjectResponse']
export type Repo = components['schemas']['ApiRepoResponse']

/**
 * The three reads the Repos screen is built on, and the group they live in is
 * the answer to a question the new-task form asked first.
 *
 * `['projects']` and `['project-repos', id]` were LOCAL CONSTS inside
 * `tasks/new-task-form.tsx` until this task. They are promoted rather than
 * re-spelled because a second spelling of a cache key is not a duplicate
 * string — it is a second cache entry. The form's project select and this
 * screen's card list would then hold independently stale copies of the same
 * list, and creating a project here would refresh one of them.
 *
 * `tools/arch/import-graph.ts` clamps a module with two or more consumers to
 * its own GROUP ROOT, and this file sits at `src/projects/` — that root — so it
 * is exempt at any consumer count. A `src/projects/queries/` subdirectory would
 * have been reported the moment the second importer landed.
 */
export const projectsQueryOptions = queryOptions({
  queryKey: ['projects'],
  queryFn: async () => {
    const res = await unwrap(await api.GET('/api/v1/projects'))
    // `projects` is required AND nullable — required and nullable are
    // independent, and a Go nil slice marshals to null. The handler happens to
    // build a non-nil slice today, so the wire carries `[]`; reading `.map` off
    // what the CONTRACT permits is what breaks, and it breaks on exactly the
    // case a new deployment starts in. Same trade as `tasksQueryOptions`.
    return res.projects ?? []
  },
  ...AUTH_SCOPED_QUERY_OPTIONS,
})

/**
 * One project, for the detail screen's header.
 *
 * SEPARATE FROM THE LIST RATHER THAN A `find` OVER IT, and that is not
 * defensiveness. `/repos/$projectId` is a linkable address: arriving there
 * cold — from a bookmark, from a notification, from the address bar — there is
 * no list in the cache to search, and a `find` would have an `undefined` arm
 * that renders as either a lie or a blank. The endpoint answers 404 for an id
 * that is not there and 403 for one the caller cannot see, and both are
 * sentences `ApiErrorPanel` already knows how to say.
 */
export const projectQueryOptions = (projectId: string) =>
  queryOptions({
    queryKey: ['project', projectId],
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/projects/{id}', { params: { path: { id: projectId } } })),
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })

/**
 * A project's repos.
 *
 * THE ONLY WAY TO LIST REPOS IS PROJECT-SCOPED — `GET /projects/{id}/repos`
 * (`assets/api/api.go:63`); assets exposes no repo collection of its own, only
 * `GET /repos/{id}` for one by id. That is the whole reason projects are the
 * organizing unit of this screen rather than a filter on it: there is no
 * unscoped repo list to render, and inventing one client-side would mean a
 * fetch per project on the list page for data nothing asked for.
 *
 * `enabled` on the empty id is what the new-task form's cascade needs — its
 * repo select has nothing to ask for until a project is picked. The routes here
 * always pass a real id from the path, so for them the guard never fires.
 */
export const projectReposQueryOptions = (projectId: string) =>
  queryOptions({
    queryKey: ['project-repos', projectId],
    queryFn: async () =>
      (
        await unwrap(
          await api.GET('/api/v1/projects/{id}/repos', { params: { path: { id: projectId } } }),
        )
      ).repos ?? [],
    enabled: projectId !== '',
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
