import { expect, test } from "@playwright/test";
import { ensureAuthenticated } from "./auth";

const DEMO_SQL =
  "SELECT product_category, COUNT(*)::int AS n FROM demo.sales GROUP BY 1 ORDER BY 1 LIMIT 5";

test.describe("Critical path", () => {
  test.beforeEach(async ({ page }) => {
    await ensureAuthenticated(page);
  });

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

    // Prefer results; surface API/UI errors if the query failed.
    const results = page.getByTestId("query-results");
    const alert = page.getByRole("alert");
    await expect
      .poll(
        async () => {
          if (await results.isVisible()) return "results";
          if (await alert.isVisible()) return "alert";
          return "pending";
        },
        { timeout: 30_000 },
      )
      .toBe("results");
    await expect(page.getByTestId("query-row-count")).toContainText(/\d+ rows/);
    await expect(results.locator("tbody tr").first()).toBeVisible();

    await page.getByTestId("query-save-name").fill(saveName);
    await page.getByTestId("query-save").click();
    await expect
      .poll(
        async () => {
          if (await page.getByTestId("query-save-success").isVisible()) return "ok";
          if (await page.getByRole("alert").isVisible()) {
            return `alert:${await page.getByRole("alert").innerText()}`;
          }
          return "pending";
        },
        { timeout: 15_000 },
      )
      .toBe("ok");
    await expect(page.getByTestId("query-save-success")).toContainText(saveName);

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
