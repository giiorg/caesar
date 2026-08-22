import { useFormContext } from 'react-hook-form'
import { CONTROL_CLASS, FieldFrame, useField } from './field-frame'

/**
 * A multi-line field, for the one input on the new-task form that is prose.
 *
 * Separate from `TextField` rather than a `multiline` flag on it: the two
 * render different elements with different attributes (`rows` has no meaning on
 * an input, `type` none on a textarea), and a flag that switches the element
 * would put both attribute sets on one signature and let a caller ask for a
 * combination that does not exist.
 */
export function TextAreaField(props: { name: string; label: string; rows?: number }) {
  const { register } = useFormContext()
  const { id, message, a11y } = useField(props.name)
  return (
    <FieldFrame id={id} label={props.label} message={message}>
      <textarea
        {...a11y}
        rows={props.rows ?? 4}
        className={CONTROL_CLASS}
        {...register(props.name)}
      />
    </FieldFrame>
  )
}
