import { test, expect, type Page } from "@playwright/test";

/**
 * Records a paced product walkthrough for the README GIF.
 * Requires a running app with large demo.sales seed (make demo-bootstrap).
 * Run via: make demo-gif
 *
 * Captions are injected into the page during recording so they stay
 * frame-accurate after ffmpeg conversion (no separate subtitle timing).
 */
test.describe("Demo workflow capture", () => {
  test("query investigation walkthrough", async ({ page }) => {
    test.setTimeout(300_000);

    await page.goto("/");
    await expect(page.getByText(/Investigate queries using the evidence/i)).toBeVisible();
    await showCaption(page, "PgQueryNarrative — Query Investigation");
    await page.waitForTimeout(2500);

    await page.getByRole("link", { name: /Start guided demo/i }).click();
    await expect(page.getByText(/Investigate a Query Regression/i)).toBeVisible();
    await expect(page.getByText(/Many partitions → 1 month/i).first()).toBeVisible();
    await showCaption(page, "1. Start from a guided regression scenario");
    await page.waitForTimeout(3000);

    await page.getByText(/Slow dashboard query/i).first().click();
    await expect(page.getByText(/Query Investigation/i).first()).toBeVisible({ timeout: 120_000 });
    await expect(page.getByRole("heading", { name: /Execution plan/i })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByText(/date_trunc|function-wrapped|no pruning/i).first()).toBeVisible({
      timeout: 30_000,
    });
    await showCaption(
      page,
      "2. Bad SQL: DATE_TRUNC on the partition key blocks pruning"
    );
    await page.waitForTimeout(4500);

    await showCaption(page, "3. Plan evidence: High findings · many partitions scanned");
    await page.waitForTimeout(4000);

    await page.getByRole("button", { name: /Compare plans/i }).click();
    await expect(page.getByRole("cell", { name: "Partitions scanned" })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByRole("cell", { name: "Total cost" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Execution time" })).toBeVisible();
    await expect(page.getByText("-100.0%")).toHaveCount(0);
    await showCaption(page, "4. Compare rewrite: range predicate · partitions 50 → 1");
    await page.waitForTimeout(5500);

    await page.getByRole("button", { name: /Generate report/i }).click();
    await expect(page.getByText(/Query Investigation: Slow dashboard query/i)).toBeVisible({
      timeout: 60_000,
    });
    await showCaption(page, "5. Engineering report with shareable evidence");
    await page.waitForTimeout(4000);

    await showCaption(page, "Evidence-backed workflow — not LLM-first");
    await page.waitForTimeout(2500);
  });
});

async function showCaption(page: Page, text: string): Promise<void> {
  await page.evaluate((caption) => {
    const id = "pqn-demo-gif-caption";
    let el = document.getElementById(id);
    if (!el) {
      el = document.createElement("div");
      el.id = id;
      el.setAttribute("role", "status");
      el.style.cssText = [
        "position:fixed",
        "left:28px",
        "right:28px",
        "bottom:28px",
        "z-index:2147483647",
        "padding:14px 18px",
        "border-radius:10px",
        "background:rgba(8,12,22,0.88)",
        "border:1px solid rgba(255,255,255,0.14)",
        "color:#f8fafc",
        "font:600 20px/1.35 ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif",
        "letter-spacing:0.01em",
        "box-shadow:0 10px 30px rgba(0,0,0,0.35)",
        "pointer-events:none",
        "text-align:left",
      ].join(";");
      document.body.appendChild(el);
    }
    el.textContent = caption;
  }, text);
}
