import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ApiError } from '../api/errors'
import { type DecisionRow, decisionsInfiniteQueryOptions } from '../decisions/decisions-query'
import { ApiErrorPanel } from '../errors/api-error-panel'
import { decidedAs } from '../gates/gate-decision'

/**
 * "History of past decisions" — the locked shell's third line under Approvals
 * (`01-app-skeleton.html:65`), which 12a §10.7 named as dropped because no
 * endpoint existed. This is the endpoint's screen.
 *
 * NOT A SUSPENSE QUERY, unlike the waiting arm beside it, and the shape is
 * `EventLog`'s (`routes/_app/runs.$runId.tsx`). The waiting list is what this
 * route is FOR — it has a loader, it suspends, and a failure to read it takes
 * the screen to the error boundary. The history is a second panel the reader
 * opted into; a 500 on it should say so where it is, not replace the screen.
 * The cost is that pending and error are this component's business, which is
 * what the first two branches below are.
 *
 * ROWS ARE RENDERED IN THE ORDER THEY ARRIVE and never re-sorted. The server
 * orders by `(acted_at DESC, id DESC)` and the cursor walks that same key, so a
 * client that sorted its own pages would disagree with the cursor at every page
 * boundary — two rows sharing an `acted_at` would swap sides and one of them
 * would appear twice.
 */
export function DecisionHistory() {
  const history = useInfiniteQuery(decisionsInfiniteQueryOptions)

  if (history.isLoading) {
    return <p className="text-text-muted text-sm">Reading the decision history…</p>
  }
  if (history.isError) {
    // ApiErrorPanel rethrows anything that is not ours, and this error is not
    // always an ApiError: openapi-fetch propagates fetch's own TypeError from a
    // request that never reached the server.
    if (history.error instanceof ApiError) return <ApiErrorPanel error={history.error} />
    return <p className="text-text-muted text-sm">The decision history could not be read.</p>
  }

  const rows = history.data?.pages.flatMap((p) => p.decisions) ?? []
  if (rows.length === 0) {
    // ITS OWN SENTENCE, and `inbox-empty.tsx` carries the rule this obeys:
    // "nothing is waiting on you" is a statement about the inbox, and told to
    // somebody reading the history it says the wrong thing about a system that
    // is simply young. Not routed through `InboxEmpty` for the same reason —
    // that component's pair is about GRANTS, and an empty history under an
    // empty scope is still an empty history.
    return <p className="text-text-muted text-sm">No decisions have been made yet.</p>
  }

  return (
    <div className="flex flex-col gap-3">
      <ul data-testid="decision-history" className="flex flex-col gap-2">
        {rows.map((d) => (
          // Keyed by the DECISION's id, which is the row's own identity. The
          // (run, node) pair is not one: request_changes brings the same gate
          // back under the same node id, so one node really does collect
          // several decisions and the pair repeats down this list.
          <li key={d.id}>
            <DecisionCard decision={d} />
          </li>
        ))}
      </ul>
      {history.hasNextPage && (
        <button
          type="button"
          onClick={() => void history.fetchNextPage()}
          disabled={history.isFetchingNextPage}
          className="border-border text-text-muted self-start rounded-md border px-3 py-1 text-sm"
        >
          {/* LOAD MORE, not an infinite scroller. The locked designs have no
              infinite scroll, and a cursor walked by scroll position on a phone
              is a page the reader cannot get back to. The control disappears
              with `hasNextPage` — a null `next_cursor` is the last page — so it
              can never ask for a page that does not exist. */}
          {history.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}
    </div>
  )
}

/**
 * One decided gate, linking into its run AT THE ATTEMPT IT WAS DECIDED ON.
 *
 * THE SELECTOR TRIPLE IS THE WHOLE POINT OF THE LINK (Task 9, §2.4). Run
 * detail's spine collapses each node to its newest (generation, attempt), so a
 * bare `?phase=<node>` opens whatever that gate looks like NOW — and by the
 * time anybody reads a history row, a request_changes has usually brought the
 * same node back at a higher generation with a different decision on it, made
 * by somebody else about a different patch. `phaseForSelector`
 * (`runs/phase-spine.ts`) is where the triple is resolved; this file is one of
 * its two producers and deliberately resolves nothing itself.
 *
 * The WHOLE card is the target, as on the waiting list beside it: on the phone
 * this screen exists for, a link the size of a card is the difference between
 * one tap and an aimed one.
 */
function DecisionCard(props: { decision: DecisionRow }) {
  const d = props.decision
  return (
    <Link
      to="/runs/$runId"
      params={{ runId: d.run_id }}
      search={{ phase: d.node_id, generation: d.generation, attempt: d.attempt }}
      className="border-border bg-surface block rounded-md border p-3"
    >
      <p className="text-text font-medium">{d.task_title}</p>
      {/* One sentence, one element. testing-library matches a node's DIRECT
          text children, so wrapping the address or the verb in a <span> would
          split the sentence — the lesson `gate-decision.tsx` records. The verb
          comes from that module rather than from a second map here: the two
          surfaces show the same event to the same person, and two spellings of
          "requested changes on" is how they come to disagree about it. */}
      <p className="text-text-muted text-sm">
        {d.acted_by_email} {decidedAs(d.decision)} {d.node_id}, attempt {d.attempt}.
      </p>
      {/* `note` is required and non-nullable on the wire, so "no note" is the
          empty string and not absence — the same test `GateDecision` makes. */}
      {d.note !== '' && <p className="text-text-muted text-sm">{d.note}</p>}
      <p className="text-text-muted font-mono text-xs">
        {/* Raw ISO for V1, in a <time> so the machine-readable value is where a
            reader (and a later formatter) expects it. */}
        <time dateTime={d.acted_at}>{d.acted_at}</time>
      </p>
    </Link>
  )
}
