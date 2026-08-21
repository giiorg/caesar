import { describe, expect, it } from 'vitest'
import { decodePatch } from './decode-patch'

// Nothing here needs a document, but this file runs in the `dom` vitest
// project: vitest.config.ts gives `node` only `tools/**/*.test.ts`, and
// `src/**/*.test.ts` is the dom project's glob. Deliberate — one pure file is
// not a reason to widen the node project's include.

const b64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
const b64text = (s: string) => b64(new TextEncoder().encode(s))

const TWO_FILES = `diff --git a/src/auth.ts b/src/auth.ts
index 111..222 100644
--- a/src/auth.ts
+++ b/src/auth.ts
@@ -1,4 +1,5 @@
 import { db } from './db'
-export function login() {}
+export function login(): void {}
+export function logout(): void {}
 // tail
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-old
+new
`

const SQL_COMMENTS = `diff --git a/db/schema.sql b/db/schema.sql
--- a/db/schema.sql
+++ b/db/schema.sql
@@ -1,4 +1,2 @@ create table runs
 create table runs (
--- legacy column: kept while the old reader was alive
--- drop after the backfill
 id bigint
`

const NESTED_ADD = `diff --git a/notes.md b/notes.md
--- a/notes.md
+++ b/notes.md
@@ -1 +1,2 @@
 # heading
+++ nested marker
`

describe('decodePatch', () => {
  it('splits files and counts lines the way the hard-facts row reads them', () => {
    const got = decodePatch(b64text(TWO_FILES))
    expect(got.files.map((f) => f.path)).toEqual(['src/auth.ts', 'README.md'])
    expect(got.files[0]).toMatchObject({ added: 2, removed: 1 })
    expect(got.files[1]).toMatchObject({ added: 1, removed: 1 })
    // Totals live here so that no renderer ever re-implements the same
    // arithmetic as a per-file sum at render time.
    expect(got).toMatchObject({ added: 3, removed: 2 })
  })

  it('does not count the ---/+++ headers as removed and added lines', () => {
    // The obvious bug in every hand-rolled diff parser: `---` and `+++` begin
    // with the very markers the line classifier keys on, and counting them
    // reads this two-file fixture as +5/-4.
    const got = decodePatch(b64text(TWO_FILES))
    expect(got.added).toBe(3)
    expect(got.removed).toBe(2)
  })

  it('keeps hunk lines classified for rendering', () => {
    const [file] = decodePatch(b64text(TWO_FILES)).files
    expect(file?.hunks).toHaveLength(1)
    expect(file?.hunks[0]?.header).toBe('@@ -1,4 +1,5 @@')
    expect(file?.hunks[0]?.lines.map((l) => l.kind)).toEqual([
      'context',
      'del',
      'add',
      'add',
      'context',
    ])
    // The leading marker is stripped: a renderer that colours by `kind` must
    // not also print the character it coloured for.
    expect(file?.hunks[0]?.lines[1]?.text).toBe('export function login() {}')
  })

  it('survives a NON-UTF-8 payload and says that it was lossy', () => {
    // THE CASE THE BASE64 CONTRACT EXISTS FOR (collect.go). A latin-1 é (0xE9)
    // is not valid UTF-8; the bytes reach the client intact precisely because
    // they were never put in a JSON string, and the client must still render
    // something rather than throwing — while saying the text it shows is not
    // exactly the bytes.
    const bytes = new Uint8Array([
      ...new TextEncoder().encode('--- a/legacy.txt\n+++ b/legacy.txt\n@@ -1 +1 @@\n-caf'),
      0xe9,
      ...new TextEncoder().encode('\n+cafe\n'),
    ])
    const got = decodePatch(b64(bytes))
    expect(got.lossy).toBe(true)
    expect(got.files.map((f) => f.path)).toEqual(['legacy.txt'])
    expect(got).toMatchObject({ added: 1, removed: 1 })
  })

  it('a clean UTF-8 patch is not marked lossy', () => {
    // Without this, a hardcoded `lossy: true` would pass the test above — the
    // flag would only ever be proved settable, never computed.
    expect(decodePatch(b64text(TWO_FILES)).lossy).toBe(false)
  })

  it('marks a binary file rather than pretending it has hunks', () => {
    const p = `diff --git a/logo.png b/logo.png
Binary files a/logo.png and b/logo.png differ
`
    const [file] = decodePatch(b64text(p)).files
    expect(file).toMatchObject({ path: 'logo.png', binary: true, added: 0, removed: 0 })
  })

  it('keeps a deleted "-- " line inside its hunk instead of reading it as a header', () => {
    // A deleted SQL comment renders as `--- …` — the del marker plus a line
    // that itself begins `-- `, and this repo's own migrations are full of
    // those. Read as a `---` file header it closes the hunk mid-content,
    // loses the remaining counts and pushes a phantom file with an empty
    // path; the owed-content guard is what keeps it a del line.
    const got = decodePatch(b64text(SQL_COMMENTS))
    expect(got.files.map((f) => f.path)).toEqual(['db/schema.sql'])
    expect(got).toMatchObject({ added: 0, removed: 2 })
    const [file] = got.files
    expect(file?.hunks).toHaveLength(1)
    // git appends the enclosing section after the trailing `@@`; the counts
    // still parse and `header` keeps the whole line.
    expect(file?.hunks[0]?.header).toBe('@@ -1,4 +1,2 @@ create table runs')
    expect(file?.hunks[0]?.lines).toEqual([
      { kind: 'context', text: 'create table runs (' },
      { kind: 'del', text: '-- legacy column: kept while the old reader was alive' },
      { kind: 'del', text: '-- drop after the backfill' },
      { kind: 'context', text: 'id bigint' },
    ])
  })

  it('counts an added line that begins "++ " instead of rewriting the path', () => {
    // The `+++` half of the same defect: an added line whose text begins
    // `++ ` renders as `+++ …`, and read as a header it rewrites the file's
    // path instead of counting as an add.
    const got = decodePatch(b64text(NESTED_ADD))
    expect(got.files.map((f) => f.path)).toEqual(['notes.md'])
    expect(got).toMatchObject({ added: 1, removed: 0 })
    expect(got.files[0]?.hunks[0]?.lines).toEqual([
      { kind: 'context', text: '# heading' },
      { kind: 'add', text: '++ nested marker' },
    ])
  })

  it('a "No newline at end of file" marker is neither content nor a count', () => {
    const p = `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-old
\\ No newline at end of file
+new
\\ No newline at end of file
`
    const got = decodePatch(b64text(p))
    expect(got).toMatchObject({ added: 1, removed: 1 })
    // The marker annotates the line before it; emitted as context it would
    // put the text " No newline at end of file" inside the rendered diff.
    expect(got.files[0]?.hunks[0]?.lines).toEqual([
      { kind: 'del', text: 'old' },
      { kind: 'add', text: 'new' },
    ])
  })

  it('an empty or unparseable payload yields an empty patch, never a throw', () => {
    // A payload the server accepted must never take a screen down: the caller
    // gets an empty patch to render as "nothing to show", not a throw.
    expect(decodePatch('')).toMatchObject({ files: [], added: 0, removed: 0 })
    expect(() => decodePatch('!!!not base64!!!')).not.toThrow()
  })
})
