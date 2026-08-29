import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  // TWO VIEWPORTS, ONE CRITICAL PATH. 12a ran mobile alone and said why: "a
  // desktop project here would double the plan's slowest lane for a viewport
  // Plan 12 owns". This is that plan. The cost is real and accepted, and it
  // buys the only place three claims are falsifiable at all — the rail paints
  // left of <main>, run detail is two-pane, and the phone bar is gone — none of
  // which jsdom can judge, because it loads no Tailwind and applies no media
  // queries.
  //
  // WHAT IS PER-PROJECT AND WHAT IS NOT. The shared critical path is ONE set of
  // specs that both projects run; only assertions about CHROME are branched,
  // and they are branched inside the spec that makes them (`approvals.spec.ts`)
  // rather than duplicated into a second file.
  //
  // `testIgnore` AND NOT `test.skip`. The plan asked for
  // `test.skip(({}, testInfo) => …)`, which does not exist: `ConditionBody` is
  // `(args: TestArgs) => boolean` (types/test.d.ts:2725), one parameter, no
  // testInfo — and `({}, …)` is an empty object pattern, which biome's
  // recommended `noEmptyPattern` rejects. The failure modes also differ the
  // right way round: a pattern that matches nothing lets the file run on mobile
  // and fail loudly at 412px, where a mistyped skip condition would skip every
  // test and stay green.
  projects: [
    {
      name: 'mobile',
      use: { ...devices['Pixel 7'] },
      testIgnore: /desktop-geometry\.spec\.ts/,
    },
    // THE MILESTONE WALK IS PHONE-ONLY, and for two reasons rather than taste.
    // The conveyor's own screen is the phone (§1: the gate arrives on a phone
    // and is decided there), and the desktop pass over these surfaces is the
    // operator checkpoint's job, not a lane's. The mechanical one is stronger:
    // the walk opens its gate by tapping the phase row's disclosure, and that
    // <button> is `md:hidden` (`runs.$runId.tsx`) — at 1280px a closed row
    // leaves the pane entirely and the rail is what selects a phase. A desktop
    // arm would be a branch through the middle of the walk, buying a second
    // spelling of a claim that is not about chrome at all.
    {
      name: 'desktop',
      use: { ...devices['Desktop Chrome'] },
      testIgnore: /milestone-walk\.spec\.ts/,
    },
  ],
  use: { baseURL: process.env.CAESAR_BASE_URL },
  // The Go harness owns the server's lifecycle, so there is no webServer here.
  forbidOnly: true,
  retries: 0,
  reporter: [['list']],
  // SERIAL, BECAUSE THERE IS ONE WORLD. `webe2e_test.go` boots ONE server over
  // ONE database for the whole run, and the approvals inbox is a global count
  // over it. Playwright's default is a worker per spec file, which would have
  // three files opening and deciding gates in each other's counts — and
  // `harness.ts`'s `quiesce`, which is what lets a spec assert an absolute
  // badge count at all, would have each worker clearing the others' gates.
  // Parallelism buys little here regardless: the lane's cost is the Postgres
  // container and the migrations, both of which happen once before any spec.
  //
  // THE SECOND PROJECT MAKES "ONE WORLD" STRICTER, NOT LOOSER. Every spec file
  // now runs TWICE against that single database, so a spec that writes a
  // uniquely-constrained row can collide WITH ITSELF — `workflow-editor.spec.ts`
  // did, on `workflows.name`'s UNIQUE index, and its name is per-project for
  // exactly that reason. One worker keeps the two passes serial as well as the
  // files within a pass, which is what leaves `quiesce` sufficient.
  workers: 1,
  // Playwright's default is 30s PER TEST, which is not a budget these paths
  // fit: a revision round is two more fake executions plus the orchestrator's
  // poll on each side of them, and the specs' own `{ timeout: 60_000 }`
  // assertions cannot outlive the test that contains them — a 30s default
  // silently caps them at 30s and turns a stated timeout into a fiction. The
  // Go lane allows 15m for everything (`Taskfile.yml`, web:e2e), so this is
  // generous rather than tight, and no single assertion's budget is capped by
  // it. It does not cover their SUM: `task-timeline.spec.ts` states 150s across
  // three waits, so a test that spent every one of them in full would still be
  // cut off here. That is a test already failing, and the alternative is worse
  // — a ceiling above 150s puts a handful of hung tests within reach of the
  // lane's own 15m, where the failure arrives as a killed lane with no
  // Playwright report rather than as a named assertion.
  timeout: 120_000,
})
