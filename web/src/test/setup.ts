// Shared setup for the `dom` vitest project.

// Registers jest-dom's matchers (`toBeInTheDocument`, `toHaveTextContent`,
// `toHaveValue`) on vitest's `expect`. Task 6's login suite is the first to
// call them; without this the failure is `Invalid Chai property:
// toBeInTheDocument`, which reads like a typo rather than a missing import.
import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

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
