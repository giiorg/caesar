import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

/**
 * The definition editor — Task 27 builds it; this task builds the ADDRESS.
 *
 * IT IS HERE BECAUSE TWO OF TASK 24's OWN BEHAVIOURS POINT AT IT and neither
 * can exist without it. The New-workflow flow's second half navigates here
 * after the metadata POST, and a `latest_version: 0` card resumes here rather
 * than opening a detail screen with an empty history. Route strings are
 * compiler-checked through Task 1's `Register` augmentation, so a `<Link>` to
 * an unbuilt route does not typecheck and a navigation assertion against a path
 * the router cannot resolve asserts nothing — the destination has to be real
 * before the journey to it can be.
 *
 * A `StubPanel`, so it says out loud that it is not built. It is also the
 * panel's only consumer now: `routes/_app/workflows.tsx` was the last one and
 * this task deletes it.
 *
 * `$workflowId_` carries the escape suffix for the same reason the version view
 * does — see `workflows.index.tsx`. The editor is a full screen, never a panel
 * under the detail header.
 */
export const Route = createFileRoute('/_app/workflows/$workflowId_/edit')({
  component: WorkflowEditor,
})

function WorkflowEditor() {
  return <StubPanel plan="Plan 12b Task 27" what="The workflow definition editor" />
}
