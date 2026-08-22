import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { z } from 'zod'
import { RunCard } from '../../runs/run-card'
import { type RunFilters, runsQueryOptions } from '../../runs/runs-query'

/**
 * `runs.index.tsx` and not `runs.tsx`, and the file name is a routing fact
 * rather than a behavioural one. `/runs/$runId` has to be a SIBLING of the list
 * — MEASURED by generating the tree both ways: with a `runs.tsx` beside
 * `runs.$runId.tsx`, `tsr generate` emits `getParentRoute: () => AppRunsRoute`
 * for the detail, making the list its PARENT layout, and a list that renders no
 * `<Outlet/>` would then have swallowed the whole detail screen. With
 * `runs.index.tsx` both routes get `getParentRoute: () => AppRoute`
 * (routeTree.gen.ts), which is the shape `approvals.index.tsx` already has.
 *
 * WHICH RUNS ARE SHOWN IS URL STATE, per the constitution: a filtered list is
 * exactly the thing somebody sends to somebody else, and React state would make
 * that link land on the unfiltered one.
 *
 * `safeParse` rather than `parse`: TanStack Router's default search parser runs
 * `JSON.parse` over each value and keeps the raw string only when that throws,
 * so `?status=failed` arrives as a string while `?status=42` arrives as the
 * NUMBER 42 — MEASURED in this router, this config. `parse` would throw on it
 * and take the whole screen down over a link somebody mistyped.
 *
 * A status this endpoint does not know is NOT swallowed here. `runlist.go:151`
 * answers `?status=nonsense` with a 400 naming the field, which reaches the
 * layout's error boundary with its correlation id — the honest answer. Clamping
 * it to "no filter" would show every run to somebody who asked for one kind,
 * which is the failure that looks like success.
 */
const searchSchema = z.object({ status: z.string().optional() })

/**
 * The filter, NARROWED AT THE READ SITE BECAUSE `validateSearch` CANNOT DROP A
 * KEY, and that is measured rather than assumed.
 *
 * A route's search is its ancestors' search with its own validated result
 * merged OVER it, and nothing between `/` and here declares `validateSearch` —
 * so the ancestors contribute the whole raw, unvalidated object. MEASURED by
 * instrumenting all three sites on `/runs?status=42`:
 *
 *     VALIDATE in= {"status":42} ok= false out= {}
 *     LOADERDEPS {"status":42} number
 *     USESEARCH  {"status":42} number
 *
 * `validateSearch` rejected the value, returned `{}` — and both readers still
 * got the number. So the schema can only ADD or REPLACE a key here; it can
 * never remove one, and `Route.useSearch()`'s `status?: string` is a type the
 * runtime is free to violate. Without this `typeof` the number would reach the
 * query string as `?status=42`, the server would 400 it, and a mistyped link
 * would take the list down after all — which is exactly what `safeParse` was
 * chosen to prevent.
 *
 * The SAME function feeds `loaderDeps` and the component, so the object the
 * loader prefetches under and the one the component subscribes to hash to the
 * same query key. Two spellings would leave the loader warming a key nothing
 * reads, and the only symptom would be a suspense flash on every filter change.
 *
 * The parameter says `string | undefined` and not `status?: string`, which is
 * NOT the same type under this flag: zod's `.optional()` produces the former
 * and it is not assignable to the latter — MEASURED, TS2379 on both call sites.
 */
const filtersOf = (search: { status?: string | undefined }): RunFilters =>
  typeof search.status === 'string' ? { status: search.status } : {}

export const Route = createFileRoute('/_app/runs/')({
  validateSearch: (search: Record<string, unknown>) => {
    const parsed = searchSchema.safeParse(search)
    return parsed.success ? parsed.data : {}
  },
  loaderDeps: ({ search }) => filtersOf(search),
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(runsQueryOptions(deps)),
  component: Runs,
})

/**
 * Every run status the endpoint accepts (`dto.go:153`), so that every URL a
 * person can arrive on has a control that says so. Offering a shortlist would
 * leave `?status=superseded` rendering a filtered list with nothing on screen
 * admitting it is filtered.
 */
const STATUSES = [
  'pending',
  'running',
  'waiting',
  'succeeded',
  'failed',
  'superseded',
  'dead',
] as const

function Runs() {
  // `filters.status`, never `search.status` — one narrowing for the query, the
  // chips and the empty state, so the filter the list applied is the filter the
  // screen says it applied. See `filtersOf` for what the raw value can be.
  const filters = filtersOf(Route.useSearch())
  const { data: runs } = useSuspenseQuery(runsQueryOptions(filters))

  return (
    <section className="flex flex-col gap-4">
      <StatusFilters current={filters.status} />
      {runs.length === 0 ? (
        /* TWO DISTINCT EMPTY STATES, and the pair a filtered list can actually
           be in. "No runs yet" told to somebody who arrived on `?status=failed`
           is false twice over: it says the system has never run anything, and
           it hides the filter that produced the emptiness — which they may not
           have chosen, having followed a link. */
        <p className="text-text-muted text-sm">
          {filters.status === undefined
            ? 'No runs yet.'
            : `No runs have status "${filters.status}".`}
        </p>
      ) : (
        <ul data-testid="run-list" className="flex flex-col gap-2">
          {runs.map((run) => (
            <li key={run.id}>
              <RunCard run={run} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * WHICH FILTER IS IN FORCE IS THIS COMPONENT'S ANSWER, NOT THE ROUTER'S, and
 * that is a deliberate departure from `runs.$runId.tsx`'s phase rail.
 *
 * `<Link>` writes `aria-current="page"` on the link it considers active, and
 * `activeOptions.includeSearch` compares the link's search as a SUBSET of the
 * address bar's — so "All", whose search is `{}`, is a subset of every other
 * filter's and would read as current on all of them. The rail can lean on the
 * router because every one of its rows names a phase; this row has an
 * unfiltered member and it cannot. `data-current` sits on the <li> rather than
 * on the link for the reason the rail measured: the router overrides an
 * `aria-current` prop passed to `<Link>`.
 */
function StatusFilters(props: { current: string | undefined }) {
  return (
    <ul data-testid="status-filters" className="flex flex-wrap gap-2">
      <li data-status="all" data-current={props.current === undefined ? 'true' : undefined}>
        <Link
          to="/runs"
          search={{}}
          className={chipClass(props.current === undefined)}
          activeOptions={{ exact: true }}
        >
          All
        </Link>
      </li>
      {STATUSES.map((status) => (
        <li
          key={status}
          data-status={status}
          data-current={props.current === status ? 'true' : undefined}
        >
          <Link to="/runs" search={{ status }} className={chipClass(props.current === status)}>
            {status}
          </Link>
        </li>
      ))}
    </ul>
  )
}

const chipClass = (current: boolean): string =>
  current
    ? 'bg-accent text-accent-contrast block rounded-md px-3 py-1 text-sm'
    : 'border-border text-text-muted block rounded-md border px-3 py-1 text-sm'
