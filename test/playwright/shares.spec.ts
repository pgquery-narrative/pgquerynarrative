import { expect, test } from "@playwright/test";
import { ensureAuthenticated } from "./auth";
import { generateAndOpenReport, REPORT_HEADLINE } from "./helpers";

test.describe("Report shares", () => {
  test.beforeEach(async ({ page }) => {
    await ensureAuthenticated(page);
  });

  test("create share link, open public view, revoke", async ({ page }) => {
    await generateAndOpenReport(page);
    // Capture this report's detail URL so revoke is not confused with other
    // parallel E2E reports that share the same mock LLM headline.
    const reportDetailURL = page.url();
    expect(reportDetailURL).toMatch(/\/reports\/[0-9a-f-]+/i);

    await expect(page.getByTestId("report-share-panel")).toBeVisible();
    await page.getByTestId("share-create").click();

    await expect(page.getByTestId("share-message")).toContainText(/Share link (copied|created)/i, {
      timeout: 15_000,
    });
    const shareURL = page.getByTestId("share-link-url");
    await expect(shareURL).toBeAttached();
    const path = await shareURL.getAttribute("data-url");
    expect(path).toMatch(/^\/shared\//);

    await expect(
      page.locator('[data-testid="share-item"][data-revoked="false"]').first(),
    ).toBeVisible();

    await page.goto(path!);
    await expect(page.getByTestId("shared-report-headline")).toHaveText(REPORT_HEADLINE, {
      timeout: 15_000,
    });
    // Public shared view must not expose the share management panel.
    await expect(page.getByTestId("report-share-panel")).toHaveCount(0);

    // Revoke from the same authenticated report detail (not headline search).
    await page.goto(reportDetailURL);
    await expect(page.getByTestId("report-share-panel")).toBeVisible();
    await expect(
      page.locator('[data-testid="share-item"][data-revoked="false"]').first(),
    ).toBeVisible({ timeout: 15_000 });
    await page.getByTestId("share-revoke").first().click();
    await expect(page.getByTestId("share-message")).toContainText(/revoked/i, { timeout: 15_000 });

    await page.goto(path!);
    await expect(page.getByText(/not found|Report not found/i)).toBeVisible({ timeout: 15_000 });
  });
});
