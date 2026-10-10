import { test, expect } from "./fixtures.js";

const observedAt = 1791300000000;
test.beforeEach(async ({ page }) => {
    await page.route("**/__voice_history__", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"><link rel="stylesheet" href="/src/voice-diagnostics.css"></head><body><div class="dlg-overlay"><div class="dlg client-info"><h3>Connection Info · Daniel</h3><div class="dlg-buttons"><button>Close</button></div></div></div></body></html>` }));
    await page.goto("/__voice_history__");
    await page.evaluate(async () => {
        const { renderReceiverDiagnostics } = await import("/src/voice-diagnostics-ui.js");
        window.__renderHistory = (diagnostic, language = "en") => {
            window.__diagnostic = diagnostic;
            return import("/src/i18n.js").then(({ setLanguage }) => {
                setLanguage(language);
                renderReceiverDiagnostics(document.querySelector(".dlg-overlay"), diagnostic);
            });
        };
    });
});

const sample = (offset, values) => ({ observed_at: observedAt + offset * 1000, ...values });
async function show(page, history, { age = 0, language = "en" } = {}) {
    await page.evaluate(({ history, at, language }) => window.__renderHistory({
        nickname: "Daniel", observed_at: at, history,
    }, language), { history, at: history.at(-1).observed_at + age, language });
    await page.locator('[data-role="receiver-diagnostics"] > summary').click();
    await page.locator('[data-role="voice-history"] > summary').click();
}

test("history graphs show shared time and percent axes, real zeros and disconnected missing intervals", async ({ page }, testInfo) => {
    await show(page, [sample(0, { buffer_ms: 0, loss_percent: 0, concealment_percent: 1 }),
        sample(5, { buffer_ms: 70, loss_percent: 0.5, concealment_percent: 3 }),
        sample(10, { buffer_ms: null, loss_percent: null, concealment_percent: null }),
        sample(15, { buffer_ms: 420, loss_percent: 2, concealment_percent: 4 }),
        sample(40, { buffer_ms: 50, loss_percent: 0, concealment_percent: 0 }),
    ]);
    const graphs = page.locator(".ci-history-chart");
    await expect(graphs).toHaveCount(3);
    await expect(page.getByRole("img", { name: /Buffer, scale 0–500 ms/ })).toBeVisible();
    await expect(graphs.nth(1).locator("figcaption span")).toHaveText("0–5 %");
    await expect(graphs.nth(2).locator("figcaption span")).toHaveText("0–5 %");
    await expect(graphs.nth(0).locator("polyline")).toHaveCount(3);
    await expect(graphs.nth(0).locator("circle")).toHaveCount(4);
    await expect(graphs.nth(0).locator("circle").first()).toHaveAttribute("cy", "64.00");
    await expect(page.locator(".ci-history-scroll tbody tr")).toHaveCount(5);
    await expect(page.getByRole("table")).toHaveAccessibleName("History measurements");
    await expect(page.locator(".ci-history-scroll tbody tr").nth(2)).toContainText("—");
    await page.locator('[data-role="voice-history"]').screenshot({ path: testInfo.outputPath("voice-history-en.png") });
});

test("stale and entirely missing history is labeled without invented lines", async ({ page }) => {
    await show(page, [sample(0, { buffer_ms: null, loss_percent: null, concealment_percent: null })], { age: 30000 });
    await expect(page.locator(".ci-history-age")).toHaveText("Stale · updated 30s ago");
    await expect(page.locator(".ci-history-chart circle, .ci-history-chart polyline")).toHaveCount(0);
    await expect(page.getByText("No measurements for this metric.", { exact: true })).toHaveCount(3);
    await expect(page.locator(".ci-history-scroll tbody")).not.toContainText("0.0%");
    await page.evaluate(() => window.__renderHistory(null));
    await expect(page.locator('[data-role="receiver-diagnostics"]')).toHaveCount(0);
});

test("history remains bounded and preserves keyboard focus and table scroll during refresh", async ({ page }) => {
    await show(page, Array.from({ length: 80 }, (_, i) => sample(i * 5, { buffer_ms: i + 1, loss_percent: 0, concealment_percent: 0 })));
    const table = page.getByRole("region", { name: "History measurements" });
    await expect(table.locator("tbody tr")).toHaveCount(60);
    await table.focus();
    await table.evaluate(node => { node.scrollTop = 120; });
    await page.evaluate(() => window.__renderHistory(window.__diagnostic));
    await expect(table).toBeFocused();
    expect(await table.evaluate(node => node.scrollTop)).toBe(120);
    await expect(page.locator('[data-role="voice-history"]')).toHaveAttribute("open", "");
});

test("German graphs fit a narrow dialog and retain exact values in a keyboard-scrollable table", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await show(page, Array.from({ length: 14 }, (_, i) => sample(i * 5, { buffer_ms: [20, 25, 180, 90, 50, 30, null][i % 7], loss_percent: i % 4 * 0.2, concealment_percent: i % 5 * 0.3 })), { language: "de", age: 20000 });
    await expect(page.locator(".ci-history-age")).toHaveText("Veraltet · vor 20s aktualisiert");
    await expect(page.getByRole("img", { name: /Sprachüberbrückung, Skala/ })).toBeVisible();
    const boxes = await page.locator(".ci-history-chart").evaluateAll(nodes => nodes.map(node => {
        const rect = node.getBoundingClientRect(); return { left: rect.left, right: rect.right };
    }));
    expect(boxes.every(box => box.left >= 0 && box.right <= 360)).toBe(true);
    const table = page.getByRole("region", { name: "Verlaufsmesswerte" });
    await table.focus(); await expect(table).toBeFocused();
    expect(await page.locator(".dlg").evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.locator('[data-role="voice-history"]').screenshot({ path: testInfo.outputPath("voice-history-de-narrow.png") });
});
