import { readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import type { Violation } from './import-graph.ts'

// The semantic namespace, and the ONLY colour names that may appear. It is a
// list of NAMES, not a hunt for literals — see below for why that inversion is
// the whole design. Exported so arch.test.ts can hold it against the
// `--color-*` declarations in src/styles/tokens.css: this list is a hand-copy
// of that theme block, and silent drift makes the check reject a legitimate
// utility.
export const SEMANTIC = [
  'ground',
  'surface',
  'surface-raised',
  'border',
  'text',
  'text-muted',
  'accent',
  'accent-contrast',
  'danger',
  'warning',
  'success',
] as const

const SEMANTIC_SET = new Set<string>(SEMANTIC)

/**
 * A utility is `prefix-value`, and the prefix ALONE does not make it a colour:
 * `text-sm`, `border-t`, `border-dashed` and `shadow-sm` share prefixes with
 * `text-muted`, `border-border` and `shadow-accent`. So this regex only
 * NOMINATES; classifyUtility decides. Getting that split wrong is not a
 * theoretical risk — a checker that flags on the prefix rejects `text-sm`,
 * which Task 6 adds three tasks later, and the failure surfaces as an
 * unrelated task going red.
 */
const UTILITY =
  /\b(bg|text|border|ring|fill|stroke|from|via|to|outline|decoration|shadow|accent|caret|divide|placeholder)-([A-Za-z0-9[\]#(),.%/_-]+)/g

/** Tailwind's default hues. The theme reset deletes them; naming one is the bug. */
const TW_HUE =
  /^(?:slate|gray|grey|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-(?:50|\d{3})$/
const TW_BARE = /^(?:white|black|transparent|current|inherit)$/

const CSS_COLOUR_FN = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|\boklch\(|\bcolor\(/
const NAMED =
  '(?:white|black|red|blue|green|rebeccapurple|gray|grey|silver|maroon|olive|lime|aqua|teal|navy|fuchsia|purple|yellow|orange)'
/** A named colour in a CSS *declaration value* — after `:`, before `;` or `}`. */
const CSS_NAMED_DECL = new RegExp(`:[^;{}]*\\b${NAMED}\\b`)
/** A named colour in a JSX inline style — `color: 'red'`, `backgroundColor: "navy"`. */
const JSX_NAMED_DECL = new RegExp(`(?:color|Color)\\s*:\\s*['"]\\s*${NAMED}\\s*['"]`)

/**
 * Three answers, not two. 'ok' is a semantic token, 'bad' is a colour that
 * evades the tokens, and 'not-a-colour' is the sizing/layout utility that
 * merely shares a prefix — the case a prefix-only matcher gets wrong.
 */
function classifyUtility(value: string): 'ok' | 'bad' | 'not-a-colour' {
  // `bg-accent/10` is the token at 10% opacity: still semantic.
  const base = value.replace(/\/(?:\d{1,3}|\[[^\]]*\])$/, '')
  if (SEMANTIC_SET.has(base)) return 'ok'
  // `bg-[#fff]`, `text-[rgb(1,2,3)]`, `bg-(--some-var)` — arbitrary values are
  // the deliberate escape hatch out of the token system, so they are the bug.
  if (base.startsWith('[') || base.startsWith('(')) return 'bad'
  if (TW_BARE.test(base) || TW_HUE.test(base)) return 'bad'
  return 'not-a-colour'
}

const SCAN = new Set(['.ts', '.tsx', '.css'])
/**
 * Tests, stories and generated files are not scanned, matching the
 * `NOT_A_CONSUMER` exclusion the import-graph checker already applies.
 *
 * For THIS rule the exclusion is not a softening at all: nothing in a test file
 * styles anything. The rule's subject is what ships, and the component whose
 * classes a test asserts on is itself scanned — so a test can only ever produce
 * a false positive here. It produced one immediately: Task 8's WCAG suite reads
 * the shipped tokens out of the theme files, and the REGEX THAT PARSES THEM
 * contains the substring `oklch(`, which the literal-colour pass cannot tell
 * from a colour. The theme files are skipped below for exactly that reason —
 * they are where colour is allowed to be spelled out — and the test that reads
 * them inherits the same exemption.
 */
const SKIP =
  /(tokens\.css|theme-dark\.css|theme-light\.css|routeTree\.gen\.ts|schema\.d\.ts|\.(test|spec|stories)\.tsx?)$/

function walk(dir: string, out: string[] = []): string[] {
  if (statSync(dir).isFile()) return [dir]
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) {
      if (e !== 'node_modules') walk(p, out)
    } else if (SCAN.has(extname(p)) && !SKIP.test(p)) {
      out.push(p)
    }
  }
  return out
}

/**
 * THE TRAP THIS IS WRITTEN AROUND: `<div className="bg-white text-red-500" />`
 * bypasses every semantic token and contains no hex, no rgb(), no oklch() —
 * there is nothing for a literal search to find. §6 closes the hole at the
 * source by resetting Tailwind's colour namespace, which lets this check
 * validate NAMES instead of hunting literals. The theme files themselves are
 * skipped: they are where colour is allowed to be spelled out.
 */
export function findNonSemanticColors(root: string): Violation[] {
  const out: Violation[] = []
  for (const file of walk(root)) {
    const isCSS = extname(file) === '.css'
    readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        const at = (message: string) =>
          out.push({ file: relative(root, file), line: i + 1, message })

        // ONE violation per offending token. `bg-[#ff0000]` is a single
        // mistake, and a checker that reports it twice — once as a utility,
        // once as a hex literal — makes every count assertion in the test
        // below meaningless. So the utility pass runs first and BLANKS what
        // it consumed; the literal passes then see only what is left.
        let rest = ''
        let cursor = 0
        for (const m of line.matchAll(UTILITY)) {
          const verdict = classifyUtility(m[2] as string)
          if (verdict === 'not-a-colour') continue
          const start = m.index as number
          rest += line.slice(cursor, start)
          rest += ' '.repeat(m[0].length)
          cursor = start + m[0].length
          if (verdict === 'bad') at(`non-semantic colour utility "${m[0]}"`)
        }
        rest += line.slice(cursor)

        // Inline styles and stylesheet declarations both, since neither
        // creates an import edge the graph checker could see.
        if (CSS_COLOUR_FN.test(rest) && !rest.includes('var(--color-')) {
          at('literal colour value; use a semantic token')
        }
        if (isCSS ? CSS_NAMED_DECL.test(rest) : JSX_NAMED_DECL.test(rest)) {
          at('named CSS colour; use a semantic token')
        }
      })
  }
  return out
}
