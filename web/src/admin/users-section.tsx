import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { asApiError, ErrorSurface } from '../errors/error-surface'
import { FormError } from '../forms/form-error'
import { SelectField } from '../forms/select-field'
import { TextField } from '../forms/text-field'
import { useAppForm } from '../forms/use-app-form'
import { FormSheet } from '../ui/form-sheet'
import { usersQueryOptions } from './users-query'

type User = components['schemas']['ApiUserResponse']

/**
 * The two account roles, LEAST PRIVILEGE FIRST.
 *
 * The generated types carry no vocabulary to read this off: `role` is a bare
 * `string` on `ApiUserResponse` and on `ContractsIdAndUpdateUser`, because the
 * Go field's underlying type is `string` and the emitter has no enum to emit —
 * the same shape `GRANT_LEVELS` found for `level`, and the same reason
 * "check it against `schema.d.ts`" would confirm nothing. The source is
 * `backend/internal/platform/authn/authn.go`: `Role.Valid()` is
 * `admin || member`, exactly two.
 *
 * UNLIKE `GRANT_LEVELS` THIS ORDER IS NOT LOAD-BEARING. That array's index is
 * the rank `AtLeast` compares, so reordering it moved a privilege boundary;
 * roles have no ladder and nothing here indexes this list. What the order buys
 * is that the first option is `member`, which is also the create form's
 * default — so an unattended submit makes the least privileged account rather
 * than the most.
 */
const ROLES = ['member', 'admin'] as const

type Role = (typeof ROLES)[number]

/**
 * A refusal from an admin endpoint, said in the server's own words.
 *
 * IT DELIBERATELY DOES NOT GO THROUGH `ApiErrorPanel`, and that is a
 * correctness point rather than a styling preference. That panel answers every
 * 403 with "You do not have access to this project. Ask an admin for a grant."
 * — true on the project screens it was written for, and FALSE three times over
 * here. This screen produces three refusals a reader can really hit and not one
 * of them is about a project grant:
 *
 *   403 "admin role required"                        — a member opened /admin
 *   403 "cannot change own role or active flag"      — the own-row guard
 *   409 "cannot remove the last active admin"        — the last-admin guard
 *
 * Telling a member to ask for a project grant when what they lack is the admin
 * ROLE sends them to ask the wrong person for the wrong thing, and telling an
 * admin the same when they have just tried to demote themselves is nonsense.
 * So the server's `detail` is rendered as written: all three sentences above
 * are already complete, actionable statements, which is precisely why the panel
 * substitutes its own on the project screens (where the raw detail is
 * "insufficient project grant" and says nothing about what to do).
 *
 * TASKS 22 AND 23 WILL WANT THIS. Harnesses, credentials and audit refuse a
 * member identically, so when the second consumer arrives this should move to
 * its own module in `admin/` rather than be imported from here — the same call
 * `grants-section.tsx` made about `usersQueryOptions`, which is why that query
 * had a clean home to be promoted to today.
 *
 * The no-status arm delegates to `ErrorSurface`, which owns the one sentence
 * for a request that never reached the server. A second copy of that wording is
 * how two screens come to disagree about what a dropped connection looks like.
 */
function AdminRefusal(props: { error: unknown }) {
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

/**
 * Accounts and their roles — the account-level half of authz. The project-level
 * half is grants, and it lives on the project detail because the API is
 * project-nested (spec §4/§5).
 *
 * THERE IS NO PASSWORD-RESET CONTROL AND THAT IS THE BACKEND'S SHAPE, not an
 * omission. The only password write is `PUT /auth/password`
 * (`changePassword`), which is `authenticated` rather than `adminOnly` and
 * takes the CURRENT password — it changes your own and cannot touch anybody
 * else's. An admin here has no endpoint that resets a member's password, so
 * offering the control would promise something no request can carry out. The
 * create form's password field is the one place a password is set from this
 * screen, because creating an account is the one operation that takes one.
 */
export function UsersSection() {
  const users = useQuery(usersQueryOptions)
  const [adding, setAdding] = useState(false)

  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-text font-medium">Users</h2>
      {users.isError ? (
        <AdminRefusal error={users.error} />
      ) : users.data === undefined ? (
        <p className="text-text-muted text-sm">Reading users…</p>
      ) : (
        /* NO EMPTY STATE, and it is not an oversight. `GET /users` returns
           every account and is admin-only, so whoever is reading this list is
           IN it — an empty answer is not a state the server can produce, and a
           "create the first user" sentence would be text no reader can ever
           see. The `?? []` in the query still earns its keep: the contract
           marks `users` nullable, and `test/render-app.tsx`'s `users: null`
           default is what puts that coalesce on a live path. */
        <ul data-testid="user-rows" className="flex flex-col gap-2">
          {users.data.map((user) => (
            <UserRow key={user.id} user={user} />
          ))}
        </ul>
      )}
      <button
        type="button"
        onClick={() => setAdding(true)}
        className="border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium"
      >
        Add user
      </button>
      <FormSheet
        open={adding}
        onOpenChange={setAdding}
        title="Add user"
        description="Create an account with a starting password and a role. They can change the password themselves once they sign in."
      >
        {/* MOUNTED ONLY WHILE THE SHEET IS OPEN, the shape every other sheet in
            the tree uses: the form's state starts clean each time it is opened
            rather than holding the last attempt's half-filled fields. */}
        {adding && <CreateUserForm onDone={() => setAdding(false)} />}
      </FormSheet>
    </section>
  )
}

