import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, extname, join, relative, resolve, sep } from 'node:path'

export type Violation = { file: string; line: number; message: string }

const SOURCE = new Set(['.ts', '.tsx'])
const GENERATED = /(routeTree\.gen\.ts|schema\.d\.ts)$/
const NOT_A_CONSUMER = /\.(test|spec|stories)\.tsx?$/

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) {
      if (e !== 'node_modules') walk(p, out)
    } else if (SOURCE.has(extname(p)) && !GENERATED.test(p)) {
      out.push(p)
    }
  }
  return out
}

// Static AND dynamic: a lazily-imported module is still a consumer, and
// counting only static edges would let a second consumer hide behind import().
//
// `@/…` is here for the same reason `import(` is. tsconfig.json maps `@/*` to
// `src/*` and vitest.config.ts repeats the alias, so `import { x } from
// '@/routes/a/helper'` is an ordinary in-tree edge that a relative-only pattern
// cannot see — and an edge this checker cannot see is a second consumer it will
// never report. There is no fixture-shaped way to notice that later: the rule
// would simply be quiet.
const IMPORT_RE = /(?:from\s*|import\s*\(\s*)['"]((?:\.|@\/)[^'"]+)['"]/g

/**
 * `root` is where `@/` points. In `task web:arch` that is `web/src`, which is
 * exactly what tsconfig's `paths` says `@/*` resolves to; in a fixture it is
 * the fixture directory, so an aliased fixture import stays inside its tree.
 */
function resolveSpecifier(fromFile: string, spec: string, root: string): string | null {
  const base = spec.startsWith('@/')
    ? resolve(root, spec.slice(2))
    : resolve(dirname(fromFile), spec)
  for (const cand of [
    base,
    `${base}.ts`,
    `${base}.tsx`,
    join(base, 'index.ts'),
    join(base, 'index.tsx'),
  ]) {
    try {
      if (statSync(cand).isFile()) return cand
    } catch {
      /* not this candidate */
    }
  }
  return null
}

function commonAncestor(paths: string[]): string {
  const parts = paths.map((p) => dirname(p).split(sep))
  const head = parts[0] ?? []
  let i = 0
  while (i < head.length && parts.every((p) => p[i] === head[i])) i++
  return head.slice(0, i).join(sep)
}

/**
 * The directories directly under `root` are the DECLARED GROUPS. Spec §2 names
 * them (`routes/ api/ realtime/ forms/ ui/ styles/`) and the plan's File
 * Structure mandates a permanent home inside one for every module. Creating a
 * new one is a deliberate, visible act, which is why this is structural rather
 * than a hand-maintained list — a list would have to anticipate `auth/`,
 * `shell/`, `approvals/` and `theme/` and would turn `web:arch` red in a later
 * task for a reason nobody would look here for.
 */
function groupRootOf(root: string, file: string): string {
  const rel = relative(root, dirname(file))
  if (rel === '') return root
  return join(root, rel.split(sep)[0] as string)
}

const isWithin = (parent: string, child: string): boolean =>
  child === parent || child.startsWith(parent + sep)

/**
 * A module with two or more direct consumers must live at their nearest common
 * ancestor directory.
 *
 * "Consumer" is a DIRECT importing module — static or dynamic — excluding
 * tests, stories and generated files. Route-root reachability is the tempting
 * alternative and it is the wrong one: every shared module is reachable from
 * every route, so it would place everything at the top and assert nothing. The
 * two definitions disagree precisely on transitive helpers, which is most of
 * what this rule will ever see.
 *
 * THE REQUIRED HOME IS CLAMPED TO THE TARGET'S OWN GROUP ROOT, and that is not
 * a softening — it is the rule's scope. Spec §2 asks for colocation *within*
 * the route tree; `api/`, `realtime/`, `forms/` and `ui/` are declared
 * permanent homes, and a module reaching them is shared by construction. The
 * unclamped rule contradicts the File Structure it is supposed to enforce:
 * MEASURED against a tree built from this plan's own literal import lines, it
 * reported `api/errors.ts has 5 consumers (api/client.ts, api/query-client.ts,
 * forms/problem-to-fields.ts, realtime/sse-connection.ts, routes/_app.tsx) so
 * it must live in .` — and the same for `api/client.ts` — because the common
 * ancestor across four groups is `src`. So: a module AT its group root is at
 * its declared home and is never reported; a module BELOW it is told to move up
 * to that root at most, never out of the group. `fixtures/grouproot/` pins both
 * halves — the exempt one and the one that must still fire.
 */
export function findMisplacedModules(root: string): Violation[] {
  const files = walk(root)
  const consumers = new Map<string, string[]>()

  for (const file of files) {
    if (NOT_A_CONSUMER.test(file)) continue
    const src = readFileSync(file, 'utf8')
    for (const m of src.matchAll(IMPORT_RE)) {
      const target = resolveSpecifier(file, m[1] as string, root)
      if (target === null || GENERATED.test(target)) continue
      const list = consumers.get(target) ?? []
      if (!list.includes(file)) list.push(file)
      consumers.set(target, list)
    }
  }

  const out: Violation[] = []
  for (const [target, importers] of consumers) {
    if (importers.length < 2) continue
    const groupRoot = groupRootOf(root, target)
    const ancestor = commonAncestor(importers)
    // Inside the group, colocate at the ancestor; outside it, the group root is
    // the answer. A root-level module has `groupRoot === root`, so the ancestor
    // always wins and its behaviour is unchanged.
    const required = isWithin(groupRoot, ancestor) ? ancestor : groupRoot
    if (dirname(target) === required) continue
    out.push({
      file: relative(root, target),
      line: 1,
      message:
        `${relative(root, target)} has ${importers.length} consumers ` +
        `(${importers.map((i) => relative(root, i)).join(', ')}) ` +
        `so it must live in ${relative(root, required) || '.'}`,
    })
  }
  return out.sort((a, b) => a.file.localeCompare(b.file))
}

/**
 * THE CROSS-GROUP MATRIX. §2 of the spec asks for the directions between
 * top-level groups to be mechanical rather than remembered, and this is where
 * that lives — not in Biome, because a lint config cannot carry a fixture and
 * a rule with no failing mutant is a rule nobody has tested.
 *
 * Only two directions are stated, because only two are load-bearing:
 *
 *   ui/**       imports NOTHING from api, realtime, auth or routes.
 *               `ui` is the presentational layer. The moment a button knows
 *               how to fetch, it stops being reusable and starts being a
 *               route, and the misplacement rule above cannot see the
 *               difference — a widget imported twice is legal wherever it sits.
 *
 *   api/**      imports NOTHING from routes, shell or ui.
 *               The generated client and its error type are the bottom of the
 *               stack. An import upward makes the contract layer depend on the
 *               chrome, and it is exactly the edge that turns `unwrap` into
 *               something that cannot be tested without rendering.
 *
 * realtime -> shell is deliberately NOT banned but IS eliminated: the
 * transport in Task 10 imports `ConnectionState` from
 * `shell/connection-indicator`, which is upside down — the transport produces
 * that state and the shell displays it. Task 8 declares the type in
 * `src/realtime/connection-state.ts` and the shell imports it from there.
 * Stating the rule and leaving the violation in the tree would teach the next
 * reader that the rules are decorative.
 */
const FORBIDDEN: ReadonlyArray<{ from: string; to: readonly string[]; why: string }> = [
  {
    from: 'ui',
    to: ['api', 'realtime', 'auth', 'routes'],
    why: 'ui is presentational; a component that fetches belongs to a route',
  },
  {
    from: 'api',
    to: ['routes', 'shell', 'ui'],
    why: 'the client is the bottom of the stack and must not depend on the chrome',
  },
]

const groupOf = (root: string, file: string): string => relative(root, file).split(sep)[0] ?? ''

export function findCrossGroupImports(root: string): Violation[] {
  const out: Violation[] = []
  for (const file of walk(root)) {
    if (NOT_A_CONSUMER.test(file)) continue
    const from = groupOf(root, file)
    const rule = FORBIDDEN.find((r) => r.from === from)
    if (rule === undefined) continue
    readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        for (const m of line.matchAll(IMPORT_RE)) {
          const target = resolveSpecifier(file, m[1] as string, root)
          if (target === null) continue
          const to = groupOf(root, target)
          if (!rule.to.includes(to)) continue
          out.push({
            file: relative(root, file),
            line: i + 1,
            message: `${from}/ must not import ${to}/ — ${rule.why}`,
          })
        }
      })
  }
  return out.sort((a, b) => a.file.localeCompare(b.file))
}
