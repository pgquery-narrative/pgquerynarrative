import { expect, type Page } from "@playwright/test";

/** When PLAYWRIGHT_OIDC=1, establish a browser session via the mock IdP. */
export async function ensureAuthenticated(page: Page): Promise<void> {
  if (process.env.PLAYWRIGHT_OIDC !== "1") {
    return;
  }

  const existing = await page.request.get("/auth/session");
  if (existing.ok()) {
    const body = (await existing.json()) as { authenticated?: boolean };
    if (body.authenticated) {
      return;
    }
  }

  await page.goto("/auth/login", { waitUntil: "domcontentloaded" });
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
}