/**
 * One account, with the two edits `PATCH /users/{id}` accepts.
 *
 * THE CLIENT DOES NOT REPLICATE THE SERVER'S TWO GUARDS, and that is a decision
 * rather than laziness. `UpdateUser` refuses two things: changing your OWN role
 * or active flag (403), and demoting or deactivating the LAST ACTIVE ADMIN
 * (409, computed inside a transaction holding an advisory lock over a
 * `CountActiveAdmins`). The second is not a question this component can answer
 * — it depends on a count under a lock, and two admins pressing Deactivate at
 * the same moment is exactly the race that lock exists for. So disabling the
 * controls it CAN predict would teach the reader that this screen prevents
 * refusals, and then the one it cannot predict arrives as a surprise on a
 * control that looked live. Both refusals are surfaced from the server
 * instead, in the server's words, and the screen makes one promise it can
 * keep: it tells you what the server said.
 */
function UserRow(props: { user: User }) {
  const queryClient = useQueryClient()

  const update = useMutation({
    mutationFn: async (body: { role?: string; active?: boolean }) =>
      unwrap(
        await api.PATCH('/api/v1/users/{id}', {
          params: { path: { id: props.user.id } },
          body,
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['users'] }),
  })

  const roleFieldId = `role-${props.user.id}`

  return (
    <li
      data-testid={`user-${props.user.id}`}
      className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="text-text grow font-medium">{props.user.email}</span>
        {/* THE ACTIVE FLAG IS RENDERED FOR BOTH VALUES. "Active" is not the
            absence of a badge: a list that marked only the deactivated rows
            reads identically to a list whose flag never arrived. */}
        <span className="text-text-muted font-mono text-xs">
          {props.user.active ? 'active' : 'deactivated'}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {/* LABELLED WITH THE EMAIL, like the grant list's Remove and the repo
            list's row buttons: a control repeated once per row has to say which
            row it belongs to, both for a screen reader and for a test that
            wants one person's control without counting siblings. */}
        <label htmlFor={roleFieldId} className="text-text-muted text-sm">
          Role for {props.user.email}
        </label>
        <select
          id={roleFieldId}
          value={props.user.role}
          disabled={update.isPending}
          onChange={(e) => update.mutate({ role: e.target.value })}
          className="border-border bg-ground text-text rounded-md border px-2 py-2 text-sm"
        >
          {/* THE SERVER'S ROLE MAY NOT BE ONE OF OURS. A role this client has
              not learned yet would otherwise select nothing and silently read
              as `member` — the first option — so it is rendered as itself and
              the select tells the truth about the account. */}
          {ROLES.includes(props.user.role as Role) ? null : (
            <option value={props.user.role}>{props.user.role}</option>
          )}
          {ROLES.map((role) => (
            <option key={role} value={role}>
              {role}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={() => update.mutate({ active: !props.user.active })}
          disabled={update.isPending}
          className="border-border text-text rounded-md border px-3 py-2 text-sm font-medium"
        >
          {props.user.active ? 'Deactivate' : 'Reactivate'} {props.user.email}
        </button>
      </div>
      {/* REACTIVATE IS OFFERED, THOUGH THE PLAN ASKED ONLY FOR DEACTIVATE.
          `active` is a boolean on one PATCH and the row renders both of its
          values, so a screen that could only set it false would show a
          deactivated account beside a control that cannot bring it back — a
          dead end reachable in one click, and the only other door is SQL. */}
      {update.isError && <AdminRefusal error={update.error} />}
    </li>
  )
}

/**
 * `email` and `password` are checked here only for the shapes the server states
 * (`validateEmail` via `mail.ParseAddress`; `MinPasswordLen`/`MaxPasswordLen` =
 * 12 and 512 bytes, `identity/password.go`). The client is the convenience and
 * the server is the authority: a field error it sends back is routed onto the
 * same field by `applyProblemToForm`, so the two agree on where a message
 * lands.
 *
 * `.min(12)` COUNTS UTF-16 CODE UNITS AND THE SERVER COUNTS BYTES, so the two
 * bounds are not the same measurement — a 12-character passphrase of non-ASCII
 * text passes here and could still be refused there, and an emoji-laden one
 * could pass the byte floor while failing this. The mismatch is safe in this
 * direction: both are floors on the same value, whichever refuses first says
 * so on the password field, and the server's answer is the one that decides.
 */
const schema = z.object({
  email: z.email('enter a valid email address'),
  password: z.string().min(12, 'use at least 12 characters'),
  role: z.enum(ROLES),
})

function CreateUserForm(props: { onDone: () => void }) {
  const queryClient = useQueryClient()

  const form = useAppForm({
    schema,
    defaultValues: { email: '', password: '', role: 'member' as Role },
    onSubmit: async (values) => {
      await unwrap(await api.POST('/api/v1/users', { body: values }))
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      props.onDone()
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextField name="email" label="Email" type="email" autoComplete="username" />
        {/* `new-password`, not `current-password`: this is an admin typing
            somebody else's starting secret, and the browser must not offer to
            fill it with the admin's own saved credential. */}
        <TextField
          name="password"
          label="Starting password"
          type="password"
          autoComplete="new-password"
        />
        <SelectField
          name="role"
          label="Role"
          placeholder="Pick a role"
          options={ROLES.map((role) => ({ value: role, label: role }))}
        />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Create user
        </button>
      </form>
    </FormProvider>
  )
}
