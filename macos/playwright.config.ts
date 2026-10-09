import { defineConfig, devices } from "@playwright/test";

// The app in WebKit (the engine the Mac app runs on), on the mock pier
// api (mock/api.mjs): its agents stream, ask, plan and finish like real
// ones, without a cloud.
export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: "http://localhost:1421",
    ...devices["Desktop Safari"],
    viewport: { width: 1400, height: 900 },
    colorScheme: "dark",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "app", testIgnore: /perf\.spec/ },
    // A lot of everything: 57 sessions, a 5,000-item chat.
    { name: "perf", testMatch: /perf\.spec/, use: { baseURL: "http://localhost:1422" } },
  ],
  webServer: [
    {
      command: "pnpm exec vite --port 1421",
      url: "http://localhost:1421",
      reuseExistingServer: false,
      env: { PIER_API_BIN: `${process.cwd()}/mock/pier` },
      timeout: 60_000,
    },
    {
      command: "pnpm exec vite --port 1422",
      url: "http://localhost:1422",
      reuseExistingServer: false,
      env: { PIER_API_BIN: `${process.cwd()}/mock/pier`, PIER_MOCK_BIG: "1" },
      timeout: 60_000,
    },
  ],
});
