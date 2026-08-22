import type { ReactNode } from 'react'
import { useFormContext } from 'react-hook-form'

/**
 * The accessibility wiring every field component needs, in one place.
 *
 * `TextField` states the reason this exists and Plan 12a is what forced it out:
 * "the accessibility wiring — label/htmlFor, `aria-invalid`, `aria-describedby`
 * pointing at the message element — is written once here instead of being
 * re-derived, and re-forgotten, per form." That held while there was one field
 * component. The new-task form needs a select and a textarea as well, and three
 * copies of the same six lines is the re-derivation that comment is about.
 *
 * Split in two because a control's attributes go ON the control while the label
 * and the message go AROUND it, and no single wrapper can put them both there
 * without taking the control as a render prop.
 */
export function useField(name: string): {
  id: string
  message: string | null
  a11y: { id: string; 'aria-invalid': boolean; 'aria-describedby': string | undefined }
} {
  const { formState } = useFormContext()
  const error = formState.errors[name]
  // Narrowed rather than `String(error.message)`: react-hook-form types
  // `message` as `string | undefined` (and it can be a React node), so the
  // blunt conversion renders the literal text "undefined" into the page for any
  // error that carries no message.
  const message = typeof error?.message === 'string' && error.message !== '' ? error.message : null
  const id = `field-${name}`
  return {
    id,
    message,
    a11y: {
      id,
      'aria-invalid': error !== undefined,
      'aria-describedby': message !== null ? `${id}-error` : undefined,
    },
  }
}

/** The label above a control and its error message below it. */
export function FieldFrame(props: {
  id: string
  label: string
  message: string | null
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={props.id} className="text-text-muted text-sm">
        {props.label}
      </label>
      {props.children}
      {props.message !== null && (
        <p id={`${props.id}-error`} role="alert" className="text-danger text-sm">
          {props.message}
        </p>
      )}
    </div>
  )
}

/** Shared by every control below, so one border does not drift from another. */
export const CONTROL_CLASS = 'bg-surface text-text border-border rounded-md border px-3 py-2'
