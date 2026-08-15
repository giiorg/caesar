import type { FieldValues, Path, UseFormSetError } from 'react-hook-form'
import type { ApiError } from '../api/errors'

/**
 * Places a problem+json document onto a form: per-field where it can, at the
 * form level where it cannot.
 *
 * Matching is by FLAT NAME. `xerrors.FieldError` is `{field, message}` and the
 * name is a plain identifier, so a pointer-shaped mapper (`/email`) would match
 * nothing on every real validation failure the backend produces.
 *
 * The three outcomes are ordered, and the order is the design:
 *
 *  1. Anything unplaceable wins, even when other errors routed. A message the
 *     form cannot put next to an input is still the server's explanation of why
 *     the submit failed; swallowing it leaves the user staring at a form that
 *     refused with no reason given. Checking this FIRST is what makes the mixed
 *     problem — some fields known, some not — surface the unknown ones instead
 *     of returning null because something else happened to route.
 *  2. Everything routed, so the fields carry the whole story and a form-level
 *     banner would only repeat it.
 *  3. No fields at all — the primary shape, not the edge: the 401 login really
 *     answers for bad credentials names no field whatsoever. `detail` is the
 *     specific sentence and `title` the generic one, so an empty detail must
 *     fall through rather than render an empty error box.
 *
 * `routed` is COUNTED rather than derived. The obvious spelling
 * `err.fields.length > unroutable.length` is, at the point it would be
 * evaluated, provably identical to `err.fields.length > 0` — the early return
 * above guarantees `unroutable.length === 0` — so half of it is dead and no
 * test can ever distinguish the two. A counter states the same thing with
 * nothing dead in it.
 */
export function applyProblemToForm<T extends FieldValues>(
  err: ApiError,
  setError: UseFormSetError<T>,
  knownFields: readonly string[],
): { formError: string | null } {
  const unroutable: string[] = []
  let routed = 0
  for (const f of err.fields) {
    if (knownFields.includes(f.field)) {
      setError(f.field as Path<T>, { type: 'server', message: f.message })
      routed++
    } else {
      unroutable.push(f.message)
    }
  }
  if (unroutable.length > 0) return { formError: unroutable.join('; ') }
  if (routed > 0) return { formError: null }
  return { formError: err.detail !== '' ? err.detail : err.title }
}
