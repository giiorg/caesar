import { Link } from '@tanstack/react-router'
import { PhaseDots } from './phase-dots'
import type { RunSummary } from './runs-query'

/**
 * The relevant timestamp of a run, and the reason the DTO carries three real
 * columns instead of a synthesized `updated_at`.
 *
 * `dto.go:122-125` says it in the contract: "a card renders
 * finished_at ?? started_at ?? created_at, and the choice stays visible".
 * Exported because Home's "Recent" section ORDERS by the same value it
 * displays — a section ordered by one timestamp and captioned with another is
 * one whose order a reader cannot check.
 */
export const runCardTime = (run: RunSummary): string =>
  run.finished_at ?? run.started_at ?? run.created_at

/**
 * One run as a LIST ROW, and the only card Home, the runs list and Tasks use.
 *
 * EVERYTHING IT RENDERS IS ON `RunSummaryResponse`, which is the whole point of
 * that projection existing beside `RunResponse`: a card that had to fetch the
 * task title, the repo label or its phase shape per row is the N+1 that
 * `GET /runs` was widened to remove (`dto.go:97-102`).
 *
 * THE WHOLE CARD IS THE LINK, matching `ApprovalList`: on the phone this list
 * exists for, a target the size of a card is the difference between one tap and
 * an aimed one. Home's GATE cards are the deliberate exception — they carry
 * inline decision buttons, and an interactive control inside an <a> is neither
 * valid HTML nor operable by keyboard — so there the title alone is the link.
 */
export function RunCard(props: { run: RunSummary }) {
  const { run } = props
  const at = runCardTime(run)
  return (
    <Link
      to="/runs/$runId"
      params={{ runId: run.id }}
      data-testid="run-card"
      className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3"
    >
      <p className="text-text font-medium">{run.task_title}</p>
      <PhaseDots phases={run.phase_summary} />
      <p className="text-text-muted font-mono text-xs">
        {run.repo_slug} · {run.workflow_name} · {run.status} ·{' '}
        {/* Raw ISO for V1, in a <time> so the machine-readable value is where a
            reader (and a later formatter) expects it — the same shape
            `ApprovalList` already uses for `waiting_since`. */}
        <time dateTime={at}>{at}</time>
      </p>
    </Link>
  )
}
