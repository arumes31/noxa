import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/sender-diagnostics-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"></head><body><section id="sharing-status" class="sharing-status"></section></body></html>` }));
    await page.goto("/sender-diagnostics-fixture");
    await page.evaluate(async () => {
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        track.getSettings = () => ({ width: 1920, height: 1080, frameRate: 60, deviceId: "private-device" });
        Object.defineProperty(track, "label", { value: "Private source title" });
        const stream = new MediaStream([track]);
        let tick = 0;
        const pc = { connectionState: "connected" }, state = { pc, shareStream: stream,
            activeTabID: "qa", serverGeneration: 1, sessionGeneration: 1, myClientID: "self", myChannelID: 1, settings: {},
            shareVideoTransceiver: { sender: { getStats: async () => {
                if (window.__sender.reject) throw new Error("stats failed");
                tick++;
                return new Map([
                    ["f", { id: "f", type: "outbound-rtp", kind: "video", ssrc: 101, rid: "f", mediaSourceId: "source", codecId: "codec", timestamp: tick * 2000,
                        frameWidth: 1920, frameHeight: 1080, framesEncoded: tick * 24, framesSent: tick * 20,
                        framesPerSecond: 60, bytesSent: tick * 2000000, packetsSent: tick * 2000, totalEncodeTime: tick * 1.2,
                        totalPacketSendDelay: tick * 20, qualityLimitationReason: "none", encoderImplementation: "libvpx", powerEfficientEncoder: false,
                        targetBitrate: 10000000, retransmittedBytesSent: tick * 20000, keyFramesEncoded: tick }],
                    ["source", { id: "source", type: "media-source", trackIdentifier: track.id, timestamp: tick * 2000, frames: tick * 120 }],
                    ["codec", { id: "codec", type: "codec", mimeType: "video/VP8" }],
                    ["secret", { id: "secret", type: "local-candidate", address: "192.0.2.99" }],
                ]);
            } } } };
        window.__sender = { reject: false, notices: [], copied: null };
        window.__noxa = { state, toast: value => window.__sender.notices.push(value) };
        window.runtime = { ClipboardSetText: async value => { window.__sender.copied = value; return true; } };
        const module = await import("/src/share-status.js");
        window.__sender.stop = module.stopShareStatus;
        module.startShareStatus({ stream, pc, scope: { tabID: "qa", generation: 1, session: 1, clientID: "self", channelID: 1 },
            preset: { width: 1920, height: 1080, fps: 60 }, surface: "screen", audioMode: "none", generation: "4",
            stop: () => module.stopShareStatus(), change: () => {}, changeQuality: () => {}, reduction: () => "" });
    });
});

test("sending details distinguish captured, encoded, sent and reported frame rates and copy only safe fields", async ({ page }, testInfo) => {
    const panel = page.locator("#sharing-status");
    await expect(panel.locator(".sharing-meta")).toContainText("Sending 1920 × 1080 · 10 fps");
    await expect(panel.locator(".sharing-warning")).toContainText("Sending fewer frames than selected");
    await panel.getByText("Sending details", { exact: true }).click();
    const details = panel.locator(".sharing-diagnostics");
    for (const [label, value] of [["Selected frame rate", "60.0 fps"], ["Capture setting", "60.0 fps"], ["Captured frames", "60.0 fps"],
        ["Encoded frames", "12.0 fps"], ["Sent frames", "10.0 fps"], ["Browser-reported encoding rate", "60.0 fps"], ["Encoding time per frame", "50.0 ms"]]) {
        await expect(details.locator("dt").filter({ hasText: new RegExp(`^${label}$`) }).locator("+ dd")).toHaveText(value);
    }
    await details.getByRole("button", { name: "Copy sending diagnostics" }).click();
    const copied = await page.evaluate(() => window.__sender.copied);
    expect(copied).not.toMatch(/private|source title|device|192\.0\.2|trackIdentifier|mediaSourceId/i);
    expect(JSON.parse(copied).video_senders[0]).toMatchObject({ generation: "4", ssrc: 101, rid: "f", capture_fps: 60, encoded_fps: 12, sent_fps: 10, reported_fps: 60 });
    await expect(details.locator("dt").filter({ hasText: /^Retransmissions/ }).locator("+ dd")).toHaveText("80 kbit/s · 1.0%");
    await expect(details.locator("dt").filter({ hasText: /^Keyframes/ }).locator("+ dd")).toHaveText("1.0");
    await page.screenshot({ path: testInfo.outputPath("sender-details-en.png"), fullPage: true });
    await page.setViewportSize({ width: 360, height: 760 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(details.locator("summary")).toHaveText("Sendedetails");
    await expect(details.getByRole("button", { name: "Sendediagnose kopieren" })).toBeEnabled();
    await page.screenshot({ path: testInfo.outputPath("sender-details-de-narrow.png"), fullPage: true });
});

test("failed or stopped sender measurements do not leave old frame rates or copied results", async ({ page }) => {
    const panel = page.locator("#sharing-status");
    await expect(panel.locator(".sharing-meta")).toContainText("10 fps");
    await page.evaluate(() => { window.__sender.reject = true; });
    await expect(panel.locator(".sharing-meta")).toContainText("— fps");
    await panel.getByText("Sending details", { exact: true }).click();
    await expect(panel.getByRole("button", { name: "Copy sending diagnostics" })).toBeDisabled();
    await expect(panel.locator(".sharing-warning")).toBeHidden();
    await page.evaluate(() => window.__sender.stop());
    await expect(panel).toBeHidden();
});
