import { useFormContext } from 'react-hook-form'
import { CONTROL_CLASS, FieldFrame, useField } from './field-frame'

export type SelectOption = { value: string; label: string }

/**
 * A NATIVE `<select>`, and that is a mobile decision rather than a shortcut.
 * On the phone this screen is designed for, a native select opens the OS
 * picker — a scroll wheel sized for a thumb, with the platform's own search and
 * dismissal — and no listbox this project would build gets close to it. It also
 * costs nothing in a test: `userEvent.selectOptions` drives the real control.
 *
 * THE PLACEHOLDER IS AN OPTION WITH AN EMPTY VALUE, not a `disabled` one. An
 * empty string is what the zod schema rejects, so "nothing picked yet" is a
 * state the form validates rather than a state the markup tries to prevent —
 * and the message then lands under the control like every other field's.
 *
 * `onSelect` fires AFTER react-hook-form has recorded the change, so a handler
 * that resets a dependent field (project → repo) is writing over a value the
 * form already knows about rather than racing it.
 */
export function SelectField(props: {
  name: string
  label: string
  options: readonly SelectOption[]
  placeholder: string
  disabled?: boolean
  onSelect?: (value: string) => void
}) {
  const { register } = useFormContext()
  const { id, message, a11y } = useField(props.name)
  const field = register(props.name)
  return (
    <FieldFrame id={id} label={props.label} message={message}>
      <select
        {...a11y}
        disabled={props.disabled ?? false}
        className={CONTROL_CLASS}
        {...field}
        onChange={async (e) => {
          const { value } = e.target
          await field.onChange(e)
          props.onSelect?.(value)
        }}
      >
        <option value="">{props.placeholder}</option>
        {props.options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </FieldFrame>
  )
}
