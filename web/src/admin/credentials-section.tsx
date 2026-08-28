import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { FormError } from '../forms/form-error'
import { SelectField } from '../forms/select-field'
import { TextField } from '../forms/text-field'
import { TextAreaField } from '../forms/textarea-field'
import { useAppForm } from '../forms/use-app-form'
import { FormSheet } from '../ui/form-sheet'
import { AdminRefusal } from './admin-refusal'
import { type Credential, credentialsQueryOptions } from './credentials-query'

/**
 * The four credential kinds, in the server's own declaration order
 * (`assets/assets.go`). There is no vocabulary in the generated types to read
 * this off — `kind` is a bare `string` on `ApiCredentialResponse` and carries
 * no enum, because the Go field's underlying type is `string`.
 *
 * ORDER IS PRESENTATION ONLY; nothing compares two kinds. It is the server's
 * order rather than a prettier one so that a kind added there and copied here
 * lands in the same place in both files.
 */
const CREDENTIAL_KINDS = ['forge_token', 'model_key', 'subscription_token', 'agent_auth_file']

/** The one kind that carries a fingerprint (`assets/credentials.go`). */
const FINGERPRINTED_KIND = 'agent_auth_file'

/**
 * Credentials, and the freshness an operator reads to decide whether one needs
 * re-importing.
 *
 * THE SECRET IS NEVER ON THIS SCREEN AND CANNOT BE. `CredentialResponse` is
 * metadata only — the plaintext never leaves the control plane — so there is no
 * field here to leak. What this screen must not do is echo what was TYPED, and
 * that is the property the tests assert after a create and after a rotation:
 * both forms are mounted only while their sheet is open, so a submitted secret
 * leaves the document with the sheet rather than sitting in a control nobody
 * closed.
 */
export function CredentialsSection() {
  const credentials = useQuery(credentialsQueryOptions)
  const [adding, setAdding] = useState(false)

  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-text font-medium">Credentials</h2>
      {credentials.isError ? (
        <AdminRefusal error={credentials.error} />
      ) : credentials.data === undefined ? (
        <p className="text-text-muted text-sm">Reading credentials…</p>
      ) : credentials.data.length === 0 ? (
        <p className="text-text-muted text-sm">
          No credentials are stored yet — repos and harnesses each need one before they can
          authenticate.
        </p>
      ) : (
        <ul data-testid="credential-rows" className="flex flex-col gap-2">
          {credentials.data.map((credential) => (
            <CredentialRow key={credential.id} credential={credential} />
          ))}
        </ul>
      )}
      <button
        type="button"
        onClick={() => setAdding(true)}
        className="border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium"
      >
        Add credential
      </button>
      <FormSheet
        open={adding}
        onOpenChange={setAdding}
        title="Add credential"
        description="The secret is encrypted on arrival and never displayed again. Rotating replaces it; there is no way to read it back."
      >
        {adding && <CreateCredentialForm onDone={() => setAdding(false)} />}
      </FormSheet>
    </section>
  )
}

/**
 * WHETHER AN EXPIRY IS STILL LIVE IS A COMPARISON, NOT A NULL CHECK — and
 * getting this wrong is how the screen would nag an operator forever about work
 * they had already done.
 *
 * Nothing ever clears `last_auth_expired_at`. `MarkCredentialAuthExpired` is
 * its only writer (`assets/store/queries.sql`), and the rotation this very
 * screen performs — `UpdateCredentialSecret` — advances `secret_version` and
 * `updated_at` while leaving the expiry stamp exactly where it was. So a row
 * that expired once would, on a plain `!== null` test, keep telling every
 * future admin to rotate a secret that was rotated weeks ago.
 *
 * `provided_at` is what settles it, and the backend put it on the wire for this
 * purpose: `assets/api/dto.go` assigns it from `UpdatedAt` and says so — "when
 * the operator last supplied a secret value; together with the two freshness
 * stamps it is what an operator reads to decide whether a subscription
 * credential needs re-importing". Re-wrapping under a new master key
 * deliberately does NOT touch it (`RewrapCredential`: "the plaintext is
 * unchanged, so secret_version, fingerprint, and updated_at … all stay put"),
 * so it really does mean *last new plaintext* rather than *last row write*.
 *
 *   expired > provided  →  the auth died after the current secret was supplied.
 *                          Still broken. Say so, and say what to do.
 *   expired <= provided →  a new secret has been supplied since. The expiry is
 *                          history: worth showing, not worth acting on.
 *
 * AN UNPARSEABLE STAMP FALLS TO `live`, not to `resolved`. `Date.parse` answers
 * NaN and every comparison against NaN is false, so the arithmetic's own
 * failure mode is to quietly claim the problem is fixed — the one direction
 * this must not fail in. A credential whose auth has died and whose row says
 * nothing is invisible until a run fails.
 */
