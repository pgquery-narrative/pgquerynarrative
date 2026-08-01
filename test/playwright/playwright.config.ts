import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:18088";
const recordVideo = process.env.DEMO_CAPTURE === "1";

export default defineConfig({
  testDir: ".",
  // Marketing capture only — run via: DEMO_CAPTURE=1 npx playwright test demo-workflow.spec.ts
  testIgnore: recordVideo ? [] : ["**/demo-workflow.spec.ts"],
  timeout: recordVideo ? 300_000 : 60_000,
  expect: { timeout: recordVideo ? 30_000 : 15_000 },
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL,
    trace: "on-first-retry",
    // Capture at 1280×720; encode script downscales to 720px GIF for README load time.
    video: recordVideo ? { mode: "on", size: { width: 1280, height: 720 } } : "off",
    ...devices["Desktop Chrome"],
    ...(recordVideo ? { viewport: { width: 1280, height: 720 } } : {}),
  },
});
