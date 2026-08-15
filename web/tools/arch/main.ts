// Imports carry the `.ts` extension on purpose: `task web:arch` runs this file
// through `node --experimental-strip-types`, and Node's ESM resolver does NOT
// complete an extensionless specifier for a TypeScript file — MEASURED on
// node 24.19.0: `import { x } from './dep'` dies with ERR_MODULE_NOT_FOUND.
// tsconfig.json therefore sets `allowImportingTsExtensions` (legal because the
// project is `noEmit`), so tsc and node agree on one spelling.
import { findCrossGroupImports, findMisplacedModules } from './import-graph.ts'
import { findNonSemanticColors } from './tokens.ts'

const root = new URL('../../src', import.meta.url).pathname
const violations = [
  ...findMisplacedModules(root),
  ...findCrossGroupImports(root),
  ...findNonSemanticColors(root),
]
for (const v of violations) console.error(`${v.file}:${v.line}: ${v.message}`)
if (violations.length > 0) {
  console.error(`web:arch: ${violations.length} violation(s)`)
  process.exit(1)
}
console.log('web:arch: clean')
