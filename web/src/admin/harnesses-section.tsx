import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { FormError } from '../forms/form-error'
import { SelectField, type SelectOption } from '../forms/select-field'
import { TextField } from '../forms/text-field'
import { TextAreaField } from '../forms/textarea-field'
import { useAppForm } from '../forms/use-app-form'
import { FormSheet } from '../ui/form-sheet'
import { AdminRefusal } from './admin-refusal'
import { type Credential, credentialsQueryOptions } from './credentials-query'
import {
  asHarnessKind,
  HARNESS_CREDENTIAL_KINDS,
  HARNESS_KINDS,
  type Harness,
  type HarnessKind,
  harnessesQueryOptions,
} from './harnesses-query'

/**
 * The agent registry — which container image runs a phase, against which
 * provider endpoint, holding which credential.
 *
 * IT READS TWO LISTS AND ONLY ONE OF THEM IS ITS OWN. A harness carries a
 * `credential_id` and nothing else about its credential, so rendering "which
 * credential" at all means joining against the credential list. That join is
 * done here rather than asked of the server because both lists are already on
 * this screen — the Credentials section next door reads the same query — so the
 * join costs no request, and a `GET /harnesses` that embedded credential names
 * would be a backend change made to save a lookup the client already has.
 */
export function HarnessesSection() {
  const harnesses = useQuery(harnessesQueryOptions)
  const credentials = useQuery(credentialsQueryOptions)
  const [adding, setAdding] = useState(false)

  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-text font-medium">Harnesses</h2>
      {harnesses.isError ? (
        <AdminRefusal error={harnesses.error} />
      ) : harnesses.data === undefined ? (
        <p className="text-text-muted text-sm">Reading harnesses…</p>
      ) : harnesses.data.length === 0 ? (
        /* AN EMPTY STATE THAT EXISTS, unlike the Users list's. A deployment
           with no harness registered is the state every deployment starts in,
           and it is the one that has to say what to do about it. */
        <p className="text-text-muted text-sm">
          No harnesses are registered yet — a run has nothing to execute in until one is.
        </p>
      ) : (
        <ul data-testid="harness-rows" className="flex flex-col gap-2">
          {harnesses.data.map((harness) => (
            <HarnessRow key={harness.id} harness={harness} credentials={credentials.data} />
          ))}
        </ul>
      )}
      <button
        type="button"
        onClick={() => setAdding(true)}
        className="border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium"
      >
        Register harness
      </button>
      <FormSheet
        open={adding}
        onOpenChange={setAdding}
        title="Register harness"
        description="Name the image and the provider endpoint a phase runs against, and bind the credential it authenticates with."
      >
        {adding && <HarnessForm onDone={() => setAdding(false)} />}
      </FormSheet>
    </section>
  )
}

/**
 * One harness. `credentials` is the list to resolve `credential_id` against and
 * is `undefined` while that query is still in flight or has failed — a state
 * this row states rather than papers over, because "which credential" is the
 * field an operator is here to check.
 */
