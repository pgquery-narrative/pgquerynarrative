import { expect, test } from "@playwright/test";

test.describe("UI smoke", () => {
  test("health and ready endpoints respond", async ({ request }) => {
    const health = await request.get("/health");
    expect(health.ok()).toBeTruthy();
    expect(await health.text()).toBe("OK");

    const ready = await request.get("/ready");
    expect(ready.ok()).toBeTruthy();
  });

  test("SPA shell loads", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator("#root")).toBeVisible();
  });

  test("query runner page renders", async ({ page }) => {
    await page.goto("/query");
    await expect(page.getByRole("heading", { name: "Query Runner" })).toBeVisible();
  });
});
