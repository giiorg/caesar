import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type RunEvent = components['schemas']['ApiRunEventResponse']

/**
 * A run's event log, POLLED. Not subscribed to.
 *
 * `GET /runs/{id}/events` answers a server-sent stream when the request asks
 * for one and this JSON page otherwise (`schema.d.ts`, the operation's own
 * description). ONE STREAM PER TAB is the rule and `_app.tsx:68` holds the only
 * connection, so this reads the JSON half — which is also why the events
 * endpoint is described as the poll-and-resume path rather than a second live
 * channel.
 *
 * THE KEY IS `['run', runId, 'events']` AND ITS REFRESHES ARE EXPLICIT, not a
 * side effect of anyone else's prefix.
 *
 * It used to be the prefix: the bridge invalidated `['run', runId]`, react-query
 * matched by prefix, and this log came along for free. Task 12 ended that — the
 * run DTO now goes out `exact`, because riding the prefix meant a chatty run
 * refetching its whole event log on every debounce window, which was the largest
 * single term in the old refetch fan. So THREE call sites name this key on
 * purpose, and they are the whole mechanism:
 *
 *   - `realtime/cache-bridge.ts`, on the LIFECYCLE tier — immediately, because a
 *     terminal or gate event is the row the reader is waiting for;
 *   - the same file's PHASE tier, on the trailing refresh — a phase burst moves
 *     this log too, just not once per window;
 *   - `gates/decide-gate.ts`, which writes the row it then re-reads.
 *
 * A key respelled `['run-events', runId]` would still fall silently back to the
 * refresh floor, and `routes/_app/runs.$runId.test.tsx` still asserts that round
 * trip end to end rather than the spelling — the same choice `approvals-query.ts`
 * records for its own key.
 *
 * `after` is deliberately unset. It is the resume cursor — the operation
 * describes it as "return events with id greater than this" — and a query that
 * asked for events above the last id it saw would have to merge pages in the
 * client, reconstructing state the server can simply re-send. One page is what
 * the log shows.
 */
export function runEventsQueryOptions(runId: string) {
  return queryOptions({
    queryKey: ['run', runId, 'events'],
    queryFn: async () => {
      const page = await unwrap(
        await api.GET('/api/v1/runs/{id}/events', {
          params: { path: { id: runId } },
          // EXPLICIT, because this URL's answer depends on it. The operation
          // documents that `Accept: text/event-stream` turns the same path into
          // a server-sent stream and "every other Accept keeps this JSON poll"
          // — so the default would work and would leave the one thing that
          // decides which body arrives to a library default.
          headers: { Accept: 'application/json' },
        }),
      )
      // The 200 publishes TWO bodies, so the generated type is the union of the
      // JSON page and a stream frame and this narrowing is what the compiler
      // needs. It is not decoration: a server that answered a stream frame to
      // the Accept above has broken the contract, and returning `[]` for it
      // would render an empty log — a run that has recorded nothing — which is
      // a claim about the run rather than about the answer.
      if (!('events' in page)) throw new Error('run events: the server answered a stream frame')
      // `events` is required AND nullable: a Go nil slice marshals to null and
      // the contract says so (`dto.go` RunEventsResponse). api.go:721 happens
      // to build a non-nil slice today, so reading `.length` straight off this
      // would throw a TypeError on exactly the shape no test would otherwise
      // reach.
      return page.events ?? []
    },
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
}
