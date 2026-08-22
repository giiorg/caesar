// Shared setup for the `dom` vitest project.

// Registers jest-dom's matchers (`toBeInTheDocument`, `toHaveTextContent`,
// `toHaveValue`) on vitest's `expect`. Task 6's login suite is the first to
// call them; without this the failure is `Invalid Chai property:
// toBeInTheDocument`, which reads like a typo rather than a missing import.
import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './msw'

// MSW intercepts at the NETWORK level for every `dom` test, so the generated
// client is exercised rather than stubbed. `onUnhandledRequest: 'error'` is the
// load-bearing half: without it a request the suite forgot to stub would fall
// through to the real network (or to jsdom's "not implemented"), and the test
// would fail somewhere far away from the missing handler.
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => server.resetHandlers())
afterAll(() => server.close())

// React Testing Library registers its own `afterEach(cleanup)` ONLY when a
// global `afterEach` exists. vitest.config.ts sets `globals: false` for this
// project, so there is none and RTL's auto-cleanup never runs: without the line
// below, the DOM rendered by one test survives into the next one in the same
// file. MEASURED before this was added: two tests rendering <p>first</p> and
// <p>second</p> left `document.body.textContent === 'firstsecond'`, and two
// renders of the same component made `getByRole` throw "Found multiple
// elements". src/test/setup.test.tsx is the regression guard.
afterEach(cleanup)

// jsdom does not implement `window.matchMedia` (MEASURED: `typeof
// window.matchMedia === 'undefined'` under the jsdom this project pins), so any
// component test that mounts a tree containing useTheme would crash with
// "window.matchMedia is not a function". Give every DOM test a default that
// reports no preference; a test whose SUBJECT is the preference overrides it
// with `vi.stubGlobal('matchMedia', …)`, and `unstubGlobals` restores this
// default afterwards.
//
// Assigned rather than stubbed on purpose: `vi.stubGlobal` here would record
// "originally undefined", and the first `vi.unstubAllGlobals()` would delete
// matchMedia outright instead of restoring the default.
function noPreferenceMatchMedia(query: string): MediaQueryList {
  return {
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  } as MediaQueryList
}

if (typeof window.matchMedia !== 'function') {
  window.matchMedia = noPreferenceMatchMedia
}

// jsdom implements NO PART of the Pointer Capture API — `setPointerCapture`,
// `releasePointerCapture` and `hasPointerCapture` are all absent from
// `Element.prototype` (MEASURED under the jsdom this project pins). Every real
// browser has all three, so this closes a gap in the fake rather than giving it
// a capability reality lacks.
//
// It is not optional decoration. `vaul`'s drawer arms a pointer-drag on its
// content, so the FIRST `userEvent.type` inside the new-task sheet throws
// `event.target.setPointerCapture is not a function` — asynchronously, out of a
// React event handler, where no assertion can catch it. MEASURED before this
// was added: `tasks.test.tsx` reported 11 tests PASSED and 19 unhandled errors,
// and vitest exited 1. A suite that is green and failing at once is the worst
// available outcome, because the exit code is the only thing that says so.
//
// `hasPointerCapture` returns false rather than tracking state: nothing in this
// tree reads it back after setting it, and a real implementation would be a
// second, untested one.
if (typeof Element.prototype.setPointerCapture !== 'function') {
  Element.prototype.setPointerCapture = () => {}
  Element.prototype.releasePointerCapture = () => {}
  Element.prototype.hasPointerCapture = () => false
}
