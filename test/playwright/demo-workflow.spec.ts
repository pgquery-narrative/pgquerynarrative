import { test, expect } from "@playwright/test";

/**
 * Records a short product walkthrough for README/marketing assets.
 * Run: PLAYWRIGHT_BASE_URL=http://127.0.0.1:8080 npx playwright test demo-workflow.spec.ts --config test/playwright/playwright.config.ts
 * Video output: test/playwright/test-results/
 */
test.describe("Demo workflow capture", () => {
  test("query investigation walkthrough", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByText(/Investigate queries using the evidence/i)).toBeVisible();

    await page.getByRole("link", { name: /Start guided demo/i }).click();
    await expect(page.getByText(/Investigate a Query Regression/i)).toBeVisible();

    await page.getByText(/Slow dashboard query/i).first().click();
    await expect(page.getByText(/Query Investigation/i).first()).toBeVisible({ timeout: 60_000 });
    await expect(page.getByRole("heading", { name: /Execution plan/i })).toBeVisible({
      timeout: 60_000,
    });
    await page.waitForTimeout(1200);

    await page.getByRole("button", { name: /Compare plans/i }).click().catch(() => {});
    await page.waitForTimeout(2000);

    await page.getByRole("button", { name: /Generate report/i }).click();
    await page.waitForTimeout(2500);
  });
});
