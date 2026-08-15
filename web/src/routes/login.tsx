import { useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { TextField } from '../forms/fields/text-field'
import { FormError } from '../forms/form-error'
import { useAppForm } from '../forms/use-app-form'

export const Route = createFileRoute('/login')({ component: LoginPage })

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
