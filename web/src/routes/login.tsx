import { useQueryClient } from '@tanstack/react-query'
import { createFileRoute, redirect, useNavigate } from '@tanstack/react-router'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { ApiError } from '../api/errors'
import { meQueryOptions } from '../auth/me-query'
import { TextField } from '../forms/fields/text-field'
import { FormError } from '../forms/form-error'
import { useAppForm } from '../forms/use-app-form'

export const Route = createFileRoute('/login')({
  /**
   * An existing session at /login redirects inward. `_app` guards the way in;
   * this guards the way back to the form once you already have a session.
   *
   * `fetchQuery` WITH `staleTime: 0`, NOT `ensureQueryData` — and that is the
   * whole correctness of this guard. MEASURED against the pinned react-query
   * 5.101.4: after a refetch 401s, the cache entry KEEPS its `data`, and
   * `ensureQueryData` then resolves from it without issuing a request at all.
   * The guard would conclude "signed in" from an answer the server has already
   * rejected, redirect to `/`, and `_app`'s expiry check would bounce it
   * straight back here — an infinite loop rendering an empty page. That is not
   * hypothetical: the plan specified `ensureQueryData` and it broke the
   * expired-session test in exactly this way.
   *
   * A guard that sends a visitor away from the login form has to PROVE the
   * session, not recall one. `_app`'s loader is deliberately the opposite: it
   * accepts cached data so a reload is instant, and relies on the layout's
   * expiry check for correctness.
   *
   * Any ApiError means "no proven session" and keeps the visitor on the form.
   * That deliberately includes a 5xx: the form is the honest place to be when
   * the server cannot say whether you are signed in.
   *
   * The redirect is thrown OUTSIDE the try. A router redirect is itself thrown,
   * so a `throw redirect()` inside would land in this catch and survive only
   * because it is not an ApiError — one `instanceof` edit away from a guard
   * that silently swallows its own redirect.
   */
  beforeLoad: async ({ context }) => {
    try {
      await context.queryClient.fetchQuery({ ...meQueryOptions, staleTime: 0 })
    } catch (e) {
      if (e instanceof ApiError) return
      throw e
    }
    throw redirect({ to: '/' })
  },
  component: LoginPage,
})

// Zod 4 promotes string formats to top-level factories: z.email(), not
// z.string().email(). VERIFIED against the pinned zod 4.4.3 — `typeof z.email
// === 'function'`, and the deprecated method form still exists, so a wrong
// spelling here would compile and only differ in the deprecation.
//
// The shape is components['schemas']['ApiLoginRequest'] from schema.d.ts:
// `{ email: string; password: string }`, both required.
const schema = z.object({
  email: z.email('enter a valid email address'),
  password: z.string().min(1, 'enter your password'),
})

/**
 * Separated from the route component so the form can be tested without a
 * router or a query client, and so the same form can later be mounted by a
 * re-authentication dialog without becoming a navigation.
 */
export function LoginForm(props: { onSuccess: () => void }) {
  const form = useAppForm({
    schema,
    defaultValues: { email: '', password: '' },
    onSubmit: async (values) => {
      await unwrap(await api.POST('/api/v1/auth/login', { body: values }))
      props.onSuccess()
    },
  })

  return (
    <FormProvider {...form}>
      {/* {...form.formProps}, not onSubmit={form.submit} — see AppForm.formProps. */}
      <form {...form.formProps} className="flex w-full max-w-sm flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextField name="email" label="Email" type="email" autoComplete="username" />
        <TextField
          name="password"
          label="Password"
          type="password"
          autoComplete="current-password"
        />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Sign in
        </button>
      </form>
    </FormProvider>
  )
}

function LoginPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  return (
    <main className="flex min-h-dvh items-center justify-center p-6">
      <LoginForm
        onSuccess={() => {
          // POST /auth/login returns only the user; GET /auth/me returns the
          // user AND their grants. Seeding the cache from the login response
          // would create an auth/me entry missing the grants the rest of the
          // app reads, so this invalidates and lets the loader refetch.
          void queryClient
            .invalidateQueries({ queryKey: ['auth', 'me'] })
            .then(() => navigate({ to: '/' }))
        }}
      />
    </main>
  )
}
