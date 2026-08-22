import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  // Mobile-viewport project only: the phone is the flagship surface, and a
  // desktop project here would double the plan's slowest lane for a viewport
  // Plan 12 owns.
  projects: [{ name: 'mobile', use: { ...devices['Pixel 7'] } }],
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
  workers: 1,
  // Playwright's default is 30s PER TEST, which is not a budget these paths
  // fit: a revision round is two more fake executions plus the orchestrator's
  // poll on each side of them, and the specs' own `{ timeout: 60_000 }`
  // assertions cannot outlive the test that contains them — a 30s default
  // silently caps them at 30s and turns a stated timeout into a fiction. The
  // Go lane allows 15m for everything (`Taskfile.yml`, web:e2e), so this is
  // generous rather than tight; the per-assertion timeouts stay the real
  // deadlines.
  timeout: 120_000,
})
