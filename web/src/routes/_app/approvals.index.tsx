import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { z } from 'zod'
import { ApprovalList } from '../../approvals/approval-list'
import { approvalsQueryOptions } from '../../approvals/approvals-query'
import { DecisionHistory } from '../../approvals/decision-history'
import { useStreamStatus } from '../../realtime/stream-status'

/**
 * The first route in this tree that FETCHES ANYTHING OF ITS OWN, which is what
 * finally produces a 401 arriving at `_app`'s error boundary from below rather
 * than from the layout's own session query.
 *
 * The loader is not what does that — MEASURED: remove it and the failure still
 * reaches the same boundary, because `useSuspenseQuery` throws to the same
 * place. What the loader buys is a router-level load: the fetch starts with the
 * route rather than with its first render, and `defaultPreload: 'intent'`
 * (router.ts) can start it on hover. No test in this tree distinguishes the two,
 * and that is recorded rather than papered over.
 *
 * IT STAYS THE WAITING ARM'S LOADER AND ONLY ITS. The History tab (Task 18)
 * reads a second endpoint and deliberately does not prefetch it: history is a
 * panel the reader opts into, and warming it on every arrival at `/approvals`
 * would put a request on the critical path of the screen's actual job.
 *
 * WHICH TAB IS OPEN IS URL STATE, and `tab` carries only the non-default value.
 * "Waiting" is `/approvals` with no search at all — the same shape
 * `runs.index.tsx` gives its unfiltered "All" — so the link somebody sends is
 * the screen they were looking at, and the address a notification points at is
 * unchanged from before this tab existed.
 *
 * `safeParse`, and the value is narrowed again at the read site, for the reason
 * both sibling routes MEASURED: a route's search is its ancestors' search with
 * its own validated result merged over it, nothing between `/` and here
 * declares `validateSearch`, so REJECTING A VALUE DOES NOT REMOVE IT. TanStack
 * Router's default parser runs `JSON.parse` per value, so `?tab=42` arrives as
 * the NUMBER 42 and `tab?: 'history'` is a type the runtime is free to violate.
 * Unlike `runs.index.tsx`'s `status`, this value is never sent anywhere — it is
 * only ever compared against one literal — so the comparison IS the narrowing
 * and no `typeof` guard is needed to make it safe.
 */
const searchSchema = z.object({ tab: z.literal('history').optional() })

export const Route = createFileRoute('/_app/approvals/')({
  validateSearch: (search: Record<string, unknown>) => {
    const parsed = searchSchema.safeParse(search)
    return parsed.success ? parsed.data : {}
  },
  loader: ({ context }) => context.queryClient.ensureQueryData(approvalsQueryOptions),
  component: Approvals,
})

const WAITING_TAB_ID = 'approvals-tab-waiting'
const HISTORY_TAB_ID = 'approvals-tab-history'

function Approvals() {
  const history = Route.useSearch().tab === 'history'
  return (
    <section className="flex flex-col gap-4">
      <ApprovalTabs history={history} />
      {/* ONE PANEL, and the unselected tab's content is not merely hidden: it
          is a different URL and a different fetch. Rendering both and toggling
          CSS would open the history's connection for every reader of the
          inbox, which is the opposite of what the tab is for. */}
      <div role="tabpanel" aria-labelledby={history ? HISTORY_TAB_ID : WAITING_TAB_ID}>
        {history ? <DecisionHistory /> : <WaitingList />}
      </div>
    </section>
  )
}

/**
 * The two tabs, which are LINKS — and the ARIA that follows from that, together
 * with the two halves of the APG pattern this deliberately does not implement.
 *
 * `role="tab"` rather than a bare link, because `styles/app.css` states the
 * 44px minimum target once for `button`, `a[role="button"]` and `[role="tab"]`
 * rather than having each component remember it. That third selector had no
 * subject in the tree until now.
 *
 * NOT A `<nav>`, and that is measured rather than stylistic:
 * `e2e/approvals.spec.ts` asserts that `getByRole('navigation')` contains
 * exactly TWO links to `/approvals` — the phone bar and the desktop rail — and
 * the Waiting tab's href is exactly `/approvals`. A `<nav>` here would make it
 * three on this screen. `role="tablist"` carries no navigation role.
 *
 * NO ROVING TABINDEX, deliberately. APG's tab widget moves focus between tabs
 * with the arrow keys and keeps one tab stop for the set; these tabs are
 * navigation whose targets are URLs, and a roving tabindex would make one of
 * two real links unreachable by the key that reaches links. Tab through both is
 * the correct behaviour for what they are. `aria-controls` is omitted for a
 * related reason: only the selected tab's panel exists in the document, so
 * pointing the unselected tab at it would be a false statement about what that
 * tab controls.
 *
 * WHICH TAB IS SELECTED IS THIS COMPONENT'S ANSWER, NOT THE ROUTER'S, exactly
 * as `runs.index.tsx`'s filter chips are. `<Link>`'s active test compares its
 * search as a SUBSET of the address bar's, so Waiting — whose search is `{}` —
 * is a subset of `?tab=history` and would read as current on BOTH tabs.
 * `activeOptions={{ exact: true }}` is what stops the router writing
 * `aria-current="page"` on the tab that is not open; `aria-selected` is what
 * says which one is.
 */
function ApprovalTabs(props: { history: boolean }) {
  return (
    <div role="tablist" aria-label="Approvals" className="border-border flex gap-2 border-b">
      <Link
        id={WAITING_TAB_ID}
        role="tab"
        aria-selected={!props.history}
        to="/approvals"
        search={{}}
        activeOptions={{ exact: true }}
        className={tabClass(!props.history)}
      >
        Waiting
      </Link>
      <Link
        id={HISTORY_TAB_ID}
        role="tab"
        aria-selected={props.history}
        to="/approvals"
        search={{ tab: 'history' }}
        className={tabClass(props.history)}
      >
        History
      </Link>
    </div>
  )
}

/**
 * NO UNDERLINE-ON-THE-SELECTED-TAB, and the reason is `web:arch`'s token rule
 * rather than taste. That idiom needs a see-through bottom border on the
 * unselected tab to reserve the same two pixels, and the bare Tailwind colour
 * name for see-through is one of the four `tools/arch/tokens.ts` rejects
 * outright — it is a colour that evades the theme. (The rule reads the whole
 * file, comments included, so this paragraph cannot spell the utility it is
 * about.) Filling the selected tab instead says the same thing with two
 * semantic tokens and reserves nothing, so neither tab moves when the other is
 * chosen.
 *
 * Deliberately NOT shared with `runs.index.tsx`'s filter chips. The two
 * surfaces answer the same question — which URL-state option is current — and
 * that MEANING is what the two files agree on; the classes are presentation,
 * and a shared helper would make a later visual divergence a shared edit.
 */
const tabClass = (selected: boolean): string =>
  selected
    ? 'bg-surface text-text flex items-center rounded-t-md px-3 text-sm font-medium'
    : 'text-text-muted flex items-center rounded-t-md px-3 text-sm'

function WaitingList() {
  const { data } = useSuspenseQuery(approvalsQueryOptions)
  // scopeEmpty comes from the stream, not from a literal. Hardcoding false
  // makes "No projects are shared with you yet" unreachable and tells a user
  // with no grants that nothing is waiting on them — a different and wrong
  // statement. It is READ FROM CONTEXT, never by calling useAttentionStream()
  // again: that hook opens a connection, and a second caller would open a
  // second stream per tab.
  const { scopeEmpty } = useStreamStatus()
  return <ApprovalList approvals={data} scopeEmpty={scopeEmpty} />
}
