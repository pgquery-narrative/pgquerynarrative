import { test, expect, type Page, type Browser } from "@playwright/test";

/**
 * Compact README GIF — three beats: finding → compare proof → report.
 * Scroll + highlight only (no camera zoom). Warm up off-camera so recording
 * starts on a ready investigation. Requires ≥1M-row demo.sales.
 * Run: make demo-gif
 */
const BAD_SQL =
  "SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' GROUP BY product_category ORDER BY revenue DESC";
const GOOD_SQL = `SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales
WHERE date >= '2025-01-01' AND date < '2025-02-01'
GROUP BY product_category
ORDER BY revenue DESC`;

test.describe("Demo workflow capture", () => {
  test("query investigation walkthrough", async ({ page, browser }) => {
    test.setTimeout(300_000);

    const readyURL = await warmReadyInvestigation(browser);

    // Recording starts here on an already-analyzed investigation (no load spinner beat).
    await page.goto(readyURL);
    await expect(page.getByText(/date_trunc|function-wrapped|no pruning/i).first()).toBeVisible({
      timeout: 30_000,
    });
    await installDemoChrome(page);

    // Beat 1 — finding
    await showCaption(page, "Finding: DATE_TRUNC blocks partition pruning");
    await focusRegion(page, "finding");
    await page.waitForTimeout(1600);

    // Beat 2 — compare proof (partitions row dominates)
    await clearFocus(page);
    await page.getByRole("button", { name: /Compare plans/i }).click();
    await expect(page.getByRole("cell", { name: "Partitions scanned" })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByText("-100.0%")).toHaveCount(0);
    await showCaption(page, "Proof: partitions 50 → 1 · ~18× faster");
    await focusRegion(page, "partitions-row");
    await page.waitForTimeout(3200);

    // Beat 3 — report
    await clearFocus(page);
    await page.getByRole("button", { name: /Generate report/i }).click();
    await expect(page.getByText(/Query Investigation: Slow dashboard query/i)).toBeVisible({
      timeout: 60_000,
    });
    await showCaption(page, "Report: evidence you can ship");
    await focusRegion(page, "report-sql");
    await page.waitForTimeout(1400);
    await clearFocus(page);
    await page.waitForTimeout(300);
  });
});

type FocusTarget = "finding" | "partitions-row" | "report-sql";

/** Create + analyze the investigation off-camera; return /investigate/:id URL. */
async function warmReadyInvestigation(browser: Browser): Promise<string> {
  const baseURL = process.env.PLAYWRIGHT_BASE_URL || "http://127.0.0.1:8080";
  const warm = await browser.newContext({
    baseURL,
    viewport: { width: 1280, height: 720 },
  });
  try {
    const page = await warm.newPage();
    const params = new URLSearchParams({
      title: "Slow dashboard query",
      sql: BAD_SQL,
      candidate: GOOD_SQL,
    });
    await page.goto(`/investigate?${params.toString()}`);
    await expect(page.getByText(/Query Investigation/i).first()).toBeVisible({ timeout: 120_000 });
    await expect(page.getByRole("heading", { name: /Execution plan/i })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByText(/date_trunc|function-wrapped|no pruning/i).first()).toBeVisible({
      timeout: 60_000,
    });
    // Prefer the persisted investigation route (fast reload for the recorded page).
    await expect(page).toHaveURL(/\/investigate\/[^/?]+/, { timeout: 30_000 });
    return page.url();
  } finally {
    await warm.close();
  }
}

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
          box-shadow: 0 0 0 6px rgba(34,211,238,0.18), 0 12px 40px rgba(0,0,0,0.25) !important;
          position: relative;
          z-index: 5;
        }
        .pqn-demo-dim {
          opacity: 0.28 !important;
          filter: grayscale(0.35);
          transition: opacity 0.25s ease, filter 0.25s ease;
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
    document.querySelectorAll(".pqn-demo-dim").forEach((n) => n.classList.remove("pqn-demo-dim"));
  });
}

/** Scroll into view and highlight. For partitions-row, dim sibling rows. */
async function focusRegion(page: Page, target: FocusTarget): Promise<void> {
  await clearFocus(page);
  const ok = await page.evaluate((targetName) => {
    const pick = (): HTMLElement | null => {
      if (targetName === "finding") {
        const hit = Array.from(document.querySelectorAll("li,div,p,span")).find((el) =>
          /date_trunc|function-wrapped|no pruning/i.test(el.textContent || "")
        );
        const card = hit?.closest("li,div[class*='border'],article");
        return (card instanceof HTMLElement ? card : hit instanceof HTMLElement ? hit : null);
      }
      if (targetName === "partitions-row") {
        const cell = Array.from(document.querySelectorAll("td")).find((el) =>
          /Partitions scanned/i.test(el.textContent || "")
        );
        const row = cell?.closest("tr");
        return row instanceof HTMLElement ? row : null;
      }
      if (targetName === "report-sql") {
        const title = Array.from(document.querySelectorAll("h2,h3,p,div")).find((el) =>
          /SQL Query|Source query|DATE_TRUNC/i.test(el.textContent || "")
        );
        const pre = document.querySelector("pre");
        if (pre instanceof HTMLElement) return pre;
        const wrap = title?.closest("section,div");
        return wrap instanceof HTMLElement ? wrap : title instanceof HTMLElement ? title : null;
      }
      return null;
    };

    const el = pick();
    if (!el) return false;

    if (targetName === "partitions-row") {
      const table = el.closest("table");
      if (table) {
        table.querySelectorAll("tbody tr, tr").forEach((tr) => {
          if (tr !== el) tr.classList.add("pqn-demo-dim");
        });
        // Dim nearby chrome so the proof row owns the frame.
        const panel = table.closest("section,div[class*='rounded'],article") || table.parentElement;
        panel?.querySelectorAll("thead, caption, p, h2, h3, button").forEach((n) => {
          if (!el.contains(n)) n.classList.add("pqn-demo-dim");
        });
      }
    }

    el.classList.add("pqn-demo-focus");
    el.scrollIntoView({ block: "center", inline: "nearest", behavior: "instant" as ScrollBehavior });
    return true;
  }, target);

  if (!ok) {
    await page.waitForTimeout(300);
  }
  await page.waitForTimeout(220);
}
