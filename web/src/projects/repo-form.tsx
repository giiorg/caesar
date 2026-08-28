import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { type Credential, credentialsQueryOptions } from '../admin/credentials-query'
import { api, unwrap } from '../api/client'
import { asApiError } from '../errors/error-surface'
import { FormError } from '../forms/form-error'
import { SelectField, type SelectOption } from '../forms/select-field'
import { TextField } from '../forms/text-field'
import { useAppForm } from '../forms/use-app-form'
import type { Repo } from './projects-query'

/**
 * `forge` IS IN THE SCHEMA AND ONLY THE CREATE FORM RENDERS A CONTROL FOR IT.
 * `UpdateRepoRequest` carries slug, default branch and credential and nothing
 * else (`ContractsIdAndUpdateRepo` in the generated schema), so a repo's forge
 * is fixed at registration — there is no write path that could change it, and
 * an editable control would be a promise the API cannot keep.
 *
 * The two values are the server's own (`assets.go`, and the validator's message
 * at `repos.go`: "must be github or gitlab").
 */
const schema = z.object({
  forge: z.enum(['github', 'gitlab']),
  slug: z.string().min(1, 'give the repo a slug'),
  default_branch: z.string().min(1, 'name the default branch'),
  credential_id: z.string().min(1, 'pick a credential'),
})

const FORGE_OPTIONS = [
  { value: 'github', label: 'GitHub' },
  { value: 'gitlab', label: 'GitLab' },
] as const

/**
 * Register a repo on a project, or edit one that is already there.
 *
 * ON EDIT, ALL THREE EDITABLE FIELDS ARE SENT, and that is a statement about
 * what the form is showing rather than laziness. `UpdateRepo` treats a null
 * member as "leave this one alone" (`assets/repos.go`), which is the right
 * shape for a partial write — but this form displays all three, pre-filled, so
 * every one of them is a value the user has seen and left standing. Sending
 * only the changed ones would need a dirty-field diff whose only observable
 * difference is which audit detail the server writes.
 *
 * `default_branch` IS PRE-FILLED WITH `main` RATHER THAN LEFT BLANK. The server
 * substitutes `main` for an empty branch (`assets/repos.go`), so a blank field
 * would silently become something the user never typed. Showing the default is
 * how it stops being silent — and it is why the schema can require the field
 * without being stricter than the system: the only way to reach the empty value
 * is to delete what is already there, which is an intention rather than an
 * omission.
 */
