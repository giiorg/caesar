import { Link } from '@tanstack/react-router'

/**
 * One entry per row of the V1 page map (decisions §7: Home · Tasks · Runs ·
 * Approvals · Workflows · Repos · Admin).
 *
 * EVERY `to` BELOW IS CHECKED BY THE COMPILER, via Task 1's `Register`
 * augmentation. If a typo in one of these strings compiles, the augmentation
 * is gone and the route typing this project spent a task making falsifiable is
 * decorative — that is the subject of `web:typecheck`'s route-inference mutant,
 * and this table is the largest body of literal route strings in the tree.
 */
const ITEMS = [
  { to: '/', label: 'Home' },
  { to: '/tasks', label: 'Tasks' },
  { to: '/runs', label: 'Runs' },
  { to: '/approvals', label: 'Approvals' },
  { to: '/workflows', label: 'Workflows' },
  { to: '/repos', label: 'Repos' },
  { to: '/admin', label: 'Admin' },
] as const

export function Nav(props: { approvalCount: number }) {
  return (
    <nav aria-label="Sections" className="border-border bg-surface flex overflow-x-auto border-t">
      {ITEMS.map((item) => (
        <Link
          key={item.to}
          to={item.to}
          className="text-text-muted flex flex-1 items-center justify-center gap-1 px-3 py-2 text-sm"
          activeProps={{ className: 'text-accent' }}
        >
          {item.label}
          {/* The count is rendered ONLY when there is something to act on. A
              badge reading "0" is a call to action for nothing. */}
          {item.to === '/approvals' && props.approvalCount > 0 && (
            <span
              data-testid="approvals-badge"
              className="bg-accent text-accent-contrast rounded-sm px-1 font-mono text-xs"
            >
              {props.approvalCount}
            </span>
          )}
        </Link>
      ))}
    </nav>
  )
}
