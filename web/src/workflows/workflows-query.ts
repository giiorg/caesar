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

/**
 * The node ids in a definition document, or null when it cannot be read.
 *
 * `document` REACHES THIS CLIENT AS `unknown`, and that is the contract being
 * honest rather than the contract being thin. `WorkflowVersionResponse.Document`
 * is a `json.RawMessage`, which swaggest reflects to the EMPTY schema
 * (`"document": {}` — any JSON), so no amount of marking will give it a shape
 * here; what the `required:"true"` on it buys is that the property is always
 * PRESENT, so nothing below carries an `undefined` branch the server cannot
 * produce. The document's real schema is enforced by the write door
 * (`workflows.ParseDocument` + `Validate`), not by the transport.
 *
 * So it is narrowed the way `approvals/artifact-query.ts` narrows an artifact
 * payload: readers that answer `null`, never a cast. `null` is the surface's
 * cue to say the version could not be read — which is the honest thing to say
 * about a document this binary does not understand, and it is not the same as
 * a document with no nodes. A document with no nodes cannot exist: `Validate`
 * refuses one, so an empty array here is already unreachable from the server
 * and any renderer treating "no nodes" as ordinary would be describing a state
 * the system does not have.
 *
 * A PLACEHOLDER, and it says so: Task 25's `derive.ts` is the real parser —
 * typed nodes, edges, the grammar — and this reads exactly the one field the
 * version view needs to prove the document arrived. It goes when that lands.
 */
export function readNodeIds(document: unknown): string[] | null {
  const doc = asObject(document)
  // `Array.isArray`, NOT a presence check. A `nodes` that is present and not an
  // array is the shape that gets past a truthiness test and then breaks: `for
  // (const n of "plan")` iterates CHARACTERS and yields four unreadable nodes,
  // and `for (const n of {})` throws inside a render. Both are documents this
  // reader cannot describe, which is what `null` is for.
  if (doc === null || !Array.isArray(doc.nodes)) return null
  const ids: string[] = []
  for (const node of doc.nodes) {
    const obj = asObject(node)
    // A node with no string id is a document this reader cannot describe, and
    // skipping it would show a person a SHORTER graph than the one the runner
    // executes. Same call `readPlan` makes about a malformed step.
    if (obj === null || typeof obj.id !== 'string') return null
    ids.push(obj.id)
  }
  return ids
}

/**
 * `typeof null === 'object'`, which is the whole reason this exists: without the
 * null check the property reads above would throw rather than answer.
 *
 * IT DOES NOT REJECT ARRAYS, and that is measured rather than sloppy.
 * `artifact-query.ts`'s twin does, because there an array would slip past its
 * field checks in a way that reads as a missing field. Here it cannot: a JSON
 * array never carries named properties, so an array DOCUMENT has no `nodes` and
 * is refused by the `Array.isArray(doc.nodes)` check, and an array NODE has no
 * `id` and is refused by the string check. Adding the guard back is a line no
 * fixture can distinguish — the mutant that deletes it survives every test in
 * this tree, which is the honest reason not to write it.
 */
function asObject(value: unknown): Record<string, unknown> | null {
  if (typeof value !== 'object' || value === null) return null
  return value as Record<string, unknown>
}
