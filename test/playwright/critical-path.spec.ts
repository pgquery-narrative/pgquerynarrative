import { expect, test } from "@playwright/test";

const DEMO_SQL =
  "SELECT product_category, COUNT(*)::int AS n FROM demo.sales GROUP BY 1 ORDER BY 1 LIMIT 5";

test.describe("Critical path", () => {
  test("empty SQL run shows an alert", async ({ page }) => {
    await page.goto("/query");
    await page.getByTestId("query-run").click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("alert")).toContainText(/empty/i);
  });

  test("run query, save, list saved, generate report", async ({ page }) => {
    const saveName = `pw-e2e-${Date.now()}`;

    await page.goto("/query");
    await page.getByTestId("query-sql").fill(DEMO_SQL);
    await page.getByTestId("query-run").click();

    await expect(page.getByTestId("query-results")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("query-row-count")).toContainText(/\d+ rows/);
    await expect(page.getByTestId("query-results").locator("tbody tr").first()).toBeVisible();

    await page.getByTestId("query-save-name").fill(saveName);
    await page.getByTestId("query-save").click();
    await expect(page.getByTestId("query-save-success")).toContainText(saveName, {
      timeout: 15_000,
    });

    await page.goto("/saved");
    await expect(page.getByTestId("saved-queries-page")).toBeVisible();
    await expect(
      page.getByTestId("saved-queries-list").getByTestId("saved-query-item").filter({
        hasText: saveName,
      }),
    ).toBeVisible({ timeout: 15_000 });

    await page.goto("/query");
    await page.getByTestId("query-sql").fill(DEMO_SQL);
    await page.getByTestId("query-generate-report").click();
    await expect(page.getByTestId("query-report-headline")).toHaveText(
      "Playwright E2E headline",
      { timeout: 60_000 },
    );
  });
});
