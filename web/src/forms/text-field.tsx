import { useFormContext } from 'react-hook-form'
import { CONTROL_CLASS, FieldFrame, useField } from './field-frame'

/**
 * The field-component registry's first entry.
 *
 * It reads the form off context rather than taking it as a prop so that a form
 * body stays a list of fields, and so the label/error wiring is written once —
 * in `field-frame.tsx`, which is where it moved when the new-task form added a
 * select and a textarea beside this.
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
  const { register } = useFormContext()
  const { id, message, a11y } = useField(props.name)
  return (
    <FieldFrame id={id} label={props.label} message={message}>
      <input
        {...a11y}
        type={props.type ?? 'text'}
        autoComplete={props.autoComplete}
        className={CONTROL_CLASS}
        {...register(props.name)}
      />
    </FieldFrame>
  )
}
