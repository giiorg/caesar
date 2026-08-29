// Imports carry the `.ts` extension for `tools/arch/main.ts`'s reason: this
// file runs through `node --experimental-strip-types`, whose ESM resolver does
// not complete an extensionless specifier for a TypeScript file.
import { createHash } from 'node:crypto'
import { mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { toDocument } from '../src/workflows/derive.ts'
import { foldSeed, OPS_PER_SEED, SEEDS } from '../src/workflows/op-generator.ts'

/**
 * The seeded editor corpus: the documents this editor produces, written where
 * the REAL Go validator can judge them.
 *
 * THIS IS THE OTHER HALF OF THE §6.3 ACCEPTANCE BAR. `editor-property.test.ts`
 * asserts everything TypeScript can assert about a generated document;
 * "validates server-side" is a claim only `workflows.Validate` can settle, and
 * a Go test cannot call into this reducer. So the documents travel as bytes:
 * generated here, committed to the repository, and validated by
 * `backend/internal/workflows/editorcorpus_test.go` on the cheapest lane there
 * is. Change a derivation rule and either the corpus diffs (`contracts:check`
 * red) or the Go judge refuses the new documents — red either way, which is the
 * whole design.
 *
 * IT REPLAYS THROUGH THE PRODUCTION `editorReducer` — that is what `foldSeed`
 * is — rather than calling `derive` beside it. A generator that imported only
 * the derivation would leave every corpus document byte-identical under a
 * REDUCER mutation, and the Go lane green while the editor shipped something
 * else. `derive` is reached the way production reaches it: through the state
 * the reducer actually built.
 */

const DIR = fileURLToPath(
  new URL('../../backend/internal/workflows/testdata/editor_corpus/', import.meta.url),
)

const MANIFEST = 'manifest.json'

mkdirSync(DIR, { recursive: true })

const files = new Map<string, string>()
for (const seed of SEEDS) {
  // Compact, no spacing: `toDocument` builds its objects in `definition.go`'s
  // struct field order, so `JSON.stringify` with no spacing is byte-for-byte
  // what `Document.Canonical()` marshals — which `editorcorpus_test.go`
  // asserts, and which is what makes a version's content hash the same number
  // whether the editor or the server computed it.
  const body = JSON.stringify(toDocument(foldSeed(seed, OPS_PER_SEED).cards))
  const name = `seed-${seed}.json`
  writeFileSync(`${DIR}${name}`, body)
  files.set(name, createHash('sha256').update(body).digest('hex'))
}

/**
 * RECONCILE, don't accumulate.
 *
 * Dropping a seed from `SEEDS` without this leaves that seed's file behind
 * forever: generation never touches it, the working tree stays clean,
 * `contracts:check` is green, and the Go judge goes on validating a document no
 * rule the editor still has would produce. The deletion is what makes the diff
 * gate see it.
 *
 * It also removes a hand-written file that was never generated — the orphan
 * `editorcorpus_test.go`'s manifest count assertion catches BEFORE a
 * regeneration, and this catches at the next one.
 */
const expected = new Set([...files.keys(), MANIFEST])
const removed: string[] = []
for (const name of readdirSync(DIR)) {
  if (name.endsWith('.json') && !expected.has(name)) {
    rmSync(`${DIR}${name}`)
    removed.push(name)
  }
}

/**
 * The manifest is the EXACT expected set, stated in a form Go can read without
 * re-running Node: sorted names, each with the sha256 of the bytes on disk.
 * Globbing alone cannot tell "the corpus" from "the corpus plus a file somebody
 * left here".
 */
const manifest = { files: Object.fromEntries([...files].sort(([a], [b]) => (a < b ? -1 : 1))) }
writeFileSync(`${DIR}${MANIFEST}`, `${JSON.stringify(manifest, null, 2)}\n`)

console.log(
  `editor corpus: ${files.size} documents from ${SEEDS.length} seeds × ${OPS_PER_SEED} ops${
    removed.length > 0 ? `, removed ${removed.join(', ')}` : ''
  }`,
)
