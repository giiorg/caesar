import { queryOptions, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { usersQueryOptions } from '../admin/users-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'
import { asApiError, ErrorSurface } from '../errors/error-surface'
import { FormError } from '../forms/form-error'
import { SelectField } from '../forms/select-field'
import { TextField } from '../forms/text-field'
import { useAppForm } from '../forms/use-app-form'
import { FormSheet } from '../ui/form-sheet'

type Me = components['schemas']['ApiMeResponse']
type ProjectGrant = components['schemas']['ApiProjectGrantResponse']
type User = components['schemas']['ApiUserResponse']

/**
 * The grant ladder, IN ORDER, and the plan's one instruction that could not be
 * carried out as written.
 *
 * The plan said to read this vocabulary out of the generated
 * `SetGrantRequest`/`GrantResponse` types and to correct the plan in place if
 * its guess differed. Neither type exists under those names, and the ones that
 * do carry no vocabulary at all: `ContractsIdAndSetGrant` is
 * `{level?: string, user_id?: string}` — `level` is a bare `string`, because
 * the Go request struct types it as a `GrantLevel` whose underlying type is
 * `string` and the emitter has no enum to emit. So there is nothing in
 * `schema.d.ts` for a reader to check this list against.
 *
 * The source is `backend/internal/platform/authn/authn.go`, which declares the
 * four constants AND their ranks (`grantRank`: view 1 · run 2 · approve 3 ·
 * manage 4). The plan's guessed values were right; only its cited source was
 * wrong. THE ORDER IS THE LOAD-BEARING PART — `AtLeast` is a rank comparison
 * there and `atLeast` below is the same comparison here, so an array reordered
 * by someone tidying it alphabetically would silently move the manage boundary.
 */
export const GRANT_LEVELS = ['view', 'run', 'approve', 'manage'] as const

type GrantLevel = (typeof GRANT_LEVELS)[number]

/**
 * `-1` FOR AN UNKNOWN LEVEL, WHICH IS WHAT MAKES THIS FAIL CLOSED. A level the
 * server grew and this client has not learned yet ranks below everything, so it
 * is refused rather than admitted — the safe direction for a comparison whose
 * only caller decides who may edit a project's access list.
 */
const rankOf = (level: string): number => GRANT_LEVELS.indexOf(level as GrantLevel)

/**
 * Whether this session may READ AND WRITE this project's grants — one question,
 * because the API asks only one.
 *
 * ALL THREE GRANT ENDPOINTS ENFORCE `manage`, INCLUDING THE LIST.
 * `grantScoped` (`backend/internal/identity/api/api.go`) resolves the project,
 * 404s an unknown one, and then calls `Authorize(..., GrantManage)` before
 * `handleListGrants` ever runs. So there is no read-only view of this section
 * to offer somebody with `approve`: for them the section's own data is a 403.
 * That is why the caller renders NOTHING rather than an error panel — they can
 * legitimately see the project, and a failure surface on a screen they are
 * allowed to be on reads as a fault rather than as a boundary.
 *
 * A GLOBAL ADMIN PASSES WITHOUT A GRANT, mirroring `Authorize`, which short
 * circuits on the role before it looks a grant up at all.
 *
 * COMPARED BY RANK RATHER THAN BY EQUALITY. `manage` is the top of the ladder
 * today, so `=== 'manage'` would behave identically — and would quietly become
 * wrong the day a level is added above it, which is exactly the kind of change
 * whose breakage nobody would look for here.
 */
export function canManageProject(me: Me, projectId: string): boolean {
  if (me.user.role === 'admin') return true
  const grant = me.grants?.find((g) => g.project_id === projectId)
  return grant !== undefined && rankOf(grant.level) >= rankOf('manage')
}

const grantsQueryOptions = (projectId: string) =>
  queryOptions({
    queryKey: ['grants', projectId],
    queryFn: async () =>
      (
        await unwrap(
          await api.GET('/api/v1/projects/{id}/grants', {
            params: { path: { id: projectId } },
          }),
        )
      ).grants ?? [],
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })

/**
 * Who can reach this project, and the controls to change it.
 *
 * GRANTS LIVE ON THE PROJECT because the API is project-nested and a grant is a
 * property of a project — `PUT /projects/{id}/grants` upserts one, so "add" and
 * "change the level of" are the same request and this section offers one form
 * for both rather than two that differ only in wording.
 *
 * RENDERED ONLY WHEN `canManageProject`, decided by the caller — see that
 * function for why the alternative is an error panel shown to somebody who has
 * done nothing wrong.
 */
export function GrantsSection(props: { projectId: string }) {
  const queryClient = useQueryClient()
  const grants = useQuery(grantsQueryOptions(props.projectId))
  const [granting, setGranting] = useState(false)

  const remove = useMutation({
    mutationFn: async (grant: ProjectGrant) =>
      unwrap(
        await api.DELETE('/api/v1/projects/{id}/grants/{user_id}', {
          params: { path: { id: props.projectId, user_id: grant.user_id } },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['grants', props.projectId] }),
  })

  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-text font-medium">Grants</h3>
      {grants.isError ? (
        <ErrorSurface failure={asApiError(grants.error)} />
      ) : grants.data === undefined ? (
        <p className="text-text-muted text-sm">Reading grants…</p>
      ) : grants.data.length === 0 ? (
        /* "NOBODY ELSE": the only readers of this sentence are an admin or a
           manager of this project, both of whom can reach it themselves. "No
           one has access" would be false to the person reading it. */
        <p className="text-text-muted text-sm">
          Nobody else has been granted access to this project yet.
        </p>
      ) : (
        <ul data-testid="grant-rows" className="flex flex-col gap-2">
          {grants.data.map((grant) => (
            <li
              key={grant.user_id}
              data-testid={`grant-${grant.user_id}`}
              className="border-border bg-surface flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border p-3"
            >
              <span className="text-text grow font-medium">{grant.email}</span>
              <span className="text-text-muted font-mono text-xs">{grant.level}</span>
              {/* NAMED WITH THE EMAIL, like `RowButtons` on the repo list: the
                  accessible name of a control repeated once per row has to say
                  which row it belongs to, and it is also what lets a test reach
                  one person's Remove without counting siblings. */}
              <button
                type="button"
                onClick={() => remove.mutate(grant)}
                disabled={remove.isPending}
                className="border-border text-text rounded-md border px-3 py-2 text-sm font-medium"
              >
                Remove {grant.email}
              </button>
            </li>
          ))}
        </ul>
      )}
      {remove.isError && <ErrorSurface failure={asApiError(remove.error)} />}
      <button
        type="button"
        onClick={() => setGranting(true)}
        className="border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium"
      >
        Grant access
      </button>
      <FormSheet
        open={granting}
        onOpenChange={setGranting}
        title="Grant access"
        description="Pick a person and the level they get. Setting a level for somebody who already has one replaces it."
      >
        {/* MOUNTED ONLY WHILE THE SHEET IS OPEN, the shape the edit-repo sheet
            already uses. It is also what keeps `GET /users` off the project
            detail's first paint: the picker's query belongs to the form, so it
            goes out when somebody opens the form and not before. */}
        {granting && <GrantForm projectId={props.projectId} onDone={() => setGranting(false)} />}
      </FormSheet>
    </section>
  )
}

const schema = z.object({
  user_id: z.string().min(1, 'pick a user'),
  level: z.enum(GRANT_LEVELS),
})

/**
 * Set one person's level on this project.
 *
 * THE DEFAULT LEVEL IS THE BOTTOM OF THE LADDER. `view` is what an unattended
 * submit grants, so the failure mode of not choosing is the least access rather
 * than the most.
 */
function GrantForm(props: { projectId: string; onDone: () => void }) {
  const queryClient = useQueryClient()
  const users = useQuery(usersQueryOptions)
  const usersForbidden = asApiError(users.error)?.status === 403

  const form = useAppForm({
    schema,
    defaultValues: { user_id: '', level: 'view' as GrantLevel },
    onSubmit: async (values) => {
      await unwrap(
        await api.PUT('/api/v1/projects/{id}/grants', {
          params: { path: { id: props.projectId } },
          body: { user_id: values.user_id, level: values.level },
        }),
      )
      await queryClient.invalidateQueries({ queryKey: ['grants', props.projectId] })
      props.onDone()
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        {usersForbidden ? (
          /**
           * THE PICKER DEGRADES TO THE ID, AND SAYS WHY. `GET /users` is
           * admin-only while granting needs only `manage` on the project, so a
           * project manager who is not a global admin can grant access and
           * cannot list the people to grant it to. That is a real deployment's
           * real state, not a contrived one. The id is a control they can still
           * complete — the server validates it and answers a field error on
           * `user_id` for anything that is not a uuid — and an empty select
           * with no explanation would instead tell them to wait for a control
           * that will never fill in.
           */
          <>
            <TextField name="user_id" label="User id" />
            <p className="text-text-muted text-sm">
              Users are listed by admins only — grant by user id instead.
            </p>
          </>
        ) : (
          <>
            <SelectField
              name="user_id"
              label="User"
              placeholder="Pick a person"
              options={(users.data ?? []).map((u: User) => ({ value: u.id, label: u.email }))}
            />
            {users.isError && <p className="text-danger text-sm">The people could not be read.</p>}
          </>
        )}
        <SelectField
          name="level"
          label="Level"
          placeholder="Pick a level"
          options={GRANT_LEVELS.map((l) => ({ value: l, label: l }))}
        />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Save grant
        </button>
      </form>
    </FormProvider>
  )
}
