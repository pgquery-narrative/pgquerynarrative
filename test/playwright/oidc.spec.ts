import { expect, test } from "@playwright/test";

// Requires PLAYWRIGHT_OIDC=1 and tools/e2e/run-playwright.sh (mock IdP + browser OIDC enabled).
test.describe("Browser OIDC", () => {
  test.skip(!process.env.PLAYWRIGHT_OIDC, "set PLAYWRIGHT_OIDC=1 to run browser login flow");

  test("login via mock IdP establishes session cookie", async ({ page }) => {
    await page.goto("/auth/login");
    await page.waitForURL(/\/auth\/callback/, { timeout: 20_000 });
    await page.waitForURL((url) => !url.pathname.includes("/auth/callback"), { timeout: 20_000 });

    const session = await page.request.get("/auth/session");
    expect(session.ok()).toBeTruthy();
    const body = await session.json();
    expect(body.authenticated).toBe(true);
    expect(body.user_id).toBeTruthy();
  });
});