export function RepoForm(props: { projectId: string; repo?: Repo; onDone: (repo: Repo) => void }) {
  const queryClient = useQueryClient()
  const existing = props.repo
  const credentials = useQuery(credentialsQueryOptions)

  const form = useAppForm({
    schema,
    defaultValues: {
      // `as const` on the tuple would not help here: the default has to be one
      // of the two literals for the enum, and a registered repo's forge is a
      // plain string on the wire. It is only ever read back on the edit form,
      // where no control writes it and the value goes nowhere — see onSubmit.
      forge: (existing?.forge === 'gitlab' ? 'gitlab' : 'github') as 'github' | 'gitlab',
      slug: existing?.slug ?? '',
      default_branch: existing?.default_branch ?? 'main',
      credential_id: existing?.credential_id ?? '',
    },
    onSubmit: async (values) => {
      const repo =
        existing === undefined
          ? await unwrap(
              await api.POST('/api/v1/projects/{id}/repos', {
                params: { path: { id: props.projectId } },
                body: {
                  forge: values.forge,
                  slug: values.slug,
                  default_branch: values.default_branch,
                  credential_id: values.credential_id,
                },
              }),
            )
          : await unwrap(
              await api.PATCH('/api/v1/repos/{id}', {
                params: { path: { id: existing.id } },
                // NO `forge` — the field is not in the request type at all, and
                // its absence here is the same fact the read-only display
                // states on screen.
                body: {
                  slug: values.slug,
                  default_branch: values.default_branch,
                  credential_id: values.credential_id,
                },
              }),
            )
      await queryClient.invalidateQueries({ queryKey: ['project-repos', props.projectId] })
      props.onDone(repo)
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        {existing === undefined ? (
          <SelectField
            name="forge"
            label="Forge"
            placeholder="Pick a forge"
            options={FORGE_OPTIONS}
          />
        ) : (
          /* STATIC TEXT, NOT A DISABLED CONTROL. A disabled select still reads
             as a control that is unavailable right now — something a permission
             or another field might unlock — and this one never will. The value
             is a property of the repo, so it is displayed like one. */
          <div className="flex flex-col gap-1">
            <p className="text-text-muted text-sm">Forge</p>
            <p className="text-text">{existing.forge}</p>
          </div>
        )}
        <TextField name="slug" label="Slug" />
        <TextField name="default_branch" label="Default branch" />
        <SelectField
          name="credential_id"
          label="Credential"
          placeholder="Pick a credential"
          options={forgeTokenOptions(credentials.data)}
        />
        <CredentialPickerNote query={credentials} />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          {existing === undefined ? 'Register repo' : 'Save repo'}
        </button>
      </form>
    </FormProvider>
  )
}

/**
 * FILTERED TO `forge_token`, AND THE SERVER AGREES — that is what makes this a
 * convenience rather than a client-side rule. `CreateRepo` and `UpdateRepo` both
 * call `requireCredentialKindIn(..., CredentialForgeToken)` and answer a 400 on
 * `credential_id` for anything else (`assets/repos.go`), so an unfiltered
 * picker would offer three kinds of credential of which exactly one can ever be
 * accepted. The milestone walk registers both a forge token and a harness
 * credential, so the wrong kinds are present in every real deployment, not just
 * in a fixture.
 */
const forgeTokenOptions = (credentials: Credential[] | undefined): SelectOption[] => {
  // `if (undefined)` RATHER THAN `?? []`, and the difference is which nulls
  // this function is allowed to absorb. `undefined` is react-query saying the
  // request has not answered yet; `null` is the WIRE, and the query above
  // already turns that into an empty array the way every other query in this
  // tree does. Written as a coalesce, this line would silently cover both — and
  // then deleting the query's own normalization would change nothing anywhere,
  // which is the definition of a guard no mutation can reach. MEASURED: with
  // the coalesce spelling, removing `?? []` from `credentialsQueryOptions` left
  // all ten tests in the detail suite green.
  if (credentials === undefined) return []
  return credentials
    .filter((c) => c.kind === 'forge_token')
    .map((c) => ({ value: c.id, label: c.name }))
}

/**
 * What the picker cannot offer, said out loud — the `Unavailable` argument from
 * the new-task form, with one arm this list needs and that one does not.
 *
 * THE 403 IS THE ARM THAT MATTERS. `GET /credentials` is admin-only, so a
 * member with `manage` on the project — who is allowed to register repos —
 * cannot read the credential list at all. Their picker is empty for a reason
 * that has nothing to do with the deployment having no credentials, and the two
 * are indistinguishable from an empty select. A silent empty select tells them
 * to keep looking for a control that will never fill in.
 */
function CredentialPickerNote(props: {
  query: { data: Credential[] | undefined; isError: boolean; error: unknown }
}) {
  const failure = asApiError(props.query.error)
  if (failure?.status === 403) {
    return (
      <p className="text-text-muted text-sm">
        Credentials are listed by admins only — ask an admin to attach one.
      </p>
    )
  }
  if (props.query.isError) {
    return <p className="text-danger text-sm">The credentials could not be read.</p>
  }
  if (props.query.data === undefined) {
    return <p className="text-text-muted text-sm">Reading credentials…</p>
  }
  if (forgeTokenOptions(props.query.data).length === 0) {
    // NOT "no credentials": the list may be full of model keys and
    // subscription tokens, none of which a repo can use. Naming the kind is
    // what turns a dead end into an instruction.
    return <p className="text-text-muted text-sm">No forge credentials are stored yet.</p>
  }
  return null
}
