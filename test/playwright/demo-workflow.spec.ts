import { test, expect, type Page } from "@playwright/test";

/**
 * Compact README GIF. Scroll + highlight each beat; zoom only on compare
 * (dense proof metrics). Requires ≥1M-row demo.sales. Run: make demo-gif
 */
const BAD_SQL =
  "SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' GROUP BY product_category ORDER BY revenue DESC";
const GOOD_SQL = `SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales
WHERE date >= '2025-01-01' AND date < '2025-02-01'
GROUP BY product_category
ORDER BY revenue DESC`;

test.describe("Demo workflow capture", () => {
  test("query investigation walkthrough", async ({ page }) => {
    test.setTimeout(300_000);

    const params = new URLSearchParams({
      title: "Slow dashboard query",
      sql: BAD_SQL,
      candidate: GOOD_SQL,
    });
    await page.goto(`/investigate?${params.toString()}`);

    await installDemoChrome(page);
    await showCaption(page, "Investigating DATE_TRUNC on a partitioned table…");

    await expect(page.getByText(/Query Investigation/i).first()).toBeVisible({ timeout: 120_000 });
    await expect(page.getByRole("heading", { name: /Execution plan/i })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByText(/date_trunc|function-wrapped|no pruning/i).first()).toBeVisible({
      timeout: 60_000,
    });

    // Scroll + highlight only — SQL and findings are readable at full frame.
    await showCaption(page, "DATE_TRUNC on the partition key → no pruning");
    await focusRegion(page, "source-sql");
    await page.waitForTimeout(1100);

    await showCaption(page, "Plan evidence: function-wrapped key blocks pruning");
    await focusRegion(page, "finding");
    await page.waitForTimeout(1400);

    await showCaption(page, "Candidate: sargable month range");
    await focusRegion(page, "candidate");
    await page.waitForTimeout(1100);

    await clearFocus(page);
    await page.getByRole("button", { name: /Compare plans/i }).click();
    await showCaption(page, "Running EXPLAIN ANALYZE compare…");
    await expect(page.getByRole("cell", { name: "Partitions scanned" })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByText("-100.0%")).toHaveCount(0);

    // Zoom only here — compare metrics need to be readable in the GIF.
    await showCaption(page, "Proof: partitions 50 → 1 · ~18× faster");
    await focusRegion(page, "compare", 1.35);
    await page.waitForTimeout(2400);

    await resetCamera(page);
    await page.getByRole("button", { name: /Generate report/i }).click();
    await expect(page.getByText(/Query Investigation: Slow dashboard query/i)).toBeVisible({
      timeout: 60_000,
    });
    await showCaption(page, "Engineering report with plan evidence");
    await focusRegion(page, "report-sql");
    await page.waitForTimeout(1100);
    await clearFocus(page);
    await page.waitForTimeout(350);
  });
});

type FocusTarget = "source-sql" | "finding" | "candidate" | "compare" | "report-sql";

async function installDemoChrome(page: Page): Promise<void> {
  await page.evaluate(() => {
    const styleId = "pqn-demo-gif-style";
    if (!document.getElementById(styleId)) {
      const style = document.createElement("style");
      style.id = styleId;
      style.textContent = `
        #pqn-demo-gif-caption {
          position: fixed; top: 16px; left: 50%; transform: translateX(-50%);
          z-index: 2147483647; max-width: min(920px, calc(100vw - 48px));
          padding: 10px 16px; border-radius: 999px;
          background: rgba(8,12,22,0.92); border: 1px solid rgba(255,255,255,0.16);
          color: #f8fafc; font: 600 16px/1.3 ui-sans-serif, system-ui, sans-serif;
          letter-spacing: 0.01em; box-shadow: 0 8px 24px rgba(0,0,0,0.35);
          pointer-events: none; text-align: center; white-space: nowrap;
          overflow: hidden; text-overflow: ellipsis;
        }
        .pqn-demo-focus {
          outline: 2px solid #22d3ee !important;
          outline-offset: 3px !important;
          border-radius: 8px;
          box-shadow: 0 0 0 6px rgba(34,211,238,0.18) !important;
        }
        html.pqn-demo-zooming, html.pqn-demo-zooming body {
          transition: transform 0.45s ease;
        }
      `;
      document.head.appendChild(style);
    }
  });
}

