import { test } from "@playwright/test";
import { ensureAuthenticated } from "./auth";

// Requires PLAYWRIGHT_OIDC=1 and tools/e2e/run-playwright.sh (mock IdP + browser OIDC enabled).
test.describe("Browser OIDC", () => {
  test.skip(process.env.PLAYWRIGHT_OIDC !== "1", "set PLAYWRIGHT_OIDC=1 to run browser login flow");

  test("login via mock IdP establishes session cookie", async ({ page }) => {
    await ensureAuthenticated(page);
  });
});