type ExpiryState = 'never' | 'live' | 'resolved'

export function expiryState(credential: Credential): ExpiryState {
  const expired = credential.last_auth_expired_at
  if (expired === null) return 'never'
  const at = Date.parse(expired)
  const provided = Date.parse(credential.provided_at)
  if (Number.isNaN(at) || Number.isNaN(provided)) return 'live'
  return at > provided ? 'live' : 'resolved'
}

function CredentialRow(props: { credential: Credential }) {
  const queryClient = useQueryClient()
  const [rotating, setRotating] = useState(false)
  const credential = props.credential

  const remove = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE('/api/v1/credentials/{id}', {
          params: { path: { id: credential.id } },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['credentials'] }),
  })

  return (
    <li
      data-testid={`credential-${credential.id}`}
      className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-text grow font-medium">{credential.name}</span>
        <span className="text-text-muted font-mono text-xs">{credential.kind}</span>
      </div>
      <Fingerprint credential={credential} />
      <p className="text-text-muted text-sm">
        {/* THE VERSION IS RENDERED FOR EVERY ROW, INCLUDING VERSION 1. It is
            what a consumer holding stale bytes compares against without either
            side seeing a secret (`queries.sql`), so it is the number an
            operator quotes when a lease looks wrong — and a field shown only
            after the first rotation would be missing exactly when someone is
            trying to work out whether a rotation happened. */}
        Secret version {credential.secret_version} · supplied{' '}
        <time dateTime={credential.provided_at}>{credential.provided_at}</time>
      </p>
      <p className="text-text-muted text-sm">
        {/* "never used" IS RENDERED, not omitted. A row with no `last_used_at`
            and no line reads identically to a row whose line failed to render,
            and "this credential has never been used" is a real answer an
            operator acts on — it is the signature of one that was stored
            against the wrong repo. */}
        {credential.last_used_at === null ? (
          'Never used'
        ) : (
          <>
            Last used <time dateTime={credential.last_used_at}>{credential.last_used_at}</time>
          </>
        )}
      </p>
      <AuthExpiry credential={credential} />
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => setRotating(true)}
          className="border-border text-text rounded-md border px-3 py-2 text-sm font-medium"
        >
          Rotate {credential.name}
        </button>
        <button
          type="button"
          onClick={() => remove.mutate()}
          disabled={remove.isPending}
          className="border-border text-danger rounded-md border px-3 py-2 text-sm font-medium"
        >
          Delete {credential.name}
        </button>
      </div>
      {/* THE 409 IS THE SERVER'S SENTENCE, VERBATIM — see `AdminRefusal` for why
          this screen does not route refusals through `ApiErrorPanel`, and for
          why the delete refusal in particular must not be paraphrased. */}
      {remove.isError && <AdminRefusal error={remove.error} />}
      <FormSheet
        open={rotating}
        onOpenChange={setRotating}
        title={`Rotate ${credential.name}`}
        description="Replace the stored secret. The version advances, and anything holding the old bytes can tell they are stale."
      >
        {rotating && <RotateSecretForm credential={credential} onDone={() => setRotating(false)} />}
      </FormSheet>
    </li>
  )
}

/**
 * THE `n/a` IS THE POINT OF THIS COMPONENT. A fingerprint is stored for
 * `agent_auth_file` alone — it feeds the partial unique index that gives one
 * refresh token exactly one row (`assets/credentials.go`) — and every other
 * kind stores NULL. Rendering nothing for those three would make "this kind has
 * no fingerprint" indistinguishable from "the fingerprint failed to load", so
 * the row says which it is and why.
 *
 * The two nulls are told apart by KIND, not by the null itself: a null on an
 * agent auth file is not a kind without fingerprints, it is a row that should
 * have one — a state the contract permits and the code path does not produce,
 * which is exactly the kind of thing a screen should show rather than smooth
 * over.
 */
function Fingerprint(props: { credential: Credential }) {
  const { kind, fingerprint } = props.credential
  if (kind !== FINGERPRINTED_KIND) {
    return (
      <p className="text-text-muted text-sm">Fingerprint: n/a — only agent auth files carry one</p>
    )
  }
  if (fingerprint === null) {
    return <p className="text-text-muted text-sm">Fingerprint: missing</p>
  }
  return (
    <p className="text-text-muted text-sm">
      Fingerprint: <code className="break-all">{fingerprint}</code>
    </p>
  )
}

