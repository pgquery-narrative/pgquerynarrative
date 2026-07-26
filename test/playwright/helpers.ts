import { expect, type Page } from "@playwright/test";
import { ensureAuthenticated } from "./auth";

export const DEMO_SQL =
  "SELECT product_category, COUNT(*)::int AS n FROM demo.sales GROUP BY 1 ORDER BY 1 LIMIT 5";

export const REPORT_HEADLINE = "Playwright E2E headline";

/** Login when OIDC is on, generate a report from demo SQL, open report detail. */
export async function generateAndOpenReport(page: Page): Promise<void> {
  await ensureAuthenticated(page);
  await page.goto("/query");
  await page.getByTestId("query-sql").fill(DEMO_SQL);
  await page.getByTestId("query-generate-report").click();
  await expect(page.getByTestId("query-report-headline")).toHaveText(REPORT_HEADLINE, {
    timeout: 60_000,
  });
  await page.getByTestId("query-report-open").click();
  await expect(page.getByTestId("report-detail-headline")).toHaveText(REPORT_HEADLINE, {
    timeout: 15_000,
  });
}

/** Save a uniquely named query and return its id + name. */
export async function saveDemoQuery(
  page: Page,
  namePrefix = "pw-sched",
): Promise<{ id: string; name: string }> {
  await ensureAuthenticated(page);
  const saveName = `${namePrefix}-${Date.now()}`;
  await page.goto("/query");
  await page.getByTestId("query-sql").fill(DEMO_SQL);
  await page.getByTestId("query-save-name").fill(saveName);
  await page.getByTestId("query-save").click();
  await expect(page.getByTestId("query-save-success")).toContainText(saveName, {
    timeout: 15_000,
  });
  // Confirm the saved query is listable and capture its id for selectOption({ value }).
  let savedId = "";
  await expect
    .poll(
      async () => {
        const res = await page.request.get("/api/v1/queries/saved?limit=100&offset=0");
        if (!res.ok()) return `http:${res.status()}`;
        const body = (await res.json()) as { items?: { id?: string; name?: string }[] };
        const match = (body.items ?? []).find((q) => q.name === saveName);
        if (!match?.id) return "missing";
        savedId = match.id;
        return "ok";
      },
      { timeout: 15_000 },
    )
    .toBe("ok");
  return { id: savedId, name: saveName };
}
