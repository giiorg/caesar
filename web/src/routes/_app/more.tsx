import { createFileRoute, Link } from '@tanstack/react-router'
import { SECONDARY } from '../../shell/nav'

/**
 * The fifth tab. It is a real page and not a menu overlay: a bottom sheet here
 * would be one more thing to dismiss on the way to a page a person visits
 * rarely, and it could not be linked to or restored on reload.
 *
 * It renders `SECONDARY` rather than a list of its own, so a page added to the
 * app cannot reach the desktop rail and miss the phone — the exact asymmetry
 * this task exists to correct.
 */
export const Route = createFileRoute('/_app/more')({ component: More })

function More() {
  return (
    <ul className="flex flex-col gap-2">
      {SECONDARY.map((item) => (
        <li key={item.to}>
          <Link
            to={item.to}
            className="border-border bg-surface text-text block rounded-md border p-3"
          >
            {item.label}
          </Link>
        </li>
      ))}
    </ul>
  )
}
