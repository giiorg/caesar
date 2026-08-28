import { useQueryClient } from '@tanstack/react-query'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { FormError } from '../forms/form-error'
import { TextField } from '../forms/text-field'
import { TextAreaField } from '../forms/textarea-field'
import { useAppForm } from '../forms/use-app-form'
import type { Workflow } from './workflows-query'

/**
 * `name` is `.min(1)` and `description` has no minimum, and both halves are the
 * server's shape rather than a client opinion: `validateWorkflowName` rejects
 * blank and `validateWorkflowDescription` accepts the empty string
 * (`workflows/workflows.go`). A client stricter than the system would refuse a
 * workflow the API would have created.
 *
 * NEITHER LENGTH CEILING IS RESTATED HERE — 100 and 500 bytes, and they are
 * BYTE counts on a string this side measures in UTF-16 code units. `.max(100)`
 * would accept a 60-character name of emoji the server then refuses, which is
 * worse than not checking: the form would have promised. The 400 carries
 * `{field: "name", message: "must be 1-100 bytes"}` and
 * `applyProblemToForm` puts it under the input.
 */
const schema = z.object({
  name: z.string().min(1, 'give the workflow a name'),
  description: z.string(),
})

/**
 * ONE COMPONENT FOR CREATE AND EDIT, keyed on whether a workflow was handed in
 * — `ProjectForm`'s shape, for `ProjectForm`'s reason: the two differ in the
 * verb, the endpoint and which cache keys move, and two components would be two
 * copies of one zod schema drifting apart.
 *
 * THE DUPLICATE-NAME CONFLICT REACHES THE BANNER, NEVER THE NAME FIELD, and it
 * is measured rather than assumed. `CreateWorkflow` AND `UpdateWorkflow` both
 * answer `xerrors.E(ClassConflict, "workflow name already taken")` with no
 * `WithFields` (`workflows/workflows.go`), `httpx.Problem` writes `errors`
 * `omitempty` so the document carries no such key, and `applyProblemToForm`
 * takes its no-fields arm. Routing it onto `name` by hand would be the client
 * inventing an attribution the server did not make — and it would be wrong the
 * day the same 409 is raised for something other than the name.
 *
 * THE CREATE PATH DOES NOT CLOSE ANYTHING; it hands the new workflow back and
 * the caller decides. That is the first half of the two-write flow — see
 * `workflows.index.tsx`, which navigates into the editor rather than closing a
 * sheet over a list.
 */
export function WorkflowForm(props: { workflow?: Workflow; onDone: (workflow: Workflow) => void }) {
  const queryClient = useQueryClient()
  const existing = props.workflow
  const form = useAppForm({
    schema,
    defaultValues: {
      name: existing?.name ?? '',
      description: existing?.description ?? '',
    },
    onSubmit: async (values) => {
      const workflow =
        existing === undefined
          ? await unwrap(await api.POST('/api/v1/workflows', { body: values }))
          : await unwrap(
              await api.PATCH('/api/v1/workflows/{id}', {
                params: { path: { id: existing.id } },
                body: values,
              }),
            )
      // BOTH KEYS ON AN EDIT: the list renders the name and the detail header
      // renders it too, from separate endpoints into separate cache entries.
      // Invalidating only the one being looked at leaves the other holding the
      // old name until its interval comes round — and the next move is Back.
      await queryClient.invalidateQueries({ queryKey: ['workflows'] })
      if (existing !== undefined) {
        await queryClient.invalidateQueries({ queryKey: ['workflow', existing.id] })
      }
      props.onDone(workflow)
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
          {existing === undefined ? 'Create workflow' : 'Save workflow'}
        </button>
      </form>
    </FormProvider>
  )
}
