import type { ReactElement, ReactNode } from 'react'
import type { components } from '../api/schema'

export type Publication = components['schemas']['ApiPublicationResponse']

/**
 * The four values `status` can carry, as a LOCAL union.
 *
 * The wire type is a bare string: `dto.go:364` documents `pending | pushed |
 * published | halted` in a trailing comment and the schema publishes
 * `status: string`, with no enum for openapi-typescript to render. So this list
 * is a client-side narrowing of a wider wire type, and it buys exactly one
 * thing — the switch in `KnownPublication` below is exhaustive over it, so
 * deleting an arm is a compile error HERE. It cannot be a compile error for a
 * fifth value added SERVER-side; that value crosses the wire as a string this
 * file has never heard of, and `narrow` sends it to a panel that says so.
 */
const STATUSES = ['pending', 'pushed', 'published', 'halted'] as const
type PublicationStatus = (typeof STATUSES)[number]
const KNOWN: ReadonlySet<string> = new Set<string>(STATUSES)
const isKnown = (status: string): status is PublicationStatus => KNOWN.has(status)

/**
 * A publish node's publication — where "see the PR" is delivered.
 *
 * A HALTED PUBLICATION IS THE CASE THIS PANEL EXISTS FOR. It is the one status
 * with no pr_url to hang the UI on, so it is the easiest of the four to render
 * as a blank space, and it is the one the operator most needs to see: the run
 * finished its work and did not land it.
 */
export function PublicationPanel(props: { publication: Publication }) {
  const status = props.publication.status
  if (!isKnown(status)) {
    return (
      <Frame publication={props.publication}>
        <p className="text-warning text-sm">
          This publication reports a status this screen does not know: {status}.
        </p>
      </Frame>
    )
  }
  return <KnownPublication status={status} publication={props.publication} />
}

/**
 * The four arms, and the exhaustiveness is the RETURN TYPE rather than a
 * default case. With `strict` on, a switch that stops covering its union leaves
 * an implicit fall-through path returning `undefined`, which `ReactElement`
 * does not include — so dropping an arm is TS2366 here and needs no unreachable
 * `assertNever` branch to say so.
 */
function KnownPublication(props: {
  status: PublicationStatus
  publication: Publication
}): ReactElement {
  const { publication } = props
  switch (props.status) {
    case 'pending':
      return (
        <Frame publication={publication}>
          <p className="text-text-muted text-sm">
            Nothing has been pushed yet — this run's work is still on its way to the branch.
          </p>
        </Frame>
      )
    case 'pushed':
      return (
        <Frame publication={publication}>
          <p className="text-text-muted text-sm">
            The branch is pushed{sha(publication.head_sha)}. No pull request has been opened for it
            yet.
          </p>
        </Frame>
      )
    case 'published':
      return <Frame publication={publication}>{prLink(publication)}</Frame>
    case 'halted':
      return (
        <Frame publication={publication}>
          <p className="text-danger font-medium">Halted</p>
          {/* The reason, verbatim and unabbreviated. It is the only field on
              this response that says WHY the run's work did not land, and it is
              written by the settlement path for a human to read. */}
          <p className="text-text">
            {publication.halt_reason === undefined || publication.halt_reason === ''
              ? 'The server recorded no reason for it.'
              : publication.halt_reason}
          </p>
        </Frame>
      )
  }
}

/**
 * A PR link, or a sentence saying why there is not one.
 *
 * `pr_number` and `pr_url` carry `omitempty` on the wire, so a publication can
 * genuinely reach `published` in this projection with neither — and a link
 * rendered from a missing URL would take the reader to the page they are on.
 */
function prLink(publication: Publication): ReactNode {
  const { pr_number: number, pr_url: url } = publication
  if (number === undefined || url === undefined || url === '') {
    return (
      <p className="text-text-muted text-sm">
        This run published, and the server did not name the pull request it opened.
      </p>
    )
  }
  return (
    <p className="text-text">
      Published as{' '}
      <a className="text-accent underline" href={url}>
        #{number}
      </a>
    </p>
  )
}

/**
 * What every publication shows regardless of status: which branch went where,
 * and the base it was cut from. These five fields are always on the wire
 * (`dto.go:363-367` marks all five required; only the four
 * conditional ones below them carry omitempty), which is why they need no branch.
 */
function Frame(props: { publication: Publication; children: ReactNode }) {
  const { publication } = props
  return (
    <section className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3">
      {props.children}
      <p className="text-text-muted font-mono text-xs">
        {publication.branch} → {publication.target_branch} · base {publication.base_sha}
      </p>
    </section>
  )
}

/** ` at abc1234`, or nothing at all when the SHA is not on the wire. */
function sha(value: string | undefined): string {
  return value === undefined || value === '' ? '' : ` at ${value}`
}
