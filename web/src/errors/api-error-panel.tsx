import { ApiError } from '../api/errors'

/**
 * The route-level error surface, and the only place `correlation_id` reaches a
 * user who did not submit a form. Every route that reads data through Suspense
 * has no error path of its own, so without this its failures reach TanStack's
 * default error component: generic text, no correlation id, and for a grant
 * failure no explanation of what the user is missing. The thread between a
 * user's complaint and a server log line breaks precisely where the user is
 * most likely to complain.
 *
 * IT LIVES IN `errors/`, NOT `ui/`. It does `error instanceof ApiError`, which
 * is a VALUE import from `api/`, and Task 3's matrix bans `ui/ -> api/` — `ui`
 * is the presentational layer, and a component that knows the shape of a
 * transport error is not presentational. `errors/` carries no inbound
 * restriction and is where the next error surface belongs too. Getting this
 * wrong is not a lint annoyance: it is the edge that makes every button in
 * `ui/` untestable without a client.
 */
export function ApiErrorPanel({ error }: { error: unknown }) {
  if (!(error instanceof ApiError)) throw error // not ours; let the root handle it

  return (
    <section role="alert" className="bg-surface text-text border-border rounded-md border p-4">
      <h2 className="font-medium">{error.title}</h2>
      {error.status === 403 ? (
        // A 403 is not a malfunction and must not read like one. The user is
        // authenticated and simply lacks a grant, so the panel says what to ask
        // for rather than inviting a retry that cannot succeed.
        <p>You do not have access to this project. Ask an admin for a grant.</p>
      ) : (
        <p>{error.detail}</p>
      )}
      {/* UNCONDITIONAL, and last. Whatever else the panel says, this is the
          string support asks for, and it is on the 500 path too — where
          `detail` is deliberately vague and the id is the only thing that can
          find the log line. */}
      <p>
        Reference: <code>{error.correlationId}</code>
      </p>
    </section>
  )
}
