import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { ApprovalList } from '../../approvals/approval-list'
import { approvalsQueryOptions } from '../../approvals/approvals-query'
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
 */
export const Route = createFileRoute('/_app/approvals/')({
  loader: ({ context }) => context.queryClient.ensureQueryData(approvalsQueryOptions),
  component: Approvals,
})

function Approvals() {
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
