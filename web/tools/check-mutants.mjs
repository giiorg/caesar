// Inverted assertions need a runner: a lane that merely ran `tsc` on the
// mutants would go red for the RIGHT outcome. This one fails when they compile.
import { spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Invoke the installed compiler by path, never `npx tsc`: npx falls back to the
// public registry when the binary is absent, and the package it lands on is not
// the TypeScript compiler. A missing install must be a named error here, not a
// mutant that "failed for an unrelated reason".
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const tsc = fileURLToPath(new URL('../node_modules/.bin/tsc', import.meta.url))
if (!existsSync(tsc)) {
  console.error(`web: ${tsc} is missing — run \`task web:install\` before the mutant check`)
  process.exit(1)
}

const cases = [
  {
    config: 'tsconfig.mutants.json',
    file: 'tools/mutants/bad-route-to.tsx',
    expect: 'TS2322',
    what: 'route-inference mutant (src/router.ts Register augmentation)',
  },
]
let failed = false
for (const c of cases) {
  const r = spawnSync(tsc, ['--noEmit', '--pretty', 'false', '-p', c.config], {
    cwd: webRoot,
    encoding: 'utf8',
  })
  const out = `${r.stdout}${r.stderr}`
  // The assertion must identify the MUTANT, not the run: `cases` is written to
  // grow, and a second fixture emitting the same code anywhere in the output
  // would otherwise keep this green with the guarded augmentation deleted.
  // tsc --pretty false emits `<path>(<line>,<col>): error TSxxxx: ...`.
  const named = out
    .split('\n')
    .some((line) => line.startsWith(`${c.file}(`) && line.includes(`error ${c.expect}:`))
  if (r.status === 0) {
    console.error(`web: ${c.what} COMPILED — the guard it protects is not active`)
    failed = true
  } else if (!named) {
    console.error(
      `web: ${c.what} failed, but ${c.file} did not report ${c.expect} — the mutant may be failing for an unrelated reason:\n${out}`,
    )
    failed = true
  } else {
    console.log(`web: ${c.what} correctly rejected (${c.file}: ${c.expect})`)
  }
}
process.exit(failed ? 1 : 0)
