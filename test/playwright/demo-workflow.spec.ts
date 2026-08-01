import { test, expect } from "@playwright/test";

/**
 * Records a short product walkthrough for README/marketing assets.
 * Requires a running app with large demo.sales seed (make demo-bootstrap).
 * Run via: make demo-gif
 */
test.describe("Demo workflow capture", () => {
  test("query investigation walkthrough", async ({ page }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await expect(page.getByText(/Investigate queries using the evidence/i)).toBeVisible();

    await page.getByRole("link", { name: /Start guided demo/i }).click();
    await expect(page.getByText(/Investigate a Query Regression/i)).toBeVisible();
    await expect(page.getByText(/Many partitions → 1 month/i).first()).toBeVisible();

    await page.getByText(/Slow dashboard query/i).first().click();
    await expect(page.getByText(/Query Investigation/i).first()).toBeVisible({ timeout: 120_000 });
    await expect(page.getByRole("heading", { name: /Execution plan/i })).toBeVisible({
      timeout: 120_000,
    });
    // DATE_TRUNC / partition-pruning findings should be high-confidence, not "Low".
    await expect(page.getByText(/date_trunc|function-wrapped|no pruning/i).first()).toBeVisible({
      timeout: 30_000,
    });
    await page.waitForTimeout(2000);

    await page.getByRole("button", { name: /Compare plans/i }).click();
    await expect(page.getByRole("cell", { name: "Partitions scanned" })).toBeVisible({ timeout: 120_000 });
    await expect(page.getByRole("cell", { name: "Total cost" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Execution time" })).toBeVisible();
    // Reject the old credibility bugs in the capture itself.
    await expect(page.getByText("-100.0%")).toHaveCount(0);
    await page.waitForTimeout(2500);

    await page.getByRole("button", { name: /Generate report/i }).click();
    await expect(page.getByText(/Query Investigation: Slow dashboard query/i)).toBeVisible({
      timeout: 60_000,
    });
    await page.waitForTimeout(2500);
  });
});
