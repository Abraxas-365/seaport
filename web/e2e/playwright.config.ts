import { defineConfig, devices } from "@playwright/test"

// Browser tests of the admin web app. `go test -run TestUI ./internal/bootstrap/`
// (SEAPORT_TEST_UI=1) starts the port, the fakes and the echo agent, then runs
// this with SEAPORT_UI_URL / SEAPORT_UI_TOKEN / SEAPORT_UI_CONTEXT set.
const baseURL = process.env.SEAPORT_UI_URL
if (!baseURL) throw new Error("SEAPORT_UI_URL is not set: run these through `SEAPORT_TEST_UI=1 go test -run TestUI ./internal/bootstrap/`")

export default defineConfig({
  testDir: "./tests",
  outputDir: "test-results",
  // The specs share one port and one echo agent, one after the other.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 30_000 },
  reporter: process.env.CI ? [["list"]] : [["list"], ["html", { open: "never", outputFolder: "playwright-report" }]],
  use: {
    baseURL,
    trace: "retain-on-failure",
    actionTimeout: 30_000,
    navigationTimeout: 30_000,
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 900 } } }],
})