function HarnessRow(props: { harness: Harness; credentials: Credential[] | undefined }) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)

  const remove = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE('/api/v1/harnesses/{id}', {
          params: { path: { id: props.harness.id } },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['harnesses'] }),
  })

  return (
    <li
      data-testid={`harness-${props.harness.id}`}
      className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-text grow font-medium">{props.harness.name}</span>
        <span className="text-text-muted font-mono text-xs">{props.harness.kind}</span>
      </div>
      <p className="text-text-muted font-mono text-xs break-all">{props.harness.image_ref}</p>
      <p className="text-text-muted text-sm">{props.harness.provider_endpoint}</p>
      <p className="text-text-muted text-sm">
        Credential: <CredentialName id={props.harness.credential_id} of={props.credentials} />
      </p>
      <div className="flex flex-wrap gap-2">
        {/* NAMED WITH THE HARNESS, like every other per-row control in the tree:
            a button repeated once per row has to say which row it belongs to,
            for a screen reader and for a test that wants one row's control
            without counting siblings. */}
        <button
          type="button"
          onClick={() => setEditing(true)}
          className="border-border text-text rounded-md border px-3 py-2 text-sm font-medium"
        >
          Edit {props.harness.name}
        </button>
        <button
          type="button"
          onClick={() => remove.mutate()}
          disabled={remove.isPending}
          className="border-border text-danger rounded-md border px-3 py-2 text-sm font-medium"
        >
          Delete {props.harness.name}
        </button>
      </div>
      {/* THERE IS NO 409 ARM HERE AND THAT IS THE SCHEMA'S SHAPE, not an
          omission. `DeleteCredential` classifies a foreign-key refusal into a
          409 because four tables restrict that delete; NOTHING in the schema
          references `harnesses`, so `DeleteHarness` (`assets/harnesses.go`)
          classifies only the missing row. A harness delete cannot be refused
          for being in use, so this surface renders whatever the server did say
          rather than a sentence about references that no request can produce. */}
      {remove.isError && <AdminRefusal error={remove.error} />}
      <FormSheet
        open={editing}
        onOpenChange={setEditing}
        title={`Edit ${props.harness.name}`}
        description="Kind is fixed at registration. Everything else can be changed."
      >
        {editing && <HarnessForm harness={props.harness} onDone={() => setEditing(false)} />}
      </FormSheet>
    </li>
  )
}

/**
 * The join, with its two honest misses spelled out. A credential the list does
 * not contain is not the same thing as a list that has not arrived, and neither
 * is the same as a name — so all three render differently. Falling back to the
 * raw id in both miss cases would make a still-loading row indistinguishable
 * from a dangling reference.
 */
function CredentialName(props: { id: string; of: Credential[] | undefined }) {
  if (props.of === undefined) return <span className="text-text-muted">…</span>
  const found = props.of.find((c) => c.id === props.id)
  if (found === undefined) {
    return <code className="text-text-muted">{props.id}</code>
  }
  return <span className="text-text">{found.name}</span>
}

/**
 * `defaults` IS TYPED AS TEXT AND VALIDATED AS A JSON OBJECT, in that order.
 *
 * The wire field is a `map[string]any` (`assets/harnesses.go`'s
 * `marshalDefaults`), so three things can go wrong and only two of them are
 * caught by `JSON.parse` alone: text that is not JSON at all, and text that IS
 * valid JSON but not an object — `[1,2]` and `"x"` both parse and then fail
 * Go's decode at the transport layer, where the message is about a malformed
 * body rather than about this field. So the refinement checks the SHAPE too and
 * the error lands under the textarea.
 *
 * The 4096-byte serialization cap (`maxDefaultsLen`) is deliberately NOT
 * mirrored: the server measures the bytes it will store, this form holds the
 * bytes someone typed, and the two differ by whitespace — a pretty-printed
 * object that this form rejected could be well under the cap once marshaled.
 * The server's answer routes onto the same field anyway.
 */
const defaultsField = z.string().superRefine((text, ctx) => {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    ctx.addIssue({ code: 'custom', message: 'defaults must be valid JSON' })
    return
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    ctx.addIssue({ code: 'custom', message: 'defaults must be a JSON object, like {}' })
  }
})

/**
 * The client's half of the server's rules (`assets/harnesses.go`,
 * `assets/assets.go`): a name of 1–100 bytes, an image ref of 1–300, and an
 * endpoint the server pins to https — with http allowed for localhost only,
 * which is the dev case and is why the check below is not a bare `startsWith`.
 *
 * The byte bounds are NOT restated here. `.min(1)` is the only half of each
 * that this form can measure honestly: zod counts UTF-16 code units and the
 * server counts bytes, so a client-side maximum would be a different
 * measurement wearing the same number — the mismatch `users-section.tsx`
 * records about the password floor, in the direction where it would refuse text
 * the server accepts. The ceiling stays the server's and its answer routes onto
 * the field.
 */
const schema = z.object({
  name: z.string().min(1, 'give the harness a name'),
  kind: z.enum(HARNESS_KINDS),
  image_ref: z.string().min(1, 'name the container image'),
  provider_endpoint: z
    .string()
    .min(1, 'give the provider endpoint')
    .refine(isAllowedEndpoint, 'must be an https URL (http allowed for localhost only)'),
  credential_id: z.string().min(1, 'pick a credential'),
  defaults: defaultsField,
})

