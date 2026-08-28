import { infiniteQueryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

/** One decided gate, as the history lists it. */
export type DecisionRow = components['schemas']['ApiDecisionResponse']

/**
 * The cross-run decision history — the first CURSOR-PAGED query in this tree.
 *
 * THE KEY IS BARE `['decisions']` AND NOTHING INVALIDATES IT FROM THE STREAM.
 * That is deliberate and it is the one place this query differs from every
 * other list here. The cache bridge fans a frame out to the inbox, the run, the
 * collections and the event log because those move while a run is in flight;
 * this list moves only when a HUMAN decides something, and the human who did it
 * is holding a mutation that invalidates this key itself
 * (`gates/decide-gate.ts`, where the twin half of this note lives). Somebody
 * else's decision arrives on the refresh floor below rather than instantly, and
 * a history that is up to a minute behind is the correct trade for keeping a
 * chatty run off a query nobody is watching change.
 *
 * `AUTH_SCOPED_QUERY_OPTIONS` COSTS MORE HERE THAN ANYWHERE ELSE, and the
 * reason it is still spread is the reason it exists. An invalidation refetches
 * EVERY page the query has loaded, not just the newest — MEASURED on the pinned
 * 5.101.4: a two-page history invalidated on `['decisions']` re-requested the
 * cursor sequence `[null, 'c1']`, i.e. exactly the walk that loaded it. So a
 * history read ten pages deep costs ten requests per refresh, and the decide
 * mutation's own invalidation pays the same bill. The floor stays because a
 * grant NARROWING closes no stream and emits no frame: nothing else in the
 * client will ever mention that a row on screen belongs to a project the reader
 * has just lost, and this list is one a person leaves open.
 *
 * The pages are NORMALIZED HERE rather than at the read site. `decisions` is
 * required AND nullable — required and nullable are independent, and a Go nil
 * slice marshals to null — so the wire may carry `null` where the handler
 * happens to build `make(...)` today. Doing it here means the component's
 * `flatMap` cannot meet a null, and `render-app.tsx`'s default handler answers
 * `decisions: null` so that this branch is on the path of a real test rather
 * than unreachable from every test in the tree.
 */
export const decisionsInfiniteQueryOptions = infiniteQueryOptions({
  queryKey: ['decisions'],
  queryFn: async ({ pageParam }) => {
    // Built by assignment rather than as a literal because tsconfig sets
    // `exactOptionalPropertyTypes`: `{ cursor: undefined }` is not assignable
    // to `{ cursor?: string }` under it, and the FIRST page — the one every
    // reader sees — is exactly the call with no cursor.
    const query: { cursor?: string } = {}
    if (pageParam !== undefined) query.cursor = pageParam
    const res = await unwrap(await api.GET('/api/v1/approvals/decisions', { params: { query } }))
    return { decisions: res.decisions ?? [], next_cursor: res.next_cursor }
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
