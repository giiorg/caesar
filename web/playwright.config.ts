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
})
