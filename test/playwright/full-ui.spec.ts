import { expect, test, type Page } from "@playwright/test";
import { ensureAuthenticated } from "./auth";
import { DEMO_SQL, saveDemoQuery } from "./helpers";

async function expectNoFatalError(page: Page) {
  const crash = page.getByText(/something went wrong|failed to load|application error/i);
  await expect(crash).toHaveCount(0);
}

test.describe("Full UI coverage", () => {
  test.beforeEach(async ({ page }) => {
    await ensureAuthenticated(page);
  });

  test("home dashboard loads entry points and evidence", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: /Investigate queries using the evidence/i })).toBeVisible();
    await expect(page.getByRole("link", { name: /Investigate a query/i })).toBeVisible();
    await expect(page.getByRole("link", { name: /Security & Trust/i })).toBeVisible();
    await expect(page.getByRole("heading", { name: "PostgreSQL evidence" })).toBeVisible();
    await expect(page.getByText("Queries observed")).toBeVisible({ timeout: 20_000 });
    await expectNoFatalError(page);
  });

  test("sidebar navigation reaches every primary page", async ({ page }) => {
    const routes: { path: string; heading: RegExp }[] = [
      { path: "/", heading: /Investigate queries using the evidence/i },
      { path: "/investigate", heading: /Investigate a Query Regression/i },
      { path: "/query", heading: /PostgreSQL Analytics Workbench/i },
      { path: "/stats", heading: /Query stats/i },
      { path: "/saved", heading: /Saved Queries/i },
      { path: "/reports", heading: /Reports/i },
      { path: "/dashboards", heading: /Dashboards/i },
      { path: "/schedules", heading: /Schedules/i },
      { path: "/security", heading: /Security & Trust/i },
      { path: "/settings", heading: /Settings/i },
    ];

    for (const route of routes) {
      await page.goto(route.path);
      await expect(page.getByRole("heading", { name: route.heading }).first()).toBeVisible({
        timeout: 20_000,
      });
      await expectNoFatalError(page);
    }
  });

  test("theme toggle switches light and dark", async ({ page }) => {
    await page.goto("/");
    const root = page.locator("html");
    const before = await root.getAttribute("class");
    // Accessible name is the visible label ("Dark"/"Light"); title holds the fuller hint.
    const toggle = page.getByTitle(/Switch to (light|dark) mode/i);
    await expect(toggle).toBeVisible();
    await toggle.click();
    await expect.poll(async () => root.getAttribute("class")).not.toBe(before);
    await toggle.click();
  });

  test("security trust page shows posture fields", async ({ page }) => {
    await page.goto("/security");
    await expect(page.getByRole("heading", { name: /Security & Trust/i })).toBeVisible();
    await expect(page.getByText("Authentication", { exact: true })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("Allowed schemas", { exact: true })).toBeVisible();
    await expect(page.getByText("demo", { exact: true }).first()).toBeVisible();
    await expect(page.getByText(/What PgQueryNarrative will never do/i)).toBeVisible();
  });

  test("settings page loads configuration cards", async ({ page }) => {
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Database" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "LLM Provider" })).toBeVisible();
    await expect(page.getByText(/ollama/i).first()).toBeVisible({ timeout: 15_000 });
  });

  test("query stats page loads and refresh works", async ({ page }) => {
    await page.goto("/stats");
    await expect(page.getByRole("heading", { name: /Query stats/i })).toBeVisible();
    await page.getByRole("button", { name: /Mean time/i }).click();
    await page.getByRole("button", { name: /Refresh/i }).click();
    // Either rows, empty state, or a soft error about pg_stat_statements — never a blank crash.
    await expect
      .poll(
        async () => {
          if (await page.getByRole("table").isVisible()) return "table";
          if (await page.getByText(/No statements recorded yet/i).isVisible()) return "empty";
          if (await page.getByText(/Failed to load query stats|pg_stat_statements/i).isVisible()) {
            return "error";
          }
          return "pending";
        },
        { timeout: 20_000 },
      )
      .not.toBe("pending");
  });

  test("workbench explain produces plan findings", async ({ page }) => {
    await page.goto("/query");
    await expect(page.getByRole("heading", { name: "PostgreSQL Analytics Workbench" })).toBeVisible();
    await page.getByTestId("query-sql").fill(DEMO_SQL);
    await page.getByRole("button", { name: /^Explain$/i }).click();
    // Prefer plan tab; also accept total-cost text if the plan panel auto-opens.
    await expect
      .poll(
        async () => {
          if (await page.getByRole("button", { name: /^Plan$/i }).isVisible()) return "tab";
          if (await page.getByText(/Total cost/i).isVisible()) return "cost";
          if (await page.getByRole("alert").isVisible()) {
            return `alert:${await page.getByRole("alert").innerText()}`;
          }
          return "pending";
        },
        { timeout: 45_000 },
      )
      .toMatch(/^(tab|cost)$/);
    if (await page.getByRole("button", { name: /^Plan$/i }).isVisible()) {
      await page.getByRole("button", { name: /^Plan$/i }).click();
    }
    await expect(page.getByText(/Total cost/i)).toBeVisible({ timeout: 15_000 });
  });

  test("investigate guided demo scenario end-to-end", async ({ page }) => {
    test.setTimeout(180_000);
    await page.goto("/investigate");
    await expect(page.getByRole("heading", { name: /Investigate a Query Regression/i })).toBeVisible();
    // Prefer the card heading — page copy also says "guided demo", which makes a
    // getByText(/…|Guided demo/i) locator match twice (strict mode violation).
    await expect(page.getByRole("heading", { name: /Sample problem queries/i })).toBeVisible();

    const scenario = page.getByRole("button", { name: /Slow dashboard query/i });
    await expect(scenario).toBeVisible({ timeout: 20_000 });
    await scenario.click();

    // Landing badge also says "Query Investigation"; wait for the created detail route.
    await expect(page).toHaveURL(/\/investigate\/[0-9a-f-]{36}/i, { timeout: 60_000 });
    // Explain finished once the verdict and the (collapsed) raw plan section render.
    await expect(page.getByText(/Raw plan analysis/i)).toBeVisible({ timeout: 90_000 });
    await expect(page.getByTestId("investigation-verdict")).toBeVisible({ timeout: 30_000 });

    // Candidate must come from the rewrite engine, not a demo answer key.
    const suggest = page.getByRole("button", { name: /Suggest rewrite/i });
    await expect(suggest).toBeVisible();
    await suggest.click();
    await expect(page.locator("textarea").first()).not.toHaveValue("", { timeout: 30_000 });

    const compare = page.getByRole("button", { name: /Compare plans/i });
    if (await compare.isVisible()) {
      await compare.click();
      await expect(page.getByText(/Candidate|improvement|comparison|cost|Partitions/i).first()).toBeVisible({
        timeout: 60_000,
      });
    }

    await page.getByRole("button", { name: /Generate report/i }).click();
    await expect(page).toHaveURL(/\/reports\/[0-9a-f-]+/i, { timeout: 90_000 });
    // Investigation reports are deterministic evidence packages (title-based headline),
    // not the mock LLM narrative used by Workbench "Generate Report".
    await expect(page.getByTestId("report-detail-headline")).toContainText(/Slow dashboard query/i, {
      timeout: 30_000,
    });
    await expect(page.getByTestId("report-share-panel")).toBeVisible();
  });

  // Dashboards mutations stay admin-only; OIDC E2E users are analysts (403 on POST).
  // Coverage for the list page is in "sidebar navigation reaches every primary page".

  test("saved queries open and delete flow", async ({ page }) => {
    const saved = await saveDemoQuery(page, "pw-saved-ui");
    await page.goto("/saved");
    await expect(page.getByTestId("saved-queries-page")).toBeVisible();
    const item = page.getByTestId("saved-query-item").filter({ hasText: saved.name });
    await expect(item).toBeVisible({ timeout: 15_000 });
    await item.getByRole("button").first().click();
    await expect(page.getByRole("button", { name: /Run|Open in workbench|Copy/i }).first()).toBeVisible({
      timeout: 10_000,
    });

    page.once("dialog", (d) => d.accept());
    await item.getByRole("button", { name: /Delete saved query/i }).click();
    await expect(page.getByTestId("saved-query-item").filter({ hasText: saved.name })).toHaveCount(0, {
      timeout: 15_000,
    });
  });

  test("reports list shows generated report", async ({ page }) => {
    await page.goto("/query");
    await page.getByTestId("query-sql").fill(DEMO_SQL);
    await page.getByTestId("query-generate-report").click();
    await expect(page.getByTestId("query-report-headline")).toHaveText("Playwright E2E headline", {
      timeout: 60_000,
    });
    await page.goto("/reports");
    await expect(page.getByTestId("reports-list-page")).toBeVisible();
    await expect(page.getByTestId("report-list-item").filter({ hasText: "Playwright E2E headline" }).first()).toBeVisible({
      timeout: 20_000,
    });
  });

  test("schema browser is visible on workbench", async ({ page }) => {
    await page.goto("/query");
    await expect(page.getByText(/Database schema/i)).toBeVisible();
    await expect(page.getByText(/demo/i).first()).toBeVisible({ timeout: 20_000 });
  });
});
