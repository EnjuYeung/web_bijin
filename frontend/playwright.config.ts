import { defineConfig } from "@playwright/test";
import { resolve } from "node:path";

export default defineConfig({
  testDir: "./tests",
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: [["list"], ["json", { outputFile: "../output/ui-results.json" }]],
  use: {
    baseURL: "http://127.0.0.1:18092",
    viewport: { width: 1440, height: 1000 },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium", launchOptions: { args: ["--no-sandbox"] } } },
    // Safari's engine, for what logging out leaves behind on iPhones and Macs.
    { name: "webkit", use: { browserName: "webkit" }, testMatch: /logout\.spec\.ts/ },
  ],
  webServer: [
    { command: "python3 tests/s3_fixture.py", port: 18093, reuseExistingServer: false },
    {
      command: "../output/bijin-ui-test",
      url: "http://127.0.0.1:18092/api/health",
      reuseExistingServer: false,
      env: {
        AUTH_USER: "ui-test", AUTH_PASS: "fixture-password", LISTEN: "127.0.0.1:18092",
        PHOTOS_DIR: resolve("../photos"), PHOTOS_HOST_DIR: "/mnt/cache/family-photos",
        DATA_DIR: resolve("../output/ui-data"), SCAN_EVERY: "1h", TZ: "Asia/Shanghai",
      },
    },
    {
      // Same app with two-step verification on; the secret is RFC 6238's test key.
      command: "../output/bijin-ui-test",
      url: "http://127.0.0.1:18094/api/health",
      reuseExistingServer: false,
      env: {
        AUTH_USER: "ui-test", AUTH_PASS: "fixture-password", AUTH_TWO_STEP_SECRET: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
        LISTEN: "127.0.0.1:18094", PHOTOS_DIR: resolve("../photos"),
        DATA_DIR: resolve("../output/ui-data-two-step"), SCAN_EVERY: "1h", TZ: "Asia/Shanghai",
      },
    },
  ],
});
