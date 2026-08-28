/**
 * The Admin screen's four sections, and the narrowing its URL state needs.
 *
 * THIS LIST DEPARTS FROM THE LOCKED PAGE MAP BY ONE ITEM, deliberately.
 * `01-app-skeleton.html` (Admin, "People, agents, and guardrails") locks four
 * entries: users & roles, harnesses, POLICY BASELINES, and the global audit
 * log. Policy baselines have no endpoint in this milestone — they are row 09's,
 * with the kill switch — so the fourth slot here is Credentials, which is a
 * real resource with a live API and which the locked map folds into
 * "harnesses" without naming. The locked item is not silently dropped: the
 * screen renders a card naming what arrives with row 09, so the page's
 * eventual shape stays legible (spec §5).
 *
 * ORDER IS PRESENTATION ONLY. Nothing compares two sections, so unlike
 * `GRANT_LEVELS` — whose array index IS the rank — reordering this list
 * changes the tab order and nothing else. The test asserts the order because
 * it is the reading order of a screen, not because a comparison depends on it.
 */
export const ADMIN_SECTIONS = ['users', 'harnesses', 'credentials', 'audit'] as const

export type AdminSection = (typeof ADMIN_SECTIONS)[number]

export const ADMIN_SECTION_LABELS: Record<AdminSection, string> = {
  users: 'Users',
  harnesses: 'Harnesses',
  credentials: 'Credentials',
  audit: 'Audit',
}

/**
 * `users` IS THE DEFAULT AND CARRIES NO SEARCH VALUE, the shape
 * `approvals.index.tsx` and `runs.index.tsx` both use: the address of the
 * screen somebody links to is the screen they were looking at, and arriving at
 * `/admin` with no search is not a different state from arriving at
 * `?section=users`.
 *
 * THE READ SITE RE-NARROWS, and that is not belt-and-braces. A route's search
 * is its ancestors' merged with its own validated result, nothing between `/`
 * and here declares `validateSearch`, so a REJECTED value is not REMOVED — the
 * route's `safeParse` falling back to `{}` still leaves `section` in the search
 * object the component reads. TanStack Router's default parser runs
 * `JSON.parse` per value, so `?section=42` arrives as the NUMBER 42 and a
 * `section?: AdminSection` annotation is a type the runtime is free to violate.
 * `unknown` in, a member of the union out, always.
 */
export const asAdminSection = (value: unknown): AdminSection =>
  ADMIN_SECTIONS.includes(value as AdminSection) ? (value as AdminSection) : 'users'
