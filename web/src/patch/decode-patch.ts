export type DiffLine = { kind: 'context' | 'add' | 'del'; text: string }
export type DiffHunk = { header: string; lines: DiffLine[] }
export type DiffFile = {
  path: string
  oldPath: string
  added: number
  removed: number
  hunks: DiffHunk[]
  binary: boolean
}
export type ParsedPatch = {
  files: DiffFile[]
  added: number
  removed: number
  /** True when the bytes were not valid UTF-8 and the rendered text therefore
   *  contains replacement characters. A consumer must say so rather than
   *  quietly showing a corrupted diff as if it were the file. */
  lossy: boolean
}

const EMPTY: ParsedPatch = { files: [], added: 0, removed: 0, lossy: false }

/**
 * base64 → bytes → text → files.
 *
 * THE BASE64 IS NOT AN ENCODING CHOICE, IT IS THE CONTRACT (collect.go): a
 * patch is arbitrary bytes, a file in a legacy encoding diffs as non-UTF-8, and
 * JSON string encoding would replace those bytes with U+FFFD silently — the
 * mangled patch still applies. Decoded ONCE here, for display, and never
 * re-encoded: nothing in the client round-trips a patch.
 *
 * The parser is unified-diff-shaped and deliberately small. It exists to answer
 * the three questions an approval surface asks of a patch — which files, how
 * many ± lines, and what do the hunks say — and it never has to APPLY anything,
 * which is what a real diff library would be for.
 */
export function decodePatch(diffBase64: string): ParsedPatch {
  if (diffBase64 === '') return EMPTY
  let bytes: Uint8Array
  try {
    const bin = atob(diffBase64)
    bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
  } catch {
    // A payload the server accepted whose diff is not base64 is a producer bug,
    // and the surface's job is to say there is nothing to show — not to take
    // the screen down with it.
    return EMPTY
  }
  // Two decodes: one to LEARN whether the bytes are valid UTF-8, one to get
  // text regardless. `fatal: true` throws on the first invalid sequence —
  // unlike inspecting the lenient output for U+FFFD, it cannot mistake a
  // literal replacement character already in the patch for one we substituted.
  let lossy = false
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    lossy = true
  }
  const text = new TextDecoder('utf-8').decode(bytes)
  return { ...parseUnifiedDiff(text), lossy }
}

function parseUnifiedDiff(text: string): { files: DiffFile[]; added: number; removed: number } {
  const files: DiffFile[] = []
  let file: DiffFile | undefined
  let hunk: DiffHunk | undefined
  // What the open hunk's `@@` header still owes, per side. A line is hunk
  // content while either of these is positive; headers can only begin at zero.
  let oldRemaining = 0
  let newRemaining = 0

  const lines = text.split('\n')
  // '\n' is a line terminator: a trailing one does not open a final empty line
  // (which would otherwise land in the last hunk as a phantom context line).
  if (lines.at(-1) === '') lines.pop()

  for (const line of lines) {
    if (hunk && line.startsWith('\\')) {
      // `\ No newline at end of file` annotates the line before it: it counts
      // toward neither side and is not content to render. It can also trail
      // the hunk's LAST line, after both counts reach zero, so it is skipped
      // whenever a hunk is open, not only while content is owed.
      continue
    }
    if (file && hunk && (oldRemaining > 0 || newRemaining > 0)) {
      // While the `@@` header's counts say the hunk is still owed content, a
      // line is classified by its first character ALONE — never as a header.
      // A deleted line whose own text begins `-- ` renders as `--- …`,
      // exactly the file-header shape the checks below match; this repo's
      // own SQL migrations are full of lines that begin that way.
      if (line.startsWith('-')) {
        oldRemaining -= 1
        file.removed += 1
        hunk.lines.push({ kind: 'del', text: line.slice(1) })
        continue
      }
      if (line.startsWith('+')) {
        newRemaining -= 1
        file.added += 1
        hunk.lines.push({ kind: 'add', text: line.slice(1) })
        continue
      }
      if (line.startsWith(' ')) {
        oldRemaining -= 1
        newRemaining -= 1
        hunk.lines.push({ kind: 'context', text: line.slice(1) })
        continue
      }
      // Not hunk content, yet the header promised more: the counts were
      // wrong. Close the debt and let the header checks read the line.
      oldRemaining = 0
      newRemaining = 0
    }
    const git = /^diff --git a\/(.+) b\/(.+)$/.exec(line)
    if (git) {
      hunk = undefined
      // Both groups are non-optional in the pattern; the `?? ''` only satisfies
      // noUncheckedIndexedAccess, which cannot see that.
      file = {
        path: git[2] ?? '',
        oldPath: git[1] ?? '',
        added: 0,
        removed: 0,
        hunks: [],
        binary: false,
      }
      files.push(file)
    } else if (file && line.startsWith('Binary files ')) {
      // A binary file has no hunks to show; it closes with zero counts.
      file.binary = true
      hunk = undefined
    } else if (line.startsWith('--- ')) {
      // A `---` HERE is a file header: while a hunk is owed content the
      // branch above consumes `-…` lines first, so a deleted `-- …` line
      // never reaches this check from inside a well-counted hunk.
      //
      // A `---` also opens a new file when none is open (a patch does not need
      // `diff --git` lines — the non-UTF-8 fixture has none) or when the
      // current one already has its hunks.
      hunk = undefined
      if (!file || file.hunks.length > 0 || file.binary) {
        file = { path: '', oldPath: '', added: 0, removed: 0, hunks: [], binary: false }
        files.push(file)
      }
      const old = line.slice(4)
      if (old !== '/dev/null') file.oldPath = old.startsWith('a/') ? old.slice(2) : old
    } else if (file && line.startsWith('+++ ')) {
      const next = line.slice(4)
      // `+++ /dev/null` is a deletion: the file keeps its old path as THE path.
      if (next === '/dev/null') file.path = file.oldPath
      else file.path = next.startsWith('b/') ? next.slice(2) : next
    } else if (file && !file.binary && line.startsWith('@@')) {
      // The `@@ -a,b +c,d @@` counts say exactly how much content the hunk
      // owes (an omitted count is 1). The pattern is not end-anchored because
      // git appends the enclosing section heading after the trailing `@@`;
      // `header` keeps that whole line.
      const counts = /^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@/.exec(line)
      oldRemaining = counts ? Number(counts[1] ?? '1') : 0
      newRemaining = counts ? Number(counts[2] ?? '1') : 0
      hunk = { header: line, lines: [] }
      file.hunks.push(hunk)
    } else if (file && line.startsWith('+')) {
      // From here down: lines no open hunk is owed. Real headers matched
      // above, so this is content with malformed or missing counts —
      // classified by marker, the way the pre-count parser classified it.
      file.added += 1
      hunk?.lines.push({ kind: 'add', text: line.slice(1) })
    } else if (file && line.startsWith('-')) {
      file.removed += 1
      hunk?.lines.push({ kind: 'del', text: line.slice(1) })
    } else if (hunk) {
      hunk.lines.push({ kind: 'context', text: line.slice(1) })
    }
  }

  let added = 0
  let removed = 0
  for (const f of files) {
    added += f.added
    removed += f.removed
  }
  return { files, added, removed }
}
