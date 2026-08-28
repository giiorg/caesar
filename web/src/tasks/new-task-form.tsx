import { queryOptions, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import { FormError } from '../forms/form-error'
import { SelectField, type SelectOption } from '../forms/select-field'
import { TextField } from '../forms/text-field'
import { TextAreaField } from '../forms/textarea-field'
import { useAppForm } from '../forms/use-app-form'
import { projectReposQueryOptions, projectsQueryOptions } from '../projects/projects-query'
import type { Task } from './tasks-query'

const workflowsQueryOptions = queryOptions({
  queryKey: ['workflows'],
  queryFn: async () => (await unwrap(await api.GET('/api/v1/workflows'))).workflows ?? [],
  ...AUTH_SCOPED_QUERY_OPTIONS,
})

/**
 * `project_id` IS NOT A `CreateTaskRequest` FIELD and is deliberately in the
 * form anyway: the repo endpoint is project-scoped, so picking a project is how
 * a person reaches a repo. It is dropped at submit time.
 *
 * There is no `workflow_version` here either, and that absence is the design —
 * see `version` below.
 */
const schema = z.object({
  project_id: z.string().min(1, 'pick a project'),
  repo_id: z.string().min(1, 'pick a repo'),
  workflow_id: z.string().min(1, 'pick a workflow'),
  title: z.string().min(1, 'give the task a title'),
  instructions: z.string().min(1, 'say what needs doing'),
})

/**
 * THE SHORT FORM IS THE LOCKED DESIGN'S OWN STATED ALTERNATIVE, not a
 * substitute invented here. Locked Tasks B pairs the list with an
 * assistant-drafted task — you describe what you want and something else fills
 * in the repo, the workflow and the inputs — and that is roadmap row 13
 * (§10.6), unbuilt. §10.6 names this short form as what ships until then.
 *
 * So: five controls, every one of which the server needs, and no field that
 * pretends to be understood. `instructions` is the only free text and it goes
 * into `inputs` verbatim; nothing here parses it, and nothing claims to.
 */
export function NewTaskForm(props: { onCreated: (task: Task) => void }) {
  const queryClient = useQueryClient()
  /**
   * THE VERSION AS IT WAS WHEN THE WORKFLOW WAS PICKED, held beside the form
   * rather than inside it.
   *
   * `CreateTaskRequest.workflow_version` pins the run to a compiled definition
   * and the form has to read it off `WorkflowResponse.latest_version`. Looking
   * it up at SUBMIT time instead needs a `find` over a list that refetches on
   * an interval and on window focus, so it has a `undefined` arm that no test
   * can reach and a `?? 0` that would silently pin the wrong definition.
   * Recording it in the same event that records the id has no such arm: the id
   * came out of `workflowOptions` in that render, so the lookup below cannot
   * miss, and the only value that maps to 0 is the placeholder — which the
   * schema rejects before `onSubmit` runs.
   *
   * It is not a form field because there is no control for it. A server error
   * naming `workflow_version` therefore stays unroutable and reaches the
   * form-level banner, which is where a message about a value with no input on
   * screen belongs.
   */
  const [version, setVersion] = useState(0)

  const form = useAppForm({
    schema,
    defaultValues: {
      project_id: '',
      repo_id: '',
      workflow_id: '',
      title: '',
      instructions: '',
    },
    onSubmit: async (values) => {
      const task = await unwrap(
        await api.POST('/api/v1/tasks', {
          body: {
            repo_id: values.repo_id,
            workflow_id: values.workflow_id,
            workflow_version: version,
            title: values.title,
            // `CreateTaskRequest` HAS NO INSTRUCTIONS FIELD. `inputs` is the
            // task-level map the definition's nodes read, and it is where free
            // text belongs — inventing a column for it would be a schema
            // change 12a does not make.
            inputs: { instructions: values.instructions },
          },
        }),
      )
      // The list this form is mounted over is keyed `['tasks']`. Invalidating
      // is what the cache bridge does for a stream frame; a create has no frame
      // of its own to wait for, and the operator who just pressed the button is
      // the least tolerant reader of a stale list there is.
      await queryClient.invalidateQueries({ queryKey: ['tasks'] })
      props.onCreated(task)
    },
  })

  const projectId = form.watch('project_id')
  const projects = useQuery(projectsQueryOptions)
  const repos = useQuery(projectReposQueryOptions(projectId))
  const workflows = useQuery(workflowsQueryOptions)

  const workflowOptions = new Map((workflows.data ?? []).map((w) => [w.id, w.latest_version]))

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />

        <SelectField
          name="project_id"
          label="Project"
          placeholder="Pick a project"
          options={asOptions(projects.data, (p) => ({ value: p.id, label: p.name }))}
          // A REPO BELONGS TO A PROJECT, so a repo chosen under the old one is
          // not merely stale, it is a value the next request would send to a
          // project that does not contain it. Clearing it makes the cascade's
          // second step visibly unanswered rather than wrong.
          onSelect={() => form.setValue('repo_id', '')}
        />
        <Unavailable
          query={projects}
          what="projects"
          empty="No projects are shared with you yet."
        />

        <SelectField
          name="repo_id"
          label="Repo"
          placeholder="Pick a repo"
          disabled={projectId === ''}
          options={asOptions(repos.data, (r) => ({ value: r.id, label: r.slug }))}
        />
        {projectId !== '' && (
          <Unavailable
            query={repos}
            what="repos"
            empty="No repos are registered on that project."
          />
        )}

        <SelectField
          name="workflow_id"
          label="Workflow"
          placeholder="Pick a workflow"
          options={asOptions(workflows.data, (w) => ({ value: w.id, label: w.name }))}
          onSelect={(id) => setVersion(workflowOptions.get(id) ?? 0)}
        />
        <Unavailable query={workflows} what="workflows" empty="No workflows are defined yet." />

        <TextField name="title" label="Title" />
        <TextAreaField name="instructions" label="Instructions" />

        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Create task
        </button>
      </form>
    </FormProvider>
  )
}

const asOptions = <T,>(rows: T[] | undefined, to: (row: T) => SelectOption): SelectOption[] =>
  (rows ?? []).map(to)

/**
 * What a select CANNOT offer, said out loud.
 *
 * A select whose only entry is its placeholder is the plausible empty content a
 * stub never fakes: it reads as "still loading" forever, and it reads the same
 * whether the list is genuinely empty, still in flight, or came back 500. Each
 * of those needs a different sentence, and the person who cannot find their
 * repo needs to know which one it is.
 *
 * Nothing renders when the list arrived with rows in it — the control is the
 * answer then.
 */
function Unavailable(props: {
  query: { data: unknown[] | undefined; isError: boolean }
  what: string
  empty: string
}) {
  if (props.query.isError) {
    return <p className="text-danger text-sm">The {props.what} could not be read.</p>
  }
  if (props.query.data === undefined) {
    return <p className="text-text-muted text-sm">Reading {props.what}…</p>
  }
  if (props.query.data.length === 0) {
    return <p className="text-text-muted text-sm">{props.empty}</p>
  }
  return null
}