/**
 * `validateEndpoint` in TypeScript. The server parses the URL and then switches
 * on the scheme, so a string that does not parse is refused before the scheme
 * is looked at — `URL` throwing is that same first arm, and it is why this is
 * not a regex.
 */
function isAllowedEndpoint(value: string): boolean {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return false
  }
  if (url.protocol === 'https:') return true
  return url.protocol === 'http:' && (url.hostname === 'localhost' || url.hostname === '127.0.0.1')
}

/**
 * Register a harness, or edit one that is already registered.
 *
 * KIND IS THE PIVOT AND ON AN EDIT IT CANNOT MOVE. `HarnessUpdate` carries no
 * kind at all (`ContractsIdAndUpdateHarness` in the generated schema, and
 * `assets/harnesses.go`: "Kind is immutable — register a new harness instead"),
 * so on the edit form it is displayed as static text rather than as a disabled
 * select — the distinction `repo-form.tsx` draws about `forge`: a disabled
 * control reads as one that is unavailable *right now*, something a permission
 * or another field might unlock, and this one never will.
 *
 * That immutability is also what makes the credential picker's filter
 * well-defined on an edit: the row's own kind picks the set, which is exactly
 * what the server does (`harnesses.go`, on `upd.CredentialID`).
 */
function HarnessForm(props: { harness?: Harness; onDone: () => void }) {
  const queryClient = useQueryClient()
  const credentials = useQuery(credentialsQueryOptions)
  const existing = props.harness

  const form = useAppForm({
    schema,
    defaultValues: {
      name: existing?.name ?? '',
      // A REGISTERED HARNESS MAY CARRY A KIND THIS BUILD DOES NOT KNOW, and the
      // schema's enum would then reject the form's own starting value. Falling
      // back to the first kind would silently retype the row — but nothing here
      // SENDS kind on an edit, so the fallback only ever decides which
      // credential kinds the picker offers, and the row's real kind is shown
      // beside it as text. A create starts at claude-code, which is a choice
      // the user then makes explicitly.
      kind: asHarnessKind(existing?.kind) ?? HARNESS_KINDS[0],
      image_ref: existing?.image_ref ?? '',
      provider_endpoint: existing?.provider_endpoint ?? '',
      credential_id: existing?.credential_id ?? '',
      // `defaults` is required-and-nullable on the wire, and `{}` is what the
      // server stores for an absent one (`marshalDefaults`), so a null renders
      // as the empty object rather than as an empty textarea — which would
      // submit as invalid JSON on a form nobody had edited.
      defaults: JSON.stringify(existing?.defaults ?? {}, null, 2),
    },
    onSubmit: async (values) => {
      // Parsed a SECOND time rather than threaded out of the refinement. zod's
      // refinements narrow types, they do not transform, and a `z.transform`
      // here would make the form's value type diverge from the textarea's
      // string — the control would then be registered against an object.
      // Parsing twice is cheap and the schema has already proved this succeeds.
      const defaults = JSON.parse(values.defaults) as Record<string, unknown>
      if (existing === undefined) {
        await unwrap(
          await api.POST('/api/v1/harnesses', {
            body: {
              name: values.name,
              kind: values.kind,
              image_ref: values.image_ref,
              provider_endpoint: values.provider_endpoint,
              credential_id: values.credential_id,
              defaults,
            },
          }),
        )
      } else {
        await unwrap(
          await api.PATCH('/api/v1/harnesses/{id}', {
            params: { path: { id: existing.id } },
            // NO `kind` — the field is not in the request type at all, and its
            // absence here is the same fact the static display states on
            // screen. Every other field IS sent: the form shows all of them,
            // pre-filled, so each is a value the user has seen and left
            // standing (the argument `repo-form.tsx` makes at length).
            body: {
              name: values.name,
              image_ref: values.image_ref,
              provider_endpoint: values.provider_endpoint,
              credential_id: values.credential_id,
              defaults,
            },
          }),
        )
      }
      await queryClient.invalidateQueries({ queryKey: ['harnesses'] })
      props.onDone()
    },
  })

  // WATCHED, so the picker re-filters as the kind changes. On an edit the field
  // has no control, so this is a constant — the row's kind — and the filter is
  // the server's own set for that row.
  const kind = form.watch('kind')

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextField name="name" label="Name" />
        {existing === undefined ? (
          <SelectField
            name="kind"
            label="Kind"
            placeholder="Pick a kind"
            options={HARNESS_KINDS.map((k) => ({ value: k, label: k }))}
            // THE CREDENTIAL IS CLEARED WHEN THE KIND CHANGES, because the two
            // sets overlap only at `model_key`: picking codex after choosing a
            // subscription token would leave a credential in the field that is
            // no longer among the options — invisible in a native select, and
            // refused by the server on submit with a message about a field
            // whose control appears to show something valid.
            onSelect={() => form.setValue('credential_id', '')}
          />
        ) : (
          <div className="flex flex-col gap-1">
            <p className="text-text-muted text-sm">Kind</p>
            <p className="text-text">{existing.kind}</p>
          </div>
        )}
        <TextField name="image_ref" label="Image reference" />
        <TextField name="provider_endpoint" label="Provider endpoint" />
        <SelectField
          name="credential_id"
          label="Credential"
          placeholder="Pick a credential"
          options={bindableOptions(kind, credentials.data)}
        />
        <CredentialPickerNote kind={kind} credentials={credentials.data} />
        <TextAreaField name="defaults" label="Defaults (JSON)" rows={5} />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          {existing === undefined ? 'Register harness' : 'Save harness'}
        </button>
      </form>
    </FormProvider>
  )
}

