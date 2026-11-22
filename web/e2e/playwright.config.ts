import { defineConfig, devices } from "@playwright/test";

// The server is the real router with in-memory fake services and seed data
// (internal/web/e2eserver). It never calls Claude and needs no database.
const PORT = Number(process.env.E2E_PORT || 8090);

export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  workers: 1, // one shared server with mutable seed data (reset before each test)
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"]],
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    ...devices["Pixel 7"],
    trace: "retain-on-failure",
  },
  projects: [{ name: "mobile-chromium" }],
  webServer: {
    command: `go run ./internal/web/e2eserver -addr 127.0.0.1:${PORT} -generate-delay 300ms`,
    cwd: "../..",
    url: `http://127.0.0.1:${PORT}/login`,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
