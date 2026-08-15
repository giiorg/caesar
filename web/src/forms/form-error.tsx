export function FormError(props: { message: string | null; correlationId: string | null }) {
  // Returning null on a null MESSAGE would drop the correlation id in the most
  // common server-rejection shape there is: applyProblemToForm returns
  // formError null whenever every error mapped to a known field, so a 400 with
  // per-field messages would render field errors and no reference at all —
  // exactly the case a user reports and support cannot trace.
  if (props.message === null && props.correlationId === null) return null
  return (
    <div role="alert" className="border-danger text-danger rounded-md border px-3 py-2 text-sm">
      {props.message !== null && <p>{props.message}</p>}
      {props.correlationId !== null && (
        <p className="text-text-muted font-mono text-xs">reference: {props.correlationId}</p>
      )}
    </div>
  )
}