/**
 * The credentials this kind of harness may bind, AND THE SERVER AGREES — which
 * is what makes the narrowing a convenience rather than a rule invented here.
 * `RegisterHarness` and `UpdateHarness` both run `requireCredentialKindIn` and
 * answer a field error on `credential_id` for anything outside the pairing, so
 * an unfiltered picker would offer credentials of which some can never be
 * accepted. The milestone walk registers a forge token and both agent
 * credentials, so the wrong kinds are present in every real deployment rather
 * than only in a fixture.
 *
 * `if (undefined)` RATHER THAN `?? []`, the spelling `repo-form.tsx` measured:
 * `undefined` is react-query saying the request has not answered, `null` is the
 * WIRE, and `credentialsQueryOptions` already normalizes the second. Written as
 * a coalesce this line would silently absorb both, and deleting the query's own
 * `?? []` would then change nothing anywhere.
 */
const bindableOptions = (
  kind: HarnessKind,
  credentials: Credential[] | undefined,
): SelectOption[] => {
  if (credentials === undefined) return []
  const bindable: readonly string[] = HARNESS_CREDENTIAL_KINDS[kind]
  return credentials
    .filter((c) => bindable.includes(c.kind))
    .map((c) => ({ value: c.id, label: `${c.name} (${c.kind})` }))
}

/**
 * What the picker cannot offer, said out loud — `repo-form.tsx`'s argument with
 * one arm dropped and one added.
 *
 * THE 403 ARM IS GONE ON PURPOSE. There, a member with `manage` on a project
 * was allowed to register repos but not to read credentials, so an empty picker
 * had a cause the screen had to explain. Here the whole section is admin-only:
 * a reader who cannot list credentials cannot see this form either, and the
 * refusal has already been rendered by the section above. An arm for it would
 * be text no reader can reach.
 *
 * WHAT REPLACES IT NAMES THE KIND. An empty picker here usually means the
 * deployment's credentials are the wrong KIND for this harness — a codex
 * harness in a deployment holding only a subscription token — and "no
 * credentials" would be false in exactly that case, since the Credentials
 * section next door is showing several.
 */
function CredentialPickerNote(props: { kind: HarnessKind; credentials: Credential[] | undefined }) {
  if (props.credentials === undefined) return null
  if (bindableOptions(props.kind, props.credentials).length > 0) return null
  return (
    <p className="text-text-muted text-sm">
      No credential can authenticate a {props.kind} harness yet — it needs one of{' '}
      {HARNESS_CREDENTIAL_KINDS[props.kind].join(' or ')}. Add one in Credentials.
    </p>
  )
}
