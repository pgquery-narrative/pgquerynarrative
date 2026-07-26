import { expect, test } from "@playwright/test";
import { ensureAuthenticated } from "./auth";
import { saveDemoQuery } from "./helpers";

test.describe("Schedules", () => {
  test.beforeEach(async ({ page }) => {
    await ensureAuthenticated(page);
  });

  test("create schedule from saved query and run now", async ({ page }) => {
    const saved = await saveDemoQuery(page);
    const scheduleName = `pw-sched-${Date.now()}`;

    await page.goto("/schedules");
    await expect(page.getByTestId("schedules-page")).toBeVisible();

    await page.getByTestId("schedule-name").fill(scheduleName);
    // Wait until the saved-query option is present (list loads async after navigation).
    await expect
      .poll(
        async () => {
          return page
            .getByTestId("schedule-saved-query")
            .locator(`option[value="${saved.id}"]`)
            .count();
        },
        { timeout: 20_000 },
      )
      .toBe(1);
    await page.getByTestId("schedule-saved-query").selectOption(saved.id);
    await page.getByTestId("schedule-interval").fill("@every 1h");
    await page.getByTestId("schedule-create").click();

    const card = page.getByTestId("schedule-item").filter({ hasText: scheduleName });
    await expect(card).toBeVisible({ timeout: 15_000 });
    await expect(card).toContainText("enabled");

    await card.getByTestId("schedule-run-now").click();
    await expect
      .poll(
        async () => {
          const status = card.getByTestId("schedule-last-status");
          if (!(await status.isVisible())) return "pending";
          const text = await status.innerText();
          if (/failed|error/i.test(text)) return `fail:${text}`;
          if (/completed|success|ok/i.test(text)) return "ok";
          if (/running|never/i.test(text)) return "pending";
          return text;
        },
        { timeout: 90_000 },
      )
      .toBe("ok");
  });
});
