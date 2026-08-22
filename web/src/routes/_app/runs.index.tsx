import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

// STILL A STUB, and the file moved for a routing reason rather than a
// behavioural one. `/runs/$runId` has to be a SIBLING of the list, and this is
// what `.index` buys — MEASURED by generating the tree both ways: with a
// `runs.tsx` beside `runs.$runId.tsx`, `tsr generate` emits
// `getParentRoute: () => AppRunsRoute` for the detail, making the list its
// PARENT layout; the stub below renders no `<Outlet/>`, so `/runs/run-1` would
// have shown the stub and nothing else. With `runs.index.tsx` both routes get
// `getParentRoute: () => AppRoute` (routeTree.gen.ts), which is the shape
// `approvals.index.tsx` already has.
//
// The real list — `runsQueryOptions(filters)` with `?status=` as URL state,
// rendered as RunCards — arrives with Task 13, which owns RunCard and
// PhaseDots.

export const Route = createFileRoute('/_app/runs/')({ component: Runs })

function Runs() {
  return <StubPanel plan="Plan 12" what="Runs" />
}
