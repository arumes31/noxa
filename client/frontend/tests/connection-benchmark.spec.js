import { test, expect } from "./fixtures.js";

async function mount(page, options = {}) {
    await page.route("**/__benchmark_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Connection benchmark</title><main></main>' }));
    await page.goto("/__benchmark_test__");
    await page.evaluate(async options => {
        window.__benchmarkCalls = [];
        window.__benchmarkStatus = { id: "test-a", phase: "running", elapsed_seconds: 3 };
        window.__noxa = { state: { activeTabID: "a", serverGeneration: 1, myClientID: "me", myChannelID: 3, muted: true } };
        window.go = { main: { App: {
            StartConnectionBenchmark: async tab => { window.__benchmarkCalls.push(["start", tab]); return { id: "test-a", phase: "connecting" }; },
            ConnectionBenchmarkStatusForTab: async (tab, id) => { window.__benchmarkCalls.push(["status", tab, id]); if (options.statusFailure) throw new Error("status unavailable"); return window.__benchmarkStatus; },
            CancelConnectionBenchmark: async (tab, id) => { window.__benchmarkCalls.push(["cancel", tab, id]); window.__benchmarkStatus = { id, phase: "cancelled" }; },
        } } };
        (await import("/src/i18n.js")).setLanguage(options.language || "en");
        document.querySelector("main").append((await import("/src/connection-benchmark.js")).createConnectionBenchmark());
    }, options);
}

test("benchmark requires explicit start, supports cancel and does not touch voice membership", async ({ page }) => {
    await mount(page);
    expect(await page.evaluate(() => window.__benchmarkCalls)).toEqual([]);
    await expect(page.getByText(/microphone and speakers are not used/)).toBeVisible();
    await page.getByRole("button", { name: "Start 20-second connection test" }).click();
    await expect(page.getByRole("status")).toContainText("3 / 20 s");
    await page.getByRole("button", { name: "Cancel test" }).click();
    await expect(page.getByRole("status")).toHaveText("Connection test cancelled");
    expect(await page.evaluate(() => [window.__noxa.state.myChannelID, window.__noxa.state.muted])).toEqual([3, true]);
});

test("completion shows unknown timing for no returns without a healthy quality claim", async ({ page }) => {
    await mount(page);
    await page.evaluate(() => { window.__benchmarkStatus = { id: "test-a", phase: "complete", result: { sent: 1000, returned: 0, unreturned: 1000, round_trip: null, arrival_gap: null } }; });
    await page.getByRole("button", { name: "Start 20-second connection test" }).click();
    await expect(page.getByText("No test packets returned. Delivery timing is unavailable.")).toBeVisible();
    await expect(page.getByText(/not a definitive loss rate/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Copy test summary" })).toBeVisible();
    await expect(page.locator(".connection-benchmark-results dd").nth(2)).toHaveText("—");
});

test("changing server cancels only the originating test and ignores stale result", async ({ page }) => {
    await mount(page);
    await page.getByRole("button", { name: "Start 20-second connection test" }).click();
    await page.evaluate(() => { window.__noxa.state.activeTabID = "b"; window.__noxa.state.serverGeneration++; });
    await expect.poll(() => page.evaluate(() => window.__benchmarkCalls.filter(call => call[0] === "cancel"))).toEqual([["cancel", "a", "test-a"]]);
    await expect(page.getByRole("button", { name: "Copy test summary" })).toBeHidden();
});

test("closing settings cancels the owned operation", async ({ page }) => {
    await mount(page);
    await page.getByRole("button", { name: "Start 20-second connection test" }).click();
    await page.evaluate(() => document.querySelector("main").replaceChildren());
    await expect.poll(() => page.evaluate(() => window.__benchmarkCalls.filter(call => call[0] === "cancel"))).toEqual([["cancel", "a", "test-a"]]);
});

test("German connection test remains readable in a narrow settings panel", async ({ page }) => {
    await page.setViewportSize({ width: 480, height: 800 });
    await mount(page, { language: "de" });
    await expect(page.getByText(/Mikrofon und Lautsprecher werden nicht verwendet/)).toBeVisible();
    await page.getByRole("button", { name: "20-Sekunden-Verbindungstest starten" }).click();
    await page.getByRole("button", { name: "Test abbrechen" }).click();
    await expect(page.getByRole("status")).toHaveText("Verbindungstest abgebrochen");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("a status failure cancels the native operation instead of leaving it running", async ({ page }) => {
    await mount(page, { statusFailure: true });
    await page.getByRole("button", { name: "Start 20-second connection test" }).click();
    await expect(page.getByRole("status")).toContainText("Test unavailable");
    expect(await page.evaluate(() => window.__benchmarkCalls.filter(call => call[0] === "cancel"))).toEqual([["cancel", "a", "test-a"]]);
    await expect(page.getByRole("button", { name: "Start 20-second connection test" })).toBeEnabled();
});
