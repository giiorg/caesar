import { zodResolver } from '@hookform/resolvers/zod'
import type { BaseSyntheticEvent } from 'react'
import { useState } from 'react'
import {
  type DefaultValues,
  type FieldValues,
  type Resolver,
  type UseFormReturn,
  useForm,
} from 'react-hook-form'
import type { ZodType } from 'zod'
import { ApiError } from '../api/errors'
import { applyProblemToForm } from './problem-to-fields'

export type AppForm<T extends FieldValues> = UseFormReturn<T> & {
  formError: string | null
  correlationId: string | null
  submit: (e?: BaseSyntheticEvent) => Promise<void>
  /**
   * Spread onto the `<form>` element. `<form {...form.formProps}>`, never
   * `<form onSubmit={form.submit}>`.
   *
   * `noValidate` is the whole reason this exists and it is NOT cosmetic.
   * MEASURED, and it is the HTML standard rather than a jsdom quirk: submitting
   * a form containing an `<input type="email">` whose value fails the type
   * constraint runs INTERACTIVE VALIDATION first, and the submit event is never
   * fired at all — `typeMismatch=true submitFired=false`. React's `onSubmit`
   * therefore never runs, react-hook-form never validates, and the zod message
   * this form exists to show is unreachable; the user gets the browser's native
   * bubble instead, which is unstyled, untranslated, not a `role="alert"`, and
   * gone on the next keystroke. With `noValidate` the same probe reports
   * `submitFired=true`.
   *
   * It belongs to the abstraction and not to each form for the reason §5 gives
   * for the abstraction existing: this is invisible until someone adds the
   * first constrained input type, and then it fails silently — the form simply
   * does nothing — in exactly the case validation is supposed to cover.
   */
  formProps: { onSubmit: (e?: BaseSyntheticEvent) => Promise<void>; noValidate: true }
}

/**
 * react-hook-form + the Zod resolver + the generated request types, with one
 * field registry, uniform error rendering, and a submit orchestrator that
 * catches `ApiError` and calls `setError` per FLAT field name.
 *
 * The wrapper is also what makes react-hook-form 8 survivable: v8 is in beta
 * and a migration touches THIS FILE rather than every form — the same argument
 * §5 makes for building the abstraction before any feature form.
 *
 * Parameterized on the VALUES type, not on the schema type. Both halves of that
 * are MEASURED against the pinned zod 4.4.3 / @hookform/resolvers 5.8.0 by
 * compiling the alternatives, not reasoned about:
 *
 *   `<S extends ZodType>` reads more naturally and does not compile — EIGHT
 *   errors, because bare `ZodType` is `unknown` for both input and output while
 *   react-hook-form needs a `FieldValues` (a record) at every position. They
 *   land on the two `UseFormReturn`/`useForm` constraint sites, the resolver
 *   cast, the `zodResolver` overload set, `onSubmit(values)`, `form.setError`
 *   and the return type. (`Object.keys` is NOT among them — it accepts the
 *   unconstrained type fine.)
 *
 *   `ZodType<T>` rather than `ZodType<T, T>` also fails, with the error that
 *   explains the whole shape: resolvers 5.8.0 declares `zodResolver<Input
 *   extends FieldValues, …>(schema: Zod4Type<Output, Input>)`, so leaving the
 *   INPUT at its `unknown` default gives `Type 'unknown' is not assignable to
 *   type 'FieldValues'` on every overload.
 *
 * A plain `z.object({…})` satisfies `ZodType<T, T>` at the call site with no
 * cast.
 */
export function useAppForm<T extends FieldValues>(opts: {
  schema: ZodType<T, T>
  defaultValues: DefaultValues<T>
  onSubmit: (values: T) => Promise<void>
}): AppForm<T> {
  const [formError, setFormError] = useState<string | null>(null)
  const [correlationId, setCorrelationId] = useState<string | null>(null)
  const form = useForm<T>({
    // The one cast, and it is narrow: `zodResolver` returns
    // `Resolver<Input, Context, Output>` and infers `Context` as `unknown` here
    // because nothing in the arguments mentions it, while `useForm<T>` wants
    // the `any` default. Input and Output are both already `T`.
    resolver: zodResolver(opts.schema) as Resolver<T>,
    defaultValues: opts.defaultValues,
  })

  const submit = form.handleSubmit(async (values) => {
    setFormError(null)
    setCorrelationId(null)
    try {
      await opts.onSubmit(values)
    } catch (e) {
      if (!(e instanceof ApiError)) {
        // NOT rethrown, and that is a correction to the plan rather than a
        // convenience. MEASURED with `throw e` here and an msw
        // `HttpResponse.error()` on POST /auth/login: zero alerts rendered, the
        // submit button re-enabled, and vitest reporting `Unhandled Rejection:
        // TypeError: Failed to fetch`. The throw lands inside handleSubmit's
        // async callback, which React never awaits, so no error boundary can
        // ever see it — and grepping web/src confirms there is no boundary and
        // no `unhandledrejection` listener to see it if one could. The user is
        // left pressing a button that does nothing, which on a self-hosted
        // single-user deployment is the commonest failure there is: the backend
        // restarting, met at exactly this form.
        //
        // The message names no cause. A transport failure, a malformed 200 that
        // fails to parse, and a genuine bug in `onSubmit` all arrive here and
        // only the first is a connection problem, so claiming one would be a
        // guess presented to the user as a diagnosis. What IS true of all three
        // is that the request did not complete. The real error is not
        // swallowed: `console.error` is the only place its identity survives,
        // which is why the test asserts on it too.
        setFormError('the request could not be completed — check your connection and try again')
        console.error(e)
        return
      }
      // Rendered on EVERY error surface: it is the only thread between a user's
      // complaint and a server log line.
      setCorrelationId(e.correlationId !== '' ? e.correlationId : null)
      // `knownFields` is the DEFAULT VALUES' keys, which is the only list that
      // is guaranteed to exist before the user has touched anything — the
      // schema's shape would do as well for a z.object but not for the unions
      // and effects a later form will want.
      const { formError: fe } = applyProblemToForm(
        e,
        form.setError,
        Object.keys(opts.defaultValues),
      )
      setFormError(fe)
    }
  })

  return Object.assign(form, {
    formError,
    correlationId,
    submit,
    formProps: { onSubmit: submit, noValidate: true as const },
  })
}
