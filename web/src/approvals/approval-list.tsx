import { Link } from '@tanstack/react-router'
import type { components } from '../api/schema'

type Approval = components['schemas']['ApiApprovalResponse']

/**
 * Card-first (§6). The desktop table projection is Plan 12's; the phone is the
 * device this list exists for, and a table that has to be scrolled sideways to
 * read a title is worse than a card on both.
 *
 * EVERY CARD IS A LINK, AND THE TARGET IS A URL RATHER THAN A DRAWER. This is
 * the screen a notification sends somebody to, and a gate under review is
 * exactly the thing they will want to forward — neither of which survives an
 * open state that lives only in React.
 *
 * THE TWO EMPTY STATES ARE DIFFERENT ANSWERS AND MUST STAY DIFFERENT.
 * "Nothing is waiting on you" is a statement about the inbox; "no projects are
 * shared with you yet" is a statement about grants. Collapsing them tells a
 * user with no access that they are all caught up, which is the one reading
 * that makes the screen useless to the person it is happening to.
 */
export function ApprovalList(props: { approvals: Approval[]; scopeEmpty: boolean }) {
  if (props.approvals.length === 0) {
    return (
      <p className="text-text-muted text-sm">
        {props.scopeEmpty ? 'No projects are shared with you yet.' : 'Nothing is waiting on you.'}
      </p>
    )
  }
  return (
    <ul className="flex flex-col gap-2">
      {props.approvals.map((a) => (
        // Keyed by the identity of the WAITING GATE, not by run: one run can
        // hold several gates, and a retry gives the same node a new attempt.
        <li key={`${a.run_id}:${a.node_id}:${a.attempt}`}>
          {/* The WHOLE card is the target, not a "view" affordance inside it:
              on the phone this list exists for, a link the size of a card is
              the difference between one tap and an aimed one. */}
          <Link
            to="/approvals/$runId/$nodeId"
            params={{ runId: a.run_id, nodeId: a.node_id }}
            className="border-border bg-surface block rounded-md border p-3"
          >
            <p className="text-text font-medium">{a.task_title}</p>
            <p className="text-text-muted font-mono text-xs">
              {a.node_id} · attempt {a.attempt} · waiting since{' '}
              {/* Raw ISO for V1, in a <time> so the machine-readable value is
                  where a reader (and a later formatter) expects it. */}
              <time dateTime={a.waiting_since}>{a.waiting_since}</time>
            </p>
          </Link>
        </li>
      ))}
    </ul>
  )
}