/**
 * `last_auth_expired_at` gets its face — the column the expired-token finding
 * was invisible without.
 *
 * The two non-empty states say different things because they call for different
 * actions, and `expiryState` above holds the whole argument for the split. The
 * resolved state still shows the timestamp: an auth that has died once is
 * history worth reading when it dies again.
 */
function AuthExpiry(props: { credential: Credential }) {
  const expired = props.credential.last_auth_expired_at
  const state = expiryState(props.credential)
  if (expired === null || state === 'never') return null
  const at = <time dateTime={expired}>{expired}</time>
  if (state === 'live') {
    return (
      <p role="status" className="text-danger text-sm">
        Auth expired {at} — rotate the secret.
      </p>
    )
  }
  return (
    <p className="text-text-muted text-sm">Auth expired {at}; a secret has been supplied since.</p>
  )
}

/**
 * The secret is a TEXTAREA on both forms, and that is about `agent_auth_file`.
 * A Codex `auth.json` is a multi-line JSON document with a 64 KiB bound, pasted
 * whole; a single-line input would hold it, but it would show one line of it.
 * The other three kinds are single tokens, which a textarea holds perfectly
 * well — one control that fits every kind beats a control that switches shape
 * on a select and is right about which shape only when this client's idea of
 * the kinds is complete.
 *
 * NOTHING VALIDATES THE SECRET'S SHAPE HERE. The server's rules are per-kind
 * and real (`validateSecret`: a size bound that differs by kind, and for an
 * auth file, JSON that yields `tokens.refresh_token`) — but a client that
 * re-implemented them would be parsing a secret in order to complain about it,
 * and would then have to be right about a file format the CLI owns and revises.
 * The one thing worth refusing locally is empty, which needs no knowledge of
 * the contents. Everything else comes back as a field error on `secret`.
 */
const secretField = z.string().min(1, 'paste the secret')

const createSchema = z.object({
  name: z.string().min(1, 'give the credential a name'),
  kind: z.enum(CREDENTIAL_KINDS),
  secret: secretField,
})

function CreateCredentialForm(props: { onDone: () => void }) {
  const queryClient = useQueryClient()

  const form = useAppForm({
    schema: createSchema,
    defaultValues: { name: '', kind: '', secret: '' },
    onSubmit: async (values) => {
      await unwrap(
        await api.POST('/api/v1/credentials', {
          body: { name: values.name, kind: values.kind, secret: values.secret },
        }),
      )
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      props.onDone()
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextField name="name" label="Name" />
        {/* NO DEFAULT KIND. Unlike the user form's `member`, there is no
            least-privilege answer here and the four kinds are not
            interchangeable — an unattended submit that picked one would store a
            secret under a kind nobody chose, and kind is immutable afterwards.
            The empty placeholder is a value the schema rejects. */}
        <SelectField
          name="kind"
          label="Kind"
          placeholder="Pick a kind"
          options={CREDENTIAL_KINDS.map((k) => ({ value: k, label: k }))}
        />
        <TextAreaField name="secret" label="Secret" rows={5} />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Store credential
        </button>
      </form>
    </FormProvider>
  )
}

const rotateSchema = z.object({ secret: secretField })

/**
 * `PUT /credentials/{id}/secret` — the only write that replaces stored
 * plaintext from this screen.
 *
 * THE KIND IS NOT ASKED FOR AND MUST NOT BE. Kind is immutable, and the server
 * reads the row's own kind in-tx to decide how the new bytes are validated and
 * fingerprinted (`RotateCredentialSecret`). A kind control here would be a
 * field the request has no room for.
 */
function RotateSecretForm(props: { credential: Credential; onDone: () => void }) {
  const queryClient = useQueryClient()

  const form = useAppForm({
    schema: rotateSchema,
    defaultValues: { secret: '' },
    onSubmit: async (values) => {
      await unwrap(
        await api.PUT('/api/v1/credentials/{id}/secret', {
          params: { path: { id: props.credential.id } },
          body: { secret: values.secret },
        }),
      )
      await queryClient.invalidateQueries({ queryKey: ['credentials'] })
      props.onDone()
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextAreaField name="secret" label={`New secret (${props.credential.kind})`} rows={5} />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Replace secret
        </button>
      </form>
    </FormProvider>
  )
}
