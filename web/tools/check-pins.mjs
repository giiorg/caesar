// A rule nobody checks is a rule that has already been broken. This one fails
// the lane on any version string that is not an exact pin — the same reason
// routeTree.gen.ts is regen-diffed rather than trusted.
import { readFileSync } from 'node:fs'

const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'))
const blocks = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']
const EXACT = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/
const bad = []
for (const block of blocks) {
  for (const [name, range] of Object.entries(pkg[block] ?? {})) {
    if (!EXACT.test(range)) {
      bad.push(`${block}.${name} = ${JSON.stringify(range)}`)
    }
  }
}

// `overrides` (npm) and `resolutions` (yarn, honoured by some tooling) rewrite
// the resolved version of a transitive dependency, so a range here defeats the
// pin above without ever appearing in a dependency block. Both nest: a value is
// either a version string or a further map, and the key `.` means the package
// itself. Walk them.
function walkOverrides(node, path) {
  for (const [name, value] of Object.entries(node ?? {})) {
    const at = `${path}.${name}`
    if (typeof value === 'string') {
      // A leading `$` means "whatever this manifest pins for that name", which
      // is exact by construction because the blocks above are checked.
      if (!value.startsWith('$') && !EXACT.test(value)) {
        bad.push(`${at} = ${JSON.stringify(value)}`)
      }
    } else if (value && typeof value === 'object') {
      walkOverrides(value, at)
    }
  }
}
for (const block of ['overrides', 'resolutions']) {
  walkOverrides(pkg[block], block)
}
if (bad.length > 0) {
  console.error('web: every dependency must be pinned exactly (no ^, ~, *, or ranges):')
  for (const line of bad) console.error(`  - ${line}`)
  process.exit(1)
}
console.log(
  `web: ${blocks.reduce((n, b) => n + Object.keys(pkg[b] ?? {}).length, 0)} dependencies, all exactly pinned`,
)
