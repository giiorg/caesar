import { Link } from '@tanstack/react-router'

/**
 * THE TAB BAR AND THE PAGE MAP ARE DIFFERENT LISTS, and conflating them is the
 * mistake Plan 11 shipped here. decisions §7 states TWO things: a **page map**
 * of seven pages (Home · Tasks · Runs · Approvals · Workflows · Repos · Admin),
 * and a **shell** of five phone tabs — Home · Runs · Approvals (badged) ·
 * Tasks · More — beside a desktop sidebar carrying all seven. This file's
 * previous comment cited the first as authority for the second, and a code
 * comment naming a decisions section is not the decisions section.
 *
 * EVERY `to` BELOW IS CHECKED BY THE COMPILER, via Task 1's `Register`
 * augmentation. If a typo in one of these strings compiles, the augmentation is
 * gone and the route typing this project spent a task making falsifiable is
 * decorative — that is the subject of `web:typecheck`'s route-inference mutant,
 * and these two tables are the largest body of literal route strings in the
 * tree.
 */
export const PHONE_TABS = [
  { to: '/', label: 'Home' },
  { to: '/runs', label: 'Runs' },
  { to: '/approvals', label: 'Approvals' },
  { to: '/tasks', label: 'Tasks' },
  { to: '/more', label: 'More' },
] as const

/**
 * The pages More holds, and the ones the desktop rail adds to the four real
 * tabs above. Exported so `/more` and the rail render ONE list: two copies
 * would drift the first time a page is added, and the phone would be the
 * surface that lost it.
 */
export const SECONDARY = [
  { to: '/workflows', label: 'Workflows' },
  { to: '/repos', label: 'Repos' },
  { to: '/admin', label: 'Admin' },
] as const

/**
 * `PHONE_TABS.slice(0, 4)` and not a third literal: the rail is the four real
 * tabs plus the secondary pages. `More` is a phone affordance with nothing
 * behind it on a surface that already shows everything it holds.
 */
const RAIL = [...PHONE_TABS.slice(0, 4), ...SECONDARY] as const

const linkClass = 'text-text-muted flex items-center gap-1 px-3 py-2 text-sm'

function Badge(props: { count: number }) {
  // The count renders ONLY when there is something to act on. A badge reading
  // "0" is a call to action for nothing.
  if (props.count <= 0) return null
  return (
    <span
      data-testid="approvals-badge"
      className="bg-accent text-accent-contrast rounded-sm px-1 font-mono text-xs"
    >
      {props.count}
    </span>
  )
}

export function Nav(props: { approvalCount: number }) {
  return (
    /* ONE landmark, and `md:order-first` so the rail precedes <main> on a wide
       viewport while the DOM order keeps the tab bar last in a phone's tab
       sequence.

       THE TWO PRESENTATIONS ARE CSS, NOT TWO <nav> ELEMENTS. jsdom applies no
       media queries, so a second nav would be present in every component test
       and would make the 'navigation' role ambiguous in _app.test.tsx's nine
       findByRole call sites and in the two getByRole ones under e2e/ — one in
       approvals.spec.ts, one in harness.ts's signIn, which every spec awaits.
       Both presentations therefore render the badge: which one a person sees is
       a viewport's decision, and a badge in only one of them is a badge missing
       on one of the two surfaces. */
    <nav aria-label="Sections" className="fixed inset-x-0 bottom-0 z-20 md:static md:order-first">
      {/* THE LANDMARK IS WHAT IS PINNED, not the div inside it — MEASURED, and
          the other way round breaks five specs. Putting `fixed` on this inner
          div takes the phone's only in-flow child out of the flow while the
          rail beside it is `display:none` at 412px, so the <nav> itself is left
          with an EMPTY BOUNDING BOX: `harness.ts`'s signIn asserts
          `getByRole('navigation')` is visible and every e2e spec awaits it, so
          all five went red at login on a class-only diff. Playwright's
          visibility is a box, and a landmark whose whole content is out of flow
          does not have one. `md:static` hands the position back where the rail
          is a flow sidebar; inset and z-index do not apply to a static box, so
          nothing else needs undoing.

          `z-20` is the top of this screen's stack (decision bar and FAB are
          z-10, so the walk's most important buttons are never under chrome),
          and it is also what forces the New-task drawer to z-30 in tasks.tsx:
          vaul sets no z-index at all, so its sheet would otherwise be painted
          over by this bar exactly where it opens. */}
      {/* The height is the token's TOTAL. `h-tab-bar` is the content row plus
          the home-indicator strip; under the global border-box the
          `pb-[env(safe-area-inset-bottom)]` carves that strip out rather than adding
          to it, so the tabs keep their full 4rem and still clear the indicator.
          Drop the padding and the tabs paint under it; drop the inset from the
          token and they lose 34px of touch target. Neither is visible in CI —
          the Playwright project and Chrome emulation both run at inset 0. */}
      <div
        data-testid="phone-tabs"
        className="border-border bg-surface flex h-tab-bar border-t pb-[env(safe-area-inset-bottom)] md:hidden"
      >
        {PHONE_TABS.map((item) => (
          <Link
            key={item.to}
            to={item.to}
            className={`${linkClass} flex-1 justify-center`}
            activeProps={{ className: 'text-accent' }}
          >
            {item.label}
            {item.to === '/approvals' && <Badge count={props.approvalCount} />}
          </Link>
        ))}
      </div>
      <div
        data-testid="desktop-rail"
        className="border-border bg-surface hidden w-44 shrink-0 md:flex md:flex-col md:border-r"
      >
        {RAIL.map((item) => (
          <Link
            key={item.to}
            to={item.to}
            className={linkClass}
            activeProps={{ className: 'text-accent' }}
          >
            {item.label}
            {item.to === '/approvals' && <Badge count={props.approvalCount} />}
          </Link>
        ))}
      </div>
    </nav>
  )
}
