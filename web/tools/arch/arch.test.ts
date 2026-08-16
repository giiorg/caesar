import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { findCrossGroupImports, findMisplacedModules } from './import-graph.ts'
import { findNonSemanticColors, SEMANTIC } from './tokens.ts'

const fx = (p: string) => new URL(`./fixtures/${p}`, import.meta.url).pathname

describe('import-graph consumer-count rule', () => {
  it('reports a module with two consumers that is not at their common ancestor', () => {
    const v = findMisplacedModules(fx('misplaced'))
    expect(v).toHaveLength(1)
    expect(v[0]?.file).toContain('routes/a/helper.ts')
    expect(v[0]?.message).toContain('routes')
  })

  it('does not count tests as consumers', () => {
    // helper.test.ts imports helper.ts. If tests counted, the message would
    // name a different required home and the count above would still be 1 —
    // so assert on the required directory, not just the violation count.
    const v = findMisplacedModules(fx('misplaced'))
    expect(v[0]?.message).not.toContain('helper.test')
  })

  it('counts a consumer that imports through the @/ alias', () => {
    // routes/c/page.ts imports '@/routes/a/helper'. tsconfig maps `@/*` to
    // `src/*` and vitest repeats the alias, so this is an ordinary edge — but
    // a checker matching only './' and '../' cannot see it, and an unseen edge
    // is a consumer that never gets counted. Assert the consumer LIST, because
    // the violation count stays 1 either way.
    const v = findMisplacedModules(fx('misplaced'))
    expect(v[0]?.message).toContain('routes/c/page.ts')
    expect(v[0]?.message).toContain('has 4 consumers')
  })

  it('counts a consumer that reaches its target only through import()', () => {
    // routes/d/page.ts has no `from` clause at all — its single edge to
    // helper.ts is `await import('../a/helper')`. The Global Constraint says a
    // consumer is a direct importer "static or dynamic", and MEASURED before
    // this fixture existed: deleting `import\s*\(\s*` from IMPORT_RE left the
    // whole suite green. The violation count stays 1 whatever this checker
    // sees, so assert the consumer LIST and the count inside the message.
    const v = findMisplacedModules(fx('misplaced'))
    expect(v[0]?.message).toContain('routes/d/page.ts')
    expect(v[0]?.message).toContain('has 4 consumers')
  })

  it('leaves a module at its declared group root alone, however many groups use it', () => {
    // grouproot/api/errors.ts is imported from api/, forms/ and routes/. Their
    // common ancestor is the fixture ROOT, so the unclamped rule ordered it to
    // move to `.` — i.e. out of the home the File Structure mandates. It is the
    // real shape of api/errors.ts once Tasks 5/8/10/11 land, and it turned
    // `task web:arch` red on a correctly-placed file.
    //
    // Asserting only "errors.ts is absent" would also pass if the rule had gone
    // vacuous, so assert the WHOLE result: exactly one violation, and it is the
    // module below the group root.
    const v = findMisplacedModules(fx('grouproot'))
    expect(v.map((x) => x.file)).toEqual(['api/detail/parse.ts', 'routes/deep/util.ts'])
  })

  it('still colocates at a directory deeper than the group root', () => {
    // The clamp is a FLOOR, not an answer. routes/deep/util.ts is used only from
    // routes/deep/nested/, so it belongs there — clamping every module to its
    // group root would pass every other assertion in this file while abolishing
    // colocation inside routes/, which is the rule spec §2 actually asked for.
    const v = findMisplacedModules(fx('grouproot')).filter((x) => x.file.includes('util.ts'))
    expect(v).toHaveLength(1)
    expect(v[0]?.message).toContain('so it must live in routes/deep/nested')
  })

  it('still moves a module below a group root up to that root, not out of the group', () => {
    // The other half of the exemption. api/detail/parse.ts is consumed by
    // api/client.ts and routes/_app.tsx, whose common ancestor is again the
    // fixture root — and "move it to the root" is the wrong prescription. The
    // required home is the target's own group root.
    const v = findMisplacedModules(fx('grouproot'))
    expect(v[0]?.message).toContain('has 2 consumers')
    expect(v[0]?.message).toContain('so it must live in api')
    expect(v[0]?.message).not.toContain('must live in .')
  })
})