async function showCaption(page: Page, text: string): Promise<void> {
  await page.evaluate((caption) => {
    let el = document.getElementById("pqn-demo-gif-caption");
    if (!el) {
      el = document.createElement("div");
      el.id = "pqn-demo-gif-caption";
      el.setAttribute("role", "status");
      document.body.appendChild(el);
    }
    el.textContent = caption;
  }, text);
}

async function clearFocus(page: Page): Promise<void> {
  await page.evaluate(() => {
    document.querySelectorAll(".pqn-demo-focus").forEach((n) => n.classList.remove("pqn-demo-focus"));
  });
}

async function resetCamera(page: Page): Promise<void> {
  await clearFocus(page);
  await page.evaluate(() => {
    const html = document.documentElement;
    html.classList.add("pqn-demo-zooming");
    html.style.transform = "none";
    html.style.transformOrigin = "50% 50%";
  });
  await page.waitForTimeout(280);
}

/** Scroll + highlight a region. Pass zoom > 1 only when the GIF needs it (compare). */
async function focusRegion(page: Page, target: FocusTarget, zoom = 1): Promise<void> {
  await clearFocus(page);
  const ok = await page.evaluate(
    ({ targetName, zoomLevel }) => {
      const pick = (): Element | null => {
        if (targetName === "source-sql") {
          const title = Array.from(document.querySelectorAll("h3,div,p,span")).find((el) =>
            /Source query/i.test(el.textContent || "")
          );
          return (
            title?.closest("[class*='rounded'],section,article,div")?.querySelector("pre") ||
            title?.closest("div") ||
            null
          );
        }
        if (targetName === "finding") {
          const hit = Array.from(document.querySelectorAll("li,div,p,span")).find((el) =>
            /date_trunc|function-wrapped|no pruning/i.test(el.textContent || "")
          );
          return hit?.closest("li,div[class*='border'],article") || hit || null;
        }
        if (targetName === "candidate") {
          return document.querySelector("textarea") || null;
        }
        if (targetName === "compare") {
          const cell = Array.from(document.querySelectorAll("td")).find((el) =>
            /Partitions scanned/i.test(el.textContent || "")
          );
          return cell?.closest("table") || document.querySelector("table") || null;
        }
        if (targetName === "report-sql") {
          const title = Array.from(document.querySelectorAll("h2,h3,p,div")).find((el) =>
            /SQL Query|Source query|DATE_TRUNC/i.test(el.textContent || "")
          );
          return document.querySelector("pre") || title?.closest("section,div") || title || null;
        }
        return null;
      };

      const el = pick();
      if (!el || !(el instanceof HTMLElement)) return false;
      el.classList.add("pqn-demo-focus");
      el.scrollIntoView({ block: "center", inline: "nearest", behavior: "instant" as ScrollBehavior });

      const html = document.documentElement;
      if (zoomLevel > 1.01) {
        const r = el.getBoundingClientRect();
        const cx = r.left + r.width / 2;
        const cy = r.top + r.height / 2;
        const ox = Math.max(10, Math.min(90, (cx / window.innerWidth) * 100));
        const oy = Math.max(15, Math.min(85, (cy / window.innerHeight) * 100));
        html.classList.add("pqn-demo-zooming");
        html.style.transformOrigin = `${ox}% ${oy}%`;
        html.style.transform = `scale(${zoomLevel})`;
      } else {
        html.style.transform = "none";
      }
      return true;
    },
    { targetName: target, zoomLevel: zoom }
  );
  if (!ok) {
    await page.waitForTimeout(300);
  }
  await page.waitForTimeout(zoom > 1.01 ? 380 : 200);
}
