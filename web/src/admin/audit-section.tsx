import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { type AuditEntry, auditInfiniteQueryOptions } from '../audit/audit-query'
import { AdminRefusal } from './admin-refusal'
import { usersQueryOptions } from './users-query'

/**
 * The global audit log — the table written since Plan 02 and never readable.
 *
 * IT JOINS AGAINST THE USER LIST, which the decision history beside it does not
 * have to: `ApiDecisionResponse` carries `acted_by_email` because the server
 * joins for it, while `ApiAuditEntryResponse` carries `actor_user_id` and
 * nothing else. A raw UUID answers "who did this" with nothing. It is the same
 * call `harnesses-section.tsx` makes about `credential_id` one file over, with
 * one honest difference worth writing down: THERE the second list was already
 * on screen, HERE it is a request this section adds. Only one section's panel
 * is mounted at a time, so opening Audit really does fetch `/users` that a
 * reader who never opens the Users tab would not otherwise have fetched. It is
 * the same admin-only query under the same key, so the two sections share one
 * cache entry and a reader who visits both pays once — and the alternative is
 * either a column of UUIDs or a backend join added to spare one request the
 * client is already entitled to make.
 */
export function AuditSection() {
  const log = useInfiniteQuery(auditInfiniteQueryOptions)
  const users = useQuery(usersQueryOptions)

  if (log.isError) return <AdminRefusal error={log.error} />
  if (log.isLoading) return <p className="text-text-muted text-sm">Reading the audit log…</p>

  const entries = log.data?.pages.flatMap((p) => p.entries) ?? []

  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-text font-medium">Audit</h2>
      {entries.length === 0 ? (
        /* A BRANCH THAT IS ALL BUT UNREACHABLE, AND STILL THE RIGHT ONE. The
           first row exists before anybody can read this screen: bootstrap
           writes `user.bootstrapped` (`identity/service.go`) and the admin
           looking at it wrote `auth.login` by arriving. So this sentence is
           defensive rather than expected — but a list with no empty branch
           paints a blank panel, and a blank panel reads as broken. The
           opposite call from `users-section.tsx`, which has no empty state at
           all, and for the opposite reason: there the reader is always IN the
           answer, so an empty list is not merely unlikely but impossible. */
        <p className="text-text-muted text-sm">The audit log is empty.</p>
      ) : (
        <ul data-testid="audit-entries" className="flex flex-col gap-2">
          {entries.map((entry) => (
            // Keyed by the row's own id — a `bigint GENERATED ALWAYS AS
            // IDENTITY` (`schema.sql`) and the only identity here.
            // `(action, subject_id)` is not one: `credential.lease_acquired`
            // lands on the same credential once per phase of every run.
            <AuditRow key={entry.id} entry={entry} users={users.data} />
          ))}
        </ul>
      )}
      {log.hasNextPage && (
        <button
          type="button"
          onClick={() => void log.fetchNextPage()}
          disabled={log.isFetchingNextPage}
          className="border-border text-text-muted self-start rounded-md border px-3 py-1 text-sm"
        >
          {/* LOAD MORE, not an infinite scroller — `decision-history.tsx`
              carries the argument. The control disappears with `hasNextPage`, a
              null `next_cursor` being the last page, so it can never ask for a
              page that does not exist. */}
          {log.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}
    </section>
  )
}

/** The account list this screen resolves actors against, as much of it as it uses. */
type Account = { id: string; email: string }

function AuditRow(props: { entry: AuditEntry; users: readonly Account[] | undefined }) {
  const { entry } = props
  return (
    <li
      data-testid={`audit-${entry.id}`}
      className="border-border bg-surface flex flex-col gap-1 rounded-md border p-3"
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        {/* The action verbatim, in the vocabulary the writers use
            (`harness.registered`, `credential.lease_acquired`). Not prettified:
            47 call sites mint these strings and any mapping here would be a
            list that silently stops covering the log the day a context adds an
            action, rendering a blank or a stale English phrase for it. */}
        <span className="text-text grow font-medium">{entry.action}</span>
        {/* Raw ISO in a <time>, the shape `decision-history.tsx` set for V1. */}
        <time dateTime={entry.occurred_at} className="text-text-muted font-mono text-xs">
          {entry.occurred_at}
        </time>
      </div>
      {/* KIND AND ID IN ONE ELEMENT, and the kind first. The id alone does not
          say which table it indexes, and `subject_id` IS NOT ALWAYS AN ID:
          `auth.login_failed` and `auth.rate_limited` put the lowercased email
          somebody tried to log in as in this field (`identity/login.go`). So
          nothing here truncates it, links it or otherwise treats it as an
          identifier — on those rows it is the whole content of the row. */}
      <p className="text-text-muted text-sm break-all">
        {entry.subject_kind} {entry.subject_id}
      </p>
      <p className="text-text-muted text-sm">
        Actor: <Actor id={entry.actor_user_id} users={props.users} />
      </p>
      <p className="text-text-muted text-sm">
        Correlation: <Correlation id={entry.correlation_id} />
      </p>
      <Detail detail={entry.detail} />
    </li>
  )
}

/**
 * Who acted, in FOUR states because there are four answers — and the two that
 * look alike are the reason this is not a one-line lookup. A list that has not
 * arrived is not a dangling reference: rendering the raw id for both would tell
 * the reader "this account is unknown to the system" during the second before
 * the users query answers. `harnesses-section.tsx`'s `CredentialName` draws the
 * same distinction one file over, for the same reason.
 *
 * A NULL ACTOR IS NOT A CORNER CASE. Sixteen of the backend's 47 audit writers
 * pass no actor, and they include every `credential.lease_*` and
 * `credential.sync_*` row — which is one pair per phase of every run — plus
 * `run.finished`, `publish.halted`, `credential.used`, `user.bootstrapped` and
 * the failed-login pair. By volume a live deployment's log is mostly this
 * branch, so it renders as a word rather than as a dash.
 *
 * "system" is the DTO's own vocabulary (`identity/api/dto.go`: "a system
 * action — bootstrap, an expiry sweep — genuinely has no actor"). It is worth
 * knowing where it reads oddly: on `auth.login_failed` the field is null
 * because nobody was ever authenticated, so the row says a login failed for
 * `<email>` with no signed-in actor — not that the platform tried to log in.
 * The action and the subject carry that meaning; a second vocabulary here would
 * only make this screen disagree with the DTO about what null means.
 */
function Actor(props: { id: string | null; users: readonly Account[] | undefined }) {
  if (props.id === null) return <span className="text-text">system</span>
  if (props.users === undefined) return <span className="text-text-muted">…</span>
  const found = props.users.find((u) => u.id === props.id)
  // The RAW ID, never a blank and never a fallback to some other account. An
  // audit log that attributes an action to the wrong person is worse than one
  // that admits it cannot name them. Reachable rather than theoretical: this
  // page is fetched fresh while `['users']` may be up to a minute stale, so an
  // account created since the last users fetch acts on rows this screen cannot
  // yet name.
  if (found === undefined) return <code className="text-text-muted">{props.id}</code>
  return <span className="text-text">{found.email}</span>
}

/**
 * The thread back to a request, or the statement that there is not one.
 *
 * `correlation_id` IS REQUIRED AND NON-NULLABLE, and the column it comes from
 * is `text NOT NULL DEFAULT ''` (`schema.sql`) — so an absent one arrives as
 * `""` rather than as null, and `<code>{''}</code>` is an element with no text:
 * invisible on the screen and to every `getByText` that would otherwise catch
 * it (the shape Task 18 recorded on the empty gate note).
 *
 * IT IS EMPTY ON EXACTLY THE ROWS SOMEBODY IS TRACING, from two independent
 * causes. Four writers set no `CorrelationID` at all — `run.finished`
 * (`runs/orchestrator.go`, twice) and `publish.halted` (`runs/publish.go`,
 * twice), where the run EVENT written two lines above carries one and the audit
 * entry does not. And `httpx.CorrelationID` is a bare type assertion on a
 * context value planted only by an HTTP middleware, so it answers `""` for
 * every runner and background path: `credential.used`,
 * `credential.auth_expired` (reached from `app/runnerports.go`) and the whole
 * lease family carry an empty one too, even though their call sites look like
 * they set one.
 */
function Correlation(props: { id: string }) {
  if (props.id === '') return <span className="text-text-muted">not recorded</span>
  return <code className="text-text-muted font-mono text-xs break-all">{props.id}</code>
}

/**
 * The row's payload, when it has one.
 *
 * THIS ONE REALLY IS A JSON SURFACE, which is the thing `artifact-kind-views.tsx`
 * exists to avoid being. There the payloads are contract-bearing — a plan, a
 * verdict, a patch — and each has a reader and a presentation. Here the map is
 * assembled inline at 47 unrelated call sites with no shared vocabulary
 * (`{"ip"}`, `{"from","to"}`, `{"attempt","epoch"}`, `{"status","outcome"}`),
 * and the exact bytes are what an operator is reconstructing an incident from.
 * A key/value renderer would also flatten the types this surface is read for:
 * `"5"` and `5` are the same line once they are formatted as prose.
 *
 * COLLAPSED, AND ABSENT WHEN EMPTY. `Record` substitutes `{}` for a nil map
 * (`platform/audit/audit.go`) and `ListPage` decodes into an initialized one,
 * so `{}` is what every writer that passes no `Detail` produces — a third of
 * them. An unconditional disclosure puts a triangle on most lines of the screen,
 * each opening onto two characters.
 */
function Detail(props: { detail: Record<string, unknown> | null }) {
  const detail = props.detail
  if (detail === null || Object.keys(detail).length === 0) return null
  return (
    <details>
      <summary className="text-text-muted flex min-h-11 items-center text-sm">Detail</summary>
      <pre className="text-text-muted overflow-x-auto font-mono text-xs">
        {JSON.stringify(detail, null, 2)}
      </pre>
    </details>
  )
}