describe('cross-group imports', () => {
  it('names every forbidden edge, once per import', () => {
    const v = findCrossGroupImports(fx('groups'))
    // 4 from ui/bad-widget + 3 from api/bad-client + 1 from ui/alias-widget,
    // which writes the ui -> api edge through the '@/' alias. If this is 0, the
    // fixture import targets are missing from disk and the checker resolved
    // nothing — do NOT "fix" it by changing this number.
    expect(v).toHaveLength(8)
    expect(v.filter((x) => x.file.includes('alias-widget'))).toHaveLength(1)
    for (const to of ['api', 'realtime', 'auth', 'routes']) {
      expect(
        v.some((x) => x.file.includes('bad-widget') && x.message.includes(`import ${to}/`)),
      ).toBe(true)
    }
    for (const to of ['shell', 'ui', 'routes']) {
      expect(
        v.some((x) => x.file.includes('bad-client') && x.message.includes(`import ${to}/`)),
      ).toBe(true)
    }
  })

  it('permits imports within a group', () => {
    // The root stays the GROUPS DIRECTORY and the filter is applied to the
    // result. Passing `fx('groups/ui/good-widget.tsx')` instead would make
    // `relative(root, file)` the empty string, so `groupOf` returns '', no
    // rule matches, and the assertion passes without the checker having
    // looked at anything — a green that means nothing.
    const v = findCrossGroupImports(fx('groups')).filter((x) => x.file.includes('good-widget'))
    expect(v).toEqual([])
  })
})

describe('token check', () => {
  it('catches every non-semantic colour form, not only hex literals', () => {
    const v = findNonSemanticColors(fx('colors'))
    const lines = v.map((x) => `${x.file}:${x.line}`)
    // 6 in bad.tsx, 3 in bad.css. bg-white and text-red-500 are the ones a
    // literal-hunting checker misses entirely — they contain no colour at all.
    expect(v).toHaveLength(9)
    expect(v.some((x) => x.message.includes('bg-white'))).toBe(true)
    expect(v.some((x) => x.message.includes('text-red-500'))).toBe(true)
    expect(lines.every((l) => !l.includes('good.tsx'))).toBe(true)
  })

  it('reports each offending token exactly once', () => {
    // Without this, `bg-[#ff0000]` counts twice — once as a utility, once as a
    // hex literal — and every count assertion here becomes unreadable. Nine
    // offending lines, nine violations: the mapping has to be one to one, or
    // the count above passes for a reason nobody intended.
    const v = findNonSemanticColors(fx('colors'))
    const perLine = new Map<string, number>()
    for (const x of v) {
      const k = `${x.file}:${x.line}`
      perLine.set(k, (perLine.get(k) ?? 0) + 1)
    }
    expect([...perLine.values()].filter((n) => n > 1)).toEqual([])
    expect(perLine.size).toBe(9)
  })

  it('flags the named CSS colour for being named, not for its property', () => {
    // `.z { border-color: rebeccapurple }` is the trap: a prefix-only matcher
    // "catches" it by reading `border-color` as the utility `border-` + the
    // value `color`. It would then pass this fixture while being blind to
    // every named colour on a property it does not recognise.
    const v = findNonSemanticColors(fx('colors/bad.css'))
    const z = v.filter((x) => x.line === 3)
    expect(z).toHaveLength(1)
    expect(z[0]?.message).toContain('named CSS colour')
  })

  it('does not scan tests, whose colours style nothing', () => {
    // `fixtures/colors/bad.test.tsx` carries four violations in the same forms
    // bad.tsx does, so this asserts the EXCLUSION and not merely a quiet file.
    // Task 8's WCAG suite is the real case: it reads the shipped tokens out of
    // the theme files, and the regex that parses them contains `oklch(`, which
    // the literal pass cannot tell from a colour. Drop the pattern from SKIP
    // and both this and the count above go red.
    const v = findNonSemanticColors(fx('colors'))
    expect(v.filter((x) => x.file.includes('bad.test'))).toEqual([])
  })

  it('passes semantic utilities and every same-prefix non-colour utility', () => {
    // text-sm, border-t, border-dashed, shadow-sm and the /opacity forms all
    // share a prefix with a real colour utility and none of them is one.
    const v = findNonSemanticColors(fx('colors/good.tsx'))
    expect(v).toEqual([])
  })

  it('accepts every semantic token name that src/styles/tokens.css declares', () => {
    // The SEMANTIC list in tokens.ts is a hand-copy of the theme block. If the
    // two drift — a token added to the CSS and not here — the check starts
    // reporting a legitimate utility as non-semantic, which is the same
    // failure mode as the text-sm regression and just as hard to trace.
    const css = readFileSync(
      new URL('../../src/styles/tokens.css', import.meta.url).pathname,
      'utf8',
    )
    const declared = [...css.matchAll(/^\s*--color-([a-z-]+):/gm)]
      .map((m) => m[1] as string)
      .filter((n) => n !== '*')
    expect(declared.length).toBeGreaterThan(0)
    expect([...SEMANTIC].sort()).toEqual([...declared].sort())
  })
})
