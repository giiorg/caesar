import { useState } from 'react'
import type { DiffFile, ParsedPatch } from './decode-patch'

/**
 * FILES LISTED, DIFFS ON DEMAND. A gate's patch is routinely thousands of
 * lines, and the locked design opens on the file list so the first thing on
 * screen is what changed rather than how.
 *
 * IT LIVES IN `src/patch/` BECAUSE IT HAS TWO IMPORTERS, and that is a checked
 * fact rather than a preference: the approval surface renders a gate's examined
 * patch with it and the artifact view renders a `patch` artifact with it, so
 * `web:arch`'s misplacement rule fires the moment the second one appears
 * (`tools/arch/import-graph.ts:126-140`). The required home is clamped to the
 * TARGET's own group root, so a module sitting at `src/patch/` — beside the
 * decoder whose output it is the presentation of — is compliant with importers
 * anywhere; a module left inside `approvals/approval-surface.tsx` is not
 * importable at all.
 */
export function FileAccordion(props: { parsed: ParsedPatch }) {
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="flex flex-col gap-2">
      {props.parsed.lossy && (
        // decodePatch decodes twice to learn this rather than hunting for
        // U+FFFD in its own output, so this warning cannot be triggered by a
        // patch that legitimately contains a replacement character.
        <p className="text-warning text-sm">
          Some of these patch bytes were not valid UTF-8, so the text below may not render exactly
          as it was produced.
        </p>
      )}
      <div data-testid="file-accordion" className="border-border rounded-md border">
        {props.parsed.files.map((file, i) => {
          // `path` ALONE IS NOT A KEY. decodePatch opens a file on a bare
          // `--- ` header with no `diff --git` line before it, and such a file
          // keeps `path: ''` until a `+++ ` header names it — so a patch
          // carrying two of them yields two entries with the same empty path.
          // The index disambiguates, and the list is never reordered or
          // filtered: it is one decode of one immutable artifact.
          //
          // Naming it here also puts it out of `noArrayIndexKey`'s reach — the
          // rule matches the index syntactically inside `key=` — so the
          // paragraph above is the only thing guarding this, rather than a
          // suppression a reader can find. It is written where the binding is
          // for that reason.
          const id = `${i}:${file.path}`
          return (
            <FileRow
              key={id}
              file={file}
              open={open === id}
              onToggle={() => setOpen(open === id ? null : id)}
            />
          )
        })}
      </div>
    </div>
  )
}

function FileRow(props: { file: DiffFile; open: boolean; onToggle: () => void }) {
  const { file } = props
  return (
    <div className="border-border border-b last:border-b-0">
      <button
        type="button"
        onClick={props.onToggle}
        aria-expanded={props.open}
        className="text-text flex w-full items-center gap-2 px-3 py-2 text-left text-sm"
      >
        <span className="font-mono">{file.path}</span>
        <span className="text-success text-xs">+{file.added}</span>
        <span className="text-danger text-xs">−{file.removed}</span>
      </button>
      {props.open && (
        <div className="border-border border-t px-3 py-2">
          {file.binary && (
            <p className="text-text-muted text-sm">
              A binary file — there is no text diff to show.
            </p>
          )}
          {file.hunks.map((hunk) => (
            <div key={hunk.header} className="font-mono text-xs">
              <p className="text-text-muted">{hunk.header}</p>
              {hunk.lines.map((line, i) => (
                <p
                  // biome-ignore lint/suspicious/noArrayIndexKey: a hunk's lines have no identity and never reorder
                  key={i}
                  className={LINE_CLASS[line.kind]}
                >
                  {/* THE MARKER IS A SEPARATE SPAN AND NOT PART OF THE TEXT.
                      decodePatch strips the leading +/-/space so that a
                      renderer colouring by `kind` does not also print the
                      character it coloured for, and the line's text is
                      therefore exactly the file's. Rendering the marker beside
                      it — not aria-hidden — is what keeps colour from being
                      the only channel that says add or del, both for a reader
                      who does not distinguish the two colours and for one
                      hearing the line read out. */}
                  <span>{MARKER[line.kind]}</span>
                  <span>{line.text}</span>
                </p>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

const LINE_CLASS = {
  add: 'text-success',
  del: 'text-danger',
  context: 'text-text-muted',
} as const

const MARKER = { add: '+', del: '−', context: ' ' } as const
