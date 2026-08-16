// MUTANT — this file is required to fail `tsc -p tsconfig.mutants.json`.
//
// It is the only signal that src/router.ts's `declare module` block is doing
// anything. Delete the augmentation and this file compiles clean, tsc --noEmit
// passes, and every typed navigation in the app silently degrades to `string`.
// If this file ever compiles, the router's type safety is gone; do not "fix"
// it by relaxing the expectation.
import { Link } from '@tanstack/react-router'

export function BadLink() {
  return <Link to="/does-not-exist">no such route</Link>
}
