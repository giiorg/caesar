import { asApiError, ErrorSurface } from '../errors/error-surface'

/**
 * A refusal from an admin endpoint, said in the server's own words.
 *
 * PROMOTED HERE OUT OF `users-section.tsx`, which is where Task 21 wrote it and
 * said this task would move it. Every section of this screen is admin-only, so
 * all four refuse a member identically and the component belongs to the screen
 * rather than to whichever section needed it first — the same call
 * `grants-section.tsx` made about `usersQueryOptions`.
 *
 * IT DELIBERATELY DOES NOT GO THROUGH `ApiErrorPanel`, and that is a
 * correctness point rather than a styling preference. That panel answers every
 * 403 with "You do not have access to this project. Ask an admin for a grant."
 * — true on the project screens it was written for, and false on every refusal
 * this screen can produce. None of them is about a project grant:
 *
 *   403 "admin role required"                       — a member opened /admin
 *   403 "cannot change own role or active flag"     — the own-row guard
 *   409 "cannot remove the last active admin"       — the last-admin guard
 *   409 "harness name already taken"                — registration
 *   409 "credential is referenced by a repo or harness"  — a blocked delete
 *   409 "an active credential already holds this refresh token …"
 *
 * Telling a member to ask for a project grant when what they lack is the admin
 * ROLE sends them to ask the wrong person for the wrong thing, and answering a
 * blocked credential delete with it is nonsense. So the server's `detail` is
 * rendered as written: every sentence above is already a complete, actionable
 * statement, which is precisely why the panel substitutes its own on the
 * project screens (where the raw detail is "insufficient project grant" and
 * says nothing about what to do).
 *
 * THE VERBATIM RULE HAS TEETH ON THE DELETE REFUSAL, and it is worth naming
 * where the rendering lives. `DeleteCredential` answers "credential is
 * referenced by a repo or harness" (`assets/credentials.go`), but FOUR tables
 * restrict that delete: `repos` and `harnesses`, and also `credential_leases`
 * and `publish_intents`. The sentence undercounts its own causes. Widening it
 * is a backend change; paraphrasing it here would be worse, because the screen
 * would then state a cause the server did not find. Rendering `detail` as
 * written keeps the client out of the business of guessing why.
 *
 * The no-status arm delegates to `ErrorSurface`, which owns the one sentence
 * for a request that never reached the server. A second copy of that wording is
 * how two screens come to disagree about what a dropped connection looks like.
 */
export function AdminRefusal(props: { error: unknown }) {
  const failure = asApiError(props.error)
  if (failure === null) return <ErrorSurface failure={null} />
  return (
    <section role="alert" className="bg-surface text-text border-border rounded-md border p-4">
      <h3 className="font-medium">{failure.title}</h3>
      <p>{failure.detail !== '' ? failure.detail : 'The request was refused.'}</p>
      {/* UNCONDITIONAL, and last, exactly as `ApiErrorPanel` has it: whatever
          else the surface says, this is the string support asks for. */}
      <p>
        Reference: <code>{failure.correlationId}</code>
      </p>
    </section>
  )
}
