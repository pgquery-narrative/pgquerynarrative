import { expect, test } from "@playwright/test";

// Requires PLAYWRIGHT_OIDC=1 and tools/e2e/run-playwright.sh (mock IdP + browser OIDC enabled).
test.describe("Browser OIDC", () => {
  test.skip(process.env.PLAYWRIGHT_OIDC !== "1", "set PLAYWRIGHT_OIDC=1 to run browser login flow");

  test("login via mock IdP establishes session cookie", async ({ page }) => {
    await page.goto("/auth/login", { waitUntil: "domcontentloaded" });
    // Full redirect chain can finish before we observe /auth/callback; settle on a non-auth path.
    await page.waitForURL(
      (url) =>
        !url.pathname.startsWith("/auth/") &&
        !url.pathname.includes("/oauth/") &&
        !url.href.includes("openid-configuration"),
      { timeout: 30_000 },
    );

    await expect
      .poll(
        async () => {
          const session = await page.request.get("/auth/session");
          if (!session.ok()) return false;
          const body = (await session.json()) as { authenticated?: boolean };
          return Boolean(body.authenticated);
        },
        { timeout: 15_000 },
      )
      .toBe(true);
  });
});
