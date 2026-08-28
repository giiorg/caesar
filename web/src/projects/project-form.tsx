import { useQueryClient } from '@tanstack/react-query'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { FormError } from '../forms/form-error'
import { TextField } from '../forms/text-field'
import { TextAreaField } from '../forms/textarea-field'
import { useAppForm } from '../forms/use-app-form'
import type { Project } from './projects-query'

/**
 * `description` HAS NO MINIMUM and that is the server's shape, not a
 * relaxation. `validateDescription` accepts the empty string
 * (`assets/projects.go`), and a client that demanded one would refuse a project
 * the API would have created — the form would be stricter than the system, with
 * nothing on the server to justify the sentence it showed.
 *
 * `name` is `.min(1)` for the opposite reason: `validateName` rejects blank,
 * so the empty value is a guaranteed round trip to a 400 the user can be spared.
 */
const schema = z.object({
  name: z.string().min(1, 'give the project a name'),
  description: z.string(),
})

/**
 * ONE COMPONENT FOR CREATE AND EDIT, keyed on whether a project was handed in.
 *
 * The two differ in exactly three places — the verb, the endpoint, and which
 * cache keys move — and everything else (both fields, both validations, the
 * banner, the correlation id) is identical. Two components would be two copies
 * of the zod schema, and a schema that drifts from its twin is a form that
 * accepts on one screen what it refuses on the other.
 *
 * THE DUPLICATE-NAME CONFLICT REACHES THE BANNER, NEVER THE NAME FIELD, and
 * that is measured rather than assumed: `CreateProject` answers
 * `xerrors.E(ClassConflict, "project name already taken")` with NO `WithFields`
 * (`assets/projects.go`), so the problem document carries no `errors[]` at all
 * and `applyProblemToForm` takes its no-fields arm. Routing it onto `name` by
 * hand would mean the client inventing a field attribution the server did not
 * make — and it would be wrong the moment the same 409 is raised for something
 * other than the name.
 */
export function ProjectForm(props: { project?: Project; onDone: (project: Project) => void }) {
  const queryClient = useQueryClient()
  const existing = props.project
  const form = useAppForm({
    schema,
    defaultValues: {
      name: existing?.name ?? '',
      description: existing?.description ?? '',
    },
    onSubmit: async (values) => {
      const project =
        existing === undefined
          ? await unwrap(await api.POST('/api/v1/projects', { body: values }))
          : await unwrap(
              await api.PATCH('/api/v1/projects/{id}', {
                params: { path: { id: existing.id } },
                body: values,
              }),
            )
      // BOTH KEYS, on an edit: the card list renders the name and the detail
      // header renders it too, and they are separate cache entries fed by
      // separate endpoints. Invalidating only the one the user is looking at
      // leaves the other holding the old name until its interval comes round —
      // and the user's next move is the back button.
      await queryClient.invalidateQueries({ queryKey: ['projects'] })
      if (existing !== undefined) {
        await queryClient.invalidateQueries({ queryKey: ['project', existing.id] })
      }
      props.onDone(project)
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextField name="name" label="Name" />
        <TextAreaField name="description" label="Description" />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          {existing === undefined ? 'Create project' : 'Save project'}
        </button>
      </form>
    </FormProvider>
  )
}
