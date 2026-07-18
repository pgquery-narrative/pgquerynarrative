import { expect, test } from "@playwright/test";

test.describe("security headers", () => {
  test("SPA responses include Content-Security-Policy", async ({ request }) => {
    const res = await request.get("/");
    expect(res.ok()).toBeTruthy();
    const csp = res.headers()["content-security-policy"];
    expect(csp).toBeTruthy();
    expect(csp).toContain("default-src 'self'");
    expect(csp).toContain("script-src 'self'");
    expect(csp).not.toContain("unsafe-eval");
  });

  test("inline script injection is not executed under CSP", async ({ page }) => {
    await page.goto("/");
    const executed = await page.evaluate(() => {
      const el = document.createElement("script");
      el.textContent = "window.__pgqn_xss = true";
      document.body.appendChild(el);
      return Boolean((window as unknown as { __pgqn_xss?: boolean }).__pgqn_xss);
    });
    // Browsers still allow page.evaluate to run scripts; assert CSP header presence
    // and that the document does not already contain an attacker-injected flag.
    expect(executed).toBe(false);
    const csp = await page.evaluate(() => {
      const meta = document.querySelector('meta[http-equiv="Content-Security-Policy"]');
      return meta?.getAttribute("content") ?? "";
    });
    // Header-based CSP is authoritative; meta may be empty when set by middleware.
    void csp;
    const headerOk = await page.evaluate(async () => {
      const res = await fetch(location.href, { method: "GET" });
      return (res.headers.get("content-security-policy") || "").includes("script-src");
    });
    expect(headerOk).toBeTruthy();
  });
});
