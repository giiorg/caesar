import { createFileRoute, Link } from '@tanstack/react-router'
import { z } from 'zod'
import {
  ADMIN_SECTION_LABELS,
  ADMIN_SECTIONS,
  type AdminSection,
  asAdminSection,
} from '../../admin/admin-nav'
import { UsersSection } from '../../admin/users-section'
import { StubPanel } from '../../ui/stub-panel'

/**
 * People, agents and guardrails — the locked page map's seventh area.
 *
 * WHICH SECTION IS OPEN IS URL STATE, and `section` carries only the
 * non-default value, the same shape `approvals.index.tsx` gives its tabs and
 * `runs.index.tsx` gives its filters. `/admin` with no search IS Users, so the
 * address somebody sends is the section they were looking at and the link that
 * existed before these sections did still lands somewhere sensible.
 *
 * `safeParse`, and the value is narrowed AGAIN at the read site by
 * `asAdminSection` — see that function for why rejecting a value here does not
 * remove it, and why `?section=42` arrives as a number.
 *
 * NO LOADER, and unlike `approvals.index.tsx` that is not a considered
 * trade-off but a consequence of who can read this screen: every query behind
 * these sections is admin-only, so prefetching at the route level would fire a
 * request that 403s for a member before the component that knows how to explain
 * the refusal has rendered. The sections fetch their own data and each renders
 * its own answer.
 */
const searchSchema = z.object({ section: z.enum(ADMIN_SECTIONS).optional() })

export const Route = createFileRoute('/_app/admin')({
  validateSearch: (search: Record<string, unknown>) => {
    const parsed = searchSchema.safeParse(search)
    return parsed.success ? parsed.data : {}
  },
  component: Admin,
})

const tabId = (section: AdminSection) => `admin-tab-${section}`

function Admin() {
  const section = asAdminSection(Route.useSearch().section)
  return (
    <section className="flex flex-col gap-4">
      <AdminTabs section={section} />
      {/* ONE PANEL. The unselected sections are not hidden with CSS — they are
          a different URL and a different fetch, and rendering all four would
          open every admin query on arrival, three of them for a section nobody
          asked to look at. */}
      <div role="tabpanel" aria-labelledby={tabId(section)}>
        {section === 'users' ? (
          <UsersSection />
        ) : (
          <StubPanel plan="Plan 12" what={label(section)} />
        )}
      </div>
      <RowNinePlaceholder />
    </section>
  )
}

const label = (section: AdminSection): string => ADMIN_SECTION_LABELS[section]

/**
 * The four sections as LINKS, with the ARIA that follows — `approvals.index.tsx`
 * established this pattern and the reasoning transfers whole: `role="tab"` is
 * one of the three selectors `styles/app.css` gives the 44px minimum target;
 * `role="tablist"` carries no navigation role, so these do not join the count
 * of navigation links an e2e spec pins; no roving tabindex, because these are
 * real links whose targets are URLs and a roving tabindex would make three of
 * four unreachable by the key that reaches links; and no `aria-controls`,
 * because only the selected section's panel is in the document.
 *
 * `activeOptions={{ exact: true }}` ON EVERY TAB, though only Users can
 * actually be got wrong without it. `<Link>`'s active test compares its search
 * as a SUBSET of the address bar's, and Users' search is `{}` — a subset of
 * every other section's — so without it the router writes
 * `aria-current="page"` on Users while Audit is open. The other three carry a
 * value nothing else matches, so the subset test is already exact for them and
 * this changes nothing; stating it uniformly says the rule the screen actually
 * follows — a tab is current when the address IS its target — rather than
 * making three tabs depend on a coincidence of the vocabulary. It is also the
 * only spelling that type-checks: `exactOptionalPropertyTypes` is on, so
 * passing `undefined` for the prop is not the same as omitting it, and the
 * conditional form does not compile.
 */
function AdminTabs(props: { section: AdminSection }) {
  return (
    <div role="tablist" aria-label="Admin" className="border-border flex gap-2 border-b">
      {ADMIN_SECTIONS.map((section) => {
        const selected = section === props.section
        return (
          <Link
            key={section}
            id={tabId(section)}
            role="tab"
            aria-selected={selected}
            to="/admin"
            search={section === 'users' ? {} : { section }}
            activeOptions={{ exact: true }}
            className={tabClass(selected)}
          >
            {label(section)}
          </Link>
        )
      })}
    </div>
  )
}

/**
 * The locked page map's fourth Admin item, which this milestone does not build.
 *
 * A NAMED CARD RATHER THAN SILENCE (spec §5). Policy baselines and the durable
 * kill switch are decision 6 of the locked design and row 09's to build; an
 * Admin screen that simply omitted them would read as an Admin screen that is
 * finished, and the reader would have no way to tell a deferred feature from a
 * forgotten one.
 *
 * IT SITS OUTSIDE THE PANEL FOR A SEMANTIC REASON, NOT A VISIBILITY ONE — and
 * the distinction is worth writing down because the draft of this comment got
 * it wrong and a mutant caught the lie. Moving this inside the `tabpanel` does
 * NOT hide it on the other sections: the panel renders on every section, so the
 * card would still be on screen all four times and every assertion about its
 * presence would stay green. What moving it would break is what it CLAIMS: the
 * panel is `aria-labelledby` the selected tab, so a card inside it announces
 * itself as part of the Credentials section — which it is not. It belongs to
 * the screen, so it sits beside the panel and the test asserts that
 * containment rather than mere presence.
 *
 * It is deliberately not `StubPanel`: that component says "not built yet" about
 * a screen this plan WILL build, and its wording ("Plan 12") would date wrongly
 * here. This names the row that owns the work.
 */
function RowNinePlaceholder() {
  return (
    <section className="border-border text-text-muted rounded-md border border-dashed p-4 text-sm">
      <p className="text-text font-medium">Policy baselines and the kill switch</p>
      <p>
        Guardrails are not configurable from here yet — they arrive with row 09, together with run
        cancel. Until then the versioned baseline is whatever the deployment was started with.
      </p>
    </section>
  )
}

/**
 * Deliberately NOT shared with `approvals.index.tsx`'s tabs, which is the call
 * that file already made about `runs.index.tsx`'s filter chips: the three
 * surfaces agree on a MEANING — which URL-state option is current — and the
 * classes are presentation. A shared helper would make a later visual
 * divergence on one screen a shared edit across three.
 *
 * The selected tab is FILLED rather than underlined for the reason
 * `approvals.index.tsx` records at length: the underline idiom needs a
 * see-through bottom border on the unselected tab to reserve the same two
 * pixels, and that utility is one `tools/arch/tokens.ts` rejects outright.
 */
const tabClass = (selected: boolean): string =>
  selected
    ? 'bg-surface text-text flex items-center rounded-t-md px-3 text-sm font-medium'
    : 'text-text-muted flex items-center rounded-t-md px-3 text-sm'
