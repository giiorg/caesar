/**
 * THE TWO EMPTY STATES OF THE INBOX, AND THE REASON THEY MUST STAY DIFFERENT.
 *
 * "Nothing is waiting on you" is a statement about the inbox; "no projects are
 * shared with you yet" is a statement about grants. Collapsing them tells a
 * user with no access that they are all caught up, which is the one reading
 * that makes the screen useless to the person it is happening to.
 *
 * ONE COMPONENT BECAUSE TWO SURFACES SHOW THE INBOX — `ApprovalList` on
 * `/approvals` and Home's "Needs you" section — and the mistake this guards
 * against is not a typo but a re-derivation: each host looks obviously correct
 * with a single "you're all caught up", and only the pair states the true
 * thing. `scopeEmpty` comes from the stream through `useStreamStatus()` and
 * never from a literal; a host that hard-codes `false` makes the second
 * sentence unreachable.
 */
export function InboxEmpty(props: { scopeEmpty: boolean }) {
  return (
    <p className="text-text-muted text-sm">
      {props.scopeEmpty ? 'No projects are shared with you yet.' : 'Nothing is waiting on you.'}
    </p>
  )
}
