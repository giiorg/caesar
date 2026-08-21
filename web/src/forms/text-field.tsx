import { useFormContext } from 'react-hook-form'

/**
 * The field-component registry's first entry.
 *
 * It reads the form off context rather than taking it as a prop so that a form
 * body stays a list of fields, and so the accessibility wiring — label/htmlFor,
 * `aria-invalid`, `aria-describedby` pointing at the message element — is
 * written once here instead of being re-derived, and re-forgotten, per form.
 *
 * AT `forms/`, NOT IN A `fields/` SUBDIRECTORY, and the checker rather than
 * taste decided that. `tools/arch/import-graph.ts` clamps a module with two or
 * more consumers to its GROUP ROOT: a module below the root is told to move up
 * to it. A registry entry has many consumers by definition, so `forms/fields/`
 * was a home no second importer could survive — Task 11's gate note was the
 * first, and `web:arch` reported the violation the moment it landed.
 */
export function TextField(props: {
  name: string
  label: string
  type?: 'text' | 'email' | 'password'
  autoComplete?: string
}) {
  const { register, formState } = useFormContext()
  const error = formState.errors[props.name]
  // Narrowed rather than `String(error.message)`: react-hook-form types
  // `message` as `string | undefined` (and it can be a React node), so the
  // blunt conversion renders the literal text "undefined" into the page for any
  // error that carries no message.
  const message = typeof error?.message === 'string' && error.message !== '' ? error.message : null
  const id = `field-${props.name}`
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-text-muted text-sm">
        {props.label}
      </label>
      <input
        id={id}
        type={props.type ?? 'text'}
        autoComplete={props.autoComplete}
        aria-invalid={error !== undefined}
        aria-describedby={message !== null ? `${id}-error` : undefined}
        className="bg-surface text-text border-border rounded-md border px-3 py-2"
        {...register(props.name)}
      />
      {message !== null && (
        <p id={`${id}-error`} role="alert" className="text-danger text-sm">
          {message}
        </p>
      )}
    </div>
  )
}
