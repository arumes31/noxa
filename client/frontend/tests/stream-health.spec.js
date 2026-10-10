import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/stream-health-fixture", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"><link rel="stylesheet" href="/src/stream-health.css"></head><body><main style="max-width:600px;padding:16px"><div id="path"></div></main></body></html>' }));
    await page.goto("/stream-health-fixture");
    await page.evaluate(async () => {
        const { renderStreamPath } = await import("/src/stream-path-diagnostics.js");
        const remote = { slot: "screen", generation: "9", sender_report: { age_ms: 1000, stale: false,
            rows: [{ slot: "screen", generation: "9", ssrc: 42, sample_ms: 2000, cpu_limited_ms: 1500, bandwidth_limited_ms: 0,
                encoded_fps: 15, sent_fps: 15, encode_ms: 50, remote_fraction_lost: 0, encoding_active: true }] },
        layers: [{ ssrc: 42, ingress: { age_ms: 100, sample_ms: 3000, fps: 30 } }], forwarding: { source_ssrc: 42, stage: { age_ms: 100, sample_ms: 4000, fps: 60 } } };
        const receiver = { sampleMS: 3000, receivedFPS: 30, fps: 30, decodeMS: 5, droppedFPS: 0, lossPercent: 0, nacks: 0, freezes: 0 };
        const options = { receiverAgeMS: 0 };
        window.__health = { remote, receiver, options, render: () => renderStreamPath(document.getElementById("path"), remote, receiver, options) };
        window.__health.render();
    });
});

test("receiver sees a cautious explanation above all raw measurements, with advice for the correct person", async ({ page }, testInfo) => {
    const health = page.locator(".stream-health");
    await expect(health.getByRole("status")).toHaveText("The sender reports encoding pressure");
    await expect(health.locator(".stream-health-evidence")).toContainText("1500.0 ms");
    await expect(health.locator(".stream-health-action")).toContainText("Ask the sender");
    await expect(page.locator('[data-stage="capture"]')).toHaveText("—");
    expect(await page.locator("[data-stage]").count()).toBeGreaterThan(30);
    expect(await page.locator("#path > :first-child").getAttribute("class")).toContain("stream-health");
    await page.screenshot({ path: testInfo.outputPath("receiver-health-en.png"), fullPage: true });
    await page.setViewportSize({ width: 360, height: 760 });
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.__health.render(); });
    await expect(health.getByRole("status")).toHaveText("Der Sender meldet hohe Encoderlast");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath("receiver-health-de-narrow.png"), fullPage: true });
});

test("numerical refreshes keep the live headline mounted and do not reannounce it", async ({ page }) => {
    await page.evaluate(() => {
        const headline = document.querySelector(".stream-health-headline");
        window.__health.headline = headline;
        window.__health.changes = 0;
        new MutationObserver(records => { window.__health.changes += records.length; }).observe(headline, { childList: true, characterData: true, subtree: true });
        window.__health.remote.sender_report.rows[0].encode_ms = 55;
        window.__health.render();
    });
    await expect(page.locator(".stream-health-evidence")).toContainText("55.0 ms");
    expect(await page.evaluate(() => window.__health.headline === document.querySelector(".stream-health-headline"))).toBe(true);
    expect(await page.evaluate(() => window.__health.changes)).toBe(0);
    await page.evaluate(() => { window.__health.options.extraAgeMS = 16000; window.__health.options.receiverAgeMS = 16000; window.__health.render(); });
    await expect(page.getByRole("status")).toHaveText("Stream health is not clear yet");
    expect(await page.evaluate(() => window.__health.changes)).toBe(1);
});

test("very low sender output remains visible even when the browser reports no CPU or bandwidth limit", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__health.remote.sender_report.rows[0], { capture_fps: 32, encoded_fps: 0.6, sent_fps: 0.6, cpu_limited_ms: 0, quality_reason: "none" });
        window.__health.render();
    });
    await expect(page.getByRole("status")).toHaveText("The sender is producing very few frames");
    await expect(page.locator(".stream-health-evidence")).toContainText("Captured: 32.0 fps · Encoded: 0.6 fps · Sent: 0.6 fps");
    await expect(page.locator(".stream-health-action")).toContainText("Ask the sender to move");
    await expect(page.locator(".stream-health-action")).toContainText("do not explain the cause");
});

test("mismatched sender identity and missing receiver loss never turn sparse evidence into a diagnosis", async ({ page }) => {
    await page.evaluate(() => {
        window.__health.remote.sender_report.rows[0].generation = "10";
        Object.assign(window.__health.receiver, { fps: 18, droppedFPS: 10, decodeMS: 50, lossPercent: null });
        window.__health.render();
    });
    await expect(page.getByRole("status")).toHaveText("Stream health is not clear yet");
    await expect(page.locator('[data-stage="encoded"]')).toHaveText("—");
    await page.evaluate(() => { window.__health.receiver.lossPercent = 0; window.__health.render(); });
    await expect(page.getByRole("status")).toHaveText("This device may be struggling to decode video");
    await expect(page.locator(".stream-health-action")).toContainText("watch fewer streams");
    await page.evaluate(() => { window.__health.receiver.lossPercent = 8; window.__health.receiver.nacks = 4; window.__health.render(); });
    await expect(page.getByRole("status")).toHaveText("Recent measurements suggest network trouble");
    await expect(page.locator(".stream-health-action")).toContainText("do not identify where packets were lost");
});
