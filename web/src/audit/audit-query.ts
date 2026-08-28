import { infiniteQueryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

/** One audit row, as the global log lists it. */
export type AuditEntry = components['schemas']['ApiAuditEntryResponse']

/**
 * The global who-did-what log — the second cursor-paged query in this tree, and
 * the one the pagination was built for.
 *
 * IT LIVES IN ITS OWN GROUP RATHER THAN IN `admin/`, whose section is its only
 * consumer today. `audit_entries` is written from 47 call sites across every
 * bounded context in the backend; the Admin screen is where it is READ first,
 * not what it belongs to, and a run-scoped or subject-scoped view of the same
 * table is the obvious next reader. The misplacement rule permits both
 * placements (it ignores anything with fewer than two consumers, and this
 * module sits at its group root either way — `tools/arch/import-graph.ts`), so
 * this is a statement about ownership rather than a constraint.
 *
 * `AUTH_SCOPED_QUERY_OPTIONS`, like every admin query: `GET /audit` is
 * `adminOnly` (`identity/api/api.go`), so a session demoted mid-visit must stop
 * rendering a log it is no longer allowed to read. The cost the twin note in
 * `decisions/decisions-query.ts` records applies here in full — the refresh
 * re-walks every page the reader has loaded, so a log read ten pages deep costs
 * ten requests a minute — and it buys something extra on this query that it
 * does not buy there: the log is append-only and this screen has no other
 * refresh path, so the interval is also how new rows arrive.
 *
 * THAT RE-WALK RECOMPUTES ITS CURSORS, which is what makes the previous
 * paragraph safe on a table that grows between two walks. MEASURED on the
 * pinned 5.101.4 with a page 1 that answers a DIFFERENT `next_cursor` the
 * second time — the fixture `decisions-query.ts` could not have had, since one
 * that repeats its cursor makes both behaviours identical: the observed
 * sequence is `[null, 'c1', null, 'c1-new']`. The mechanism is
 * `infiniteQueryBehavior.js`'s loop, which takes page 1 from
 * `oldPageParams[0] ?? initialPageParam` and every later page from
 * `getNextPageParam` applied to the page it just fetched, on every fetch that
 * carries no `direction` — the interval and an invalidation alike; only
 * `fetchNextPage` carries one. Reusing the STORED cursor would have restarted
 * page 2 from a boundary that had moved and rendered the rows in between twice.
 * `admin/audit-section.test.tsx` holds the assertion.
 *
 * NOTHING INVALIDATES `['audit']`. Every mutation in this client writes an
 * audit row, so a bridge from the mutations to this key would be an edge from
 * essentially the whole app to a screen almost nobody has open; the interval
 * above is the refresh, and a log that is up to a minute behind is the correct
 * trade.
 *
 * NO `limit`. The server's default is 50 and its ceiling is 200
 * (`platform/audit/read.go`), and a client-side number here would be a second
 * place for the page size to live — one that says nothing the server does not
 * already decide, and that silently disagrees with the cursor's own page size
 * the day one of them changes.
 *
 * The pages are NORMALIZED HERE rather than at the read site. `entries` is
 * required AND nullable — required and nullable are independent, and a Go nil
 * slice marshals to null — so the wire may carry `null` where
 * `auditPageResponse` happens to build `make(...)` today. Doing it here means
 * the section's `flatMap` cannot meet a null, and `render-app.tsx`'s default
 * handler answers `entries: null` so that this branch is on the path of a real
 * test rather than unreachable from every test in the tree.
 */
export const auditInfiniteQueryOptions = infiniteQueryOptions({
  queryKey: ['audit'],
  queryFn: async ({ pageParam }) => {
    // Built by assignment rather than as a literal because tsconfig sets
    // `exactOptionalPropertyTypes`: `{ cursor: undefined }` is not assignable
    // to `{ cursor?: string }` under it, and the FIRST page — the one every
    // reader sees — is exactly the call with no cursor.
    const query: { cursor?: string } = {}
    if (pageParam !== undefined) query.cursor = pageParam
    const res = await unwrap(await api.GET('/api/v1/audit', { params: { query } }))
    return { entries: res.entries ?? [], next_cursor: res.next_cursor }
  },
  // `undefined` IS the first page, and the annotation is what makes the type of
  // every later one `string`. Written as a cast rather than left to inference:
  // a bare `undefined` infers `initialPageParam: undefined`, which makes
  // `getNextPageParam`'s string return an error at this call site rather than a
  // page that never loads at runtime.
  initialPageParam: undefined as string | undefined,
  // A NULL `next_cursor` IS THE LAST PAGE — the contract says so in as many
  // words — and `undefined` is how react-query spells "there is no next page",
  // which is also what removes the Load more control from the screen.
  getNextPageParam: (last) => last.next_cursor ?? undefined,
  ...AUTH_SCOPED_QUERY_OPTIONS,
})
