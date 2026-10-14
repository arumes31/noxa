import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }, testInfo) => {
    if (testInfo.title.includes("hung sender stats") || testInfo.title.includes("manual recording") || testInfo.title.includes("sender trace")) await page.clock.install();
    await page.route("**/sender-diagnostics-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"></head><body><section id="sharing-status" class="sharing-status"></section></body></html>` }));
    await page.goto("/sender-diagnostics-fixture");
    await page.evaluate(async traceEnabled => {
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        track.getSettings = () => ({ width: 2560, height: 1440, frameRate: 60, deviceId: "private-device" });
        Object.defineProperty(track, "label", { value: "Private source title" });
        const stream = new MediaStream([track]);
        let tick = 0;
        const pc = { connectionState: "connected" }, state = { pc, shareStream: stream,
            activeTabID: "qa", serverGeneration: 1, sessionGeneration: 1, myClientID: "self", myChannelID: 1, settings: {},
            shareVideoTransceiver: { sender: { track, getParameters: () => ({ encodings: [{ rid: "f", active: true, maxBitrate: 50000000 }] }), getStats: async () => {
                if (window.__sender.reject) throw new Error("stats failed");
                tick++;
                return new Map([
                    ["f", { id: "f", type: "outbound-rtp", kind: "video", ssrc: 101, rid: "f", mediaSourceId: "source", codecId: "codec", timestamp: tick * 2000,
                        frameWidth: 1920, frameHeight: 1080, framesEncoded: tick * 24, framesSent: tick * 20,
                        framesPerSecond: 60, bytesSent: tick * 2000000, packetsSent: tick * 2000, totalEncodeTime: tick * 1.2,
                        totalPacketSendDelay: tick * 20, qualityLimitationReason: "none", encoderImplementation: "libvpx", powerEfficientEncoder: false,
                        targetBitrate: 10000000, retransmittedBytesSent: tick * 20000, keyFramesEncoded: tick,
                        pliCount: tick * 2, firCount: 0, hugeFramesSent: tick, qpSum: tick * 720,
                        transportId: "transport", remoteId: "remote", qualityLimitationDurations: { bandwidth: tick * 0.5, cpu: 0 } }],
                    ["transport", { id: "transport", type: "transport", selectedCandidatePairId: "pair" }],
                    ["pair", { id: "pair", type: "candidate-pair", timestamp: tick * 2000, availableOutgoingBitrate: 12000000, currentRoundTripTime: 0.04, localCandidateId: "secret" }],
                    ["remote", { id: "remote", type: "remote-inbound-rtp", localId: "f", ssrc: 101, timestamp: tick * 2000 - 100, roundTripTime: 0.08, fractionLost: 0.02 }],
                    ["source", { id: "source", type: "media-source", trackIdentifier: track.id, timestamp: tick * 2000, frames: tick * 120 }],
                    ["codec", { id: "codec", type: "codec", mimeType: "video/VP8" }],
                    ["secret", { id: "secret", type: "local-candidate", address: "192.0.2.99" }],
                ]);
            } } } };
        window.__sender = { reject: false, notices: [], copied: null };
        window.__noxa = { state, toast: value => window.__sender.notices.push(value) };
        window.runtime = { ClipboardSetText: async value => { window.__sender.copied = value; return true; } };
        if (traceEnabled) {
            const bridge = new EventTarget();
            window.__sender.traceMessages = [];
            bridge.postMessage = value => window.__sender.traceMessages.push(JSON.parse(value.slice("__noxa_sender_trace_v1:".length)));
            window.chrome ??= {}; window.chrome.webview = bridge;
            window.go = { main: { App: { SupportsSenderTrace: async () => true, ArmSenderTraceForTab: async () => "a".repeat(64) } } };
            pc.getSenders = () => [state.shareVideoTransceiver.sender, ...(window.__sender.camera ? [{ track: { kind: "video", readyState: "live" } }] : [])];
            window.__sender.emitTrace = (status, record) => {
                const start = window.__sender.traceMessages.findLast(message => message.command === "start");
                bridge.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "noxa.sender_trace.v1",
                    token: start.token, scope: start.scope, state: status, reason: "duration", ...(record ? { record } : {}) }) }));
            };
        }
        const module = await import("/src/share-status.js");
        window.__sender.stop = module.stopShareStatus;
        window.__sender.start = () => module.startShareStatus({ stream, pc, scope: { tabID: "qa", generation: 1, session: 1, clientID: "self", channelID: 1 },
            preset: { width: 1920, height: 1080, fps: 60 }, surface: "screen", audioMode: "none", generation: "4",
            stop: () => module.stopShareStatus(), change: () => {}, changeQuality: () => {}, reduction: () => "" });
        window.__sender.start();
    }, testInfo.title.includes("sender trace"));
});

test("sending details distinguish captured, encoded, sent and reported frame rates and copy only safe fields", async ({ page }, testInfo) => {
    const panel = page.locator("#sharing-status");
    await expect(panel.locator(".sharing-meta")).toContainText("Sending 1920 × 1080 · 10 fps");
    await expect(panel.locator(".sharing-warning")).toBeHidden();
    await expect(panel.locator(".stream-health-headline")).toHaveText("Stream health is not clear yet");
    await panel.getByText("Sending details", { exact: true }).click();
    const details = panel.locator(".sharing-diagnostics");
    for (const [label, value] of [["Selected frame rate", "60.0 fps"], ["Capture setting", "60.0 fps"], ["Captured frames", "60.0 fps"],
        ["Encoded frames", "12.0 fps"], ["Sent frames", "10.0 fps"], ["Browser-reported encoding rate", "60.0 fps"], ["Encoding time per frame", "50.0 ms"],
        ["Estimated outgoing capacity (shared transport)", "12.00 Mbit/s"], ["Sender–server connection RTT", "40.0 ms"],
        ["Server-reported video RTT", "80.0 ms"], ["Server-reported video loss (latest fresh report)", "2.0%"],
        ["Applied encoder bitrate ceiling", "50.00 Mbit/s"], ["Encoding enabled", "yes"],
        ["Bandwidth-limited time in this interval", "500.0 ms"], ["CPU-limited time in this interval", "0.0 ms"]]) {
        await expect(details.getByText(label, { exact: true }).locator("+ dd")).toHaveText(value);
    }
    await details.getByRole("button", { name: "Copy sending diagnostics" }).click();
    const copied = await page.evaluate(() => window.__sender.copied);
    expect(copied).not.toMatch(/private|source title|device|192\.0\.2|trackIdentifier|mediaSourceId/i);
    expect(JSON.parse(copied).video_senders[0]).toMatchObject({ generation: "4", ssrc: 101, rid: "f", capture_fps: 60, encoded_fps: 12, sent_fps: 10, reported_fps: 60,
        capture_width: 2560, capture_height: 1440, width: 1920, height: 1080,
        available_outgoing_bitrate_bps: 12000000, transport_rtt_ms: 40, remote_rtt_ms: 80, remote_fraction_lost: 0.02,
        encoding_max_bitrate_bps: 50000000, encoding_active: true, bandwidth_limited_ms: 500, cpu_limited_ms: 0 });
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


test("hung sender stats stay single-flight across timeout and panel restart, then recover after late settlement", async ({ page }) => {
    await page.evaluate(() => {
        const fixture = window.__sender, sender = window.__noxa.state.shareVideoTransceiver.sender;
        fixture.stop();
        const normal = sender.getStats;
        fixture.calls = 0;
        sender.getStats = () => {
            fixture.calls++;
            return fixture.calls === 1 ? new Promise(resolve => { fixture.resolveLate = resolve; }) : normal();
        };
        fixture.start();
    });
    await page.clock.runFor(6000);
    expect(await page.evaluate(() => window.__sender.calls)).toBe(1);
    await page.evaluate(async () => {
        const { sampleSenderStats } = await import("/src/peer-stats.js");
        // Recovery's fresh request shares the timed-out status request too.
        await sampleSenderStats(window.__noxa.state.shareVideoTransceiver.sender, { fresh: true }).catch(() => {});
        window.__sender.stop(); window.__sender.start();
    });
    await page.clock.runFor(30000);
    expect(await page.evaluate(() => window.__sender.calls)).toBe(1);
    await expect(page.locator(".sharing-meta")).toContainText("\u2014 fps");
    // The late failed-generation result must not become the new panel's sample.
    await page.evaluate(() => window.__sender.resolveLate(new Map([["late", {
        id: "late", type: "outbound-rtp", kind: "video", ssrc: 101, timestamp: 1000,
        frameWidth: 64, frameHeight: 64, framesEncoded: 1, framesSent: 1,
    }]])));
    await expect(page.locator(".sharing-meta")).not.toContainText("64 \u00d7 64");
    await page.clock.runFor(4500);
    expect(await page.evaluate(() => window.__sender.calls)).toBeGreaterThan(1);
    await expect(page.locator(".sharing-meta")).toContainText("Sending 1920 \u00d7 1080 \u00b7 10 fps");
    await page.evaluate(() => window.__sender.stop());
    const stoppedCalls = await page.evaluate(() => window.__sender.calls);
    await page.clock.runFor(10000);
    expect(await page.evaluate(() => window.__sender.calls)).toBe(stoppedCalls);
});


test("manual recording reuses existing polls, stops, exports and ends on session change", async ({ page }, testInfo) => {
    const panel = page.locator(".sharing-diagnostics");
    await panel.locator("summary").click();
    await page.evaluate(() => {
        const sender = window.__noxa.state.shareVideoTransceiver.sender, get = sender.getStats;
        window.__sender.calls = 0; sender.getStats = () => { window.__sender.calls++; return get(); };
    });
    await panel.getByRole("button", { name: "Start diagnostic recording", exact: true }).click();
    expect(await page.evaluate(() => window.__sender.calls)).toBe(0);
    const announcement = panel.locator('.stream-recording [role="status"]');
    await expect(announcement).toHaveText("Diagnostic recording started.");
    await page.clock.runFor(6500);
    await expect(announcement).toHaveText("Diagnostic recording started.");
    expect(await page.evaluate(() => window.__sender.calls)).toBe(3);
    await panel.locator(".stream-recording small").scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("sender-diagnostic-recording.png"), fullPage: true });
    await panel.getByRole("button", { name: "Stop diagnostic recording", exact: true }).click();
    await panel.getByRole("button", { name: "Copy sending diagnostics" }).click();
    const record = await page.evaluate(() => JSON.parse(window.__sender.copied).recording);
    expect(record).toMatchObject({ state: "interrupted", stop_reason: "manual", sample_count: 3, direction: "sent" });
    expect(record.samples[0].rows[0]).toMatchObject({ width: 1920, height: 1080, power_efficient: false });
    for (const { rows: [row] } of record.samples) {
        expect(row).toMatchObject({ pli_count: row.key_frames * 2, fir_count: 0,
            huge_frames_sent: row.key_frames, qp_sum: row.frames_encoded * 30 });
    }
    expect(JSON.stringify(record)).not.toMatch(/private|192\.0\.2|trackIdentifier|stats_id/i);
    await panel.getByRole("button", { name: "Start diagnostic recording", exact: true }).click();
    await page.evaluate(() => { window.__noxa.state.sessionGeneration++; });
    await page.clock.runFor(1100);
    // The sender poll may tear down its panel before the recorder watchdog fires.
    // Both paths must preserve the interrupted recording in the durable archive.
    const archive = page.locator(".stream-recording-archive");
    await archive.locator("summary").click();
    const saved = archive.locator(":scope > div > div").filter({ hasText: "stream or session changed" });
    await expect(saved).toHaveCount(1);
    await saved.getByRole("button", { name: "Copy saved diagnostics", exact: true }).click();
    const interrupted = await page.evaluate(() => JSON.parse(window.__sender.copied).recording);
    expect(interrupted).toMatchObject({ state: "interrupted", direction: "sent", slot: "screen", stop_reason: "scope_changed" });
});


test("manual recording separates reused SSRC lifetimes and remains copyable after share teardown", async ({ page }, testInfo) => {
    const panel = page.locator(".sharing-diagnostics"); await panel.locator("summary").click();
    await panel.getByRole("button", { name: "Start diagnostic recording", exact: true }).click();
    await page.clock.runFor(2100);
    await page.evaluate(() => {
        const sender = window.__noxa.state.shareVideoTransceiver.sender, original = sender.getStats;
        sender.getStats = async () => { const report = await original(), row = report.get("f"); report.delete("f");
            row.id = "new-stats-id"; report.set(row.id, row); return report; };
    });
    await page.clock.runFor(2100);
    await expect(panel.locator(".stream-recording")).toContainText("counter lifetime changed");
    await page.evaluate(() => window.__sender.stop());
    await expect(page.locator("#sharing-status")).toBeHidden();
    const archive = page.locator(".stream-recording-archive"); await archive.locator("summary").click();
    await archive.getByRole("button", { name: "Copy saved diagnostics" }).click();
    const record = await page.evaluate(() => JSON.parse(window.__sender.copied).recording);
    expect(record).toMatchObject({ direction: "sent", slot: "screen", stop_reason: "epoch_changed", sample_count: 1 });
    expect(JSON.stringify(record)).not.toContain("new-stats-id");
    await page.screenshot({ path: testInfo.outputPath("sender-saved-diagnostics-after-stop.png"), fullPage: true });
    await archive.getByRole("button", { name: "Remove saved copy" }).click(); await expect(archive).toBeHidden();
});


test("sender trace stays off until clicked and remains explicitly exportable after share ends", async ({ page }) => {
    await expect(page.locator(".sharing-meta")).toContainText("10 fps");
    await page.locator(".sharing-diagnostics summary").click();
    expect(await page.evaluate(() => window.__sender.traceMessages)).toEqual([]);
    await page.getByRole("button", { name: "Start 60-second sender trace", exact: true }).click();
    await page.evaluate(() => window.__sender.emitTrace("recording"));
    await expect(page.locator('.sender-trace [role="status"]')).toContainText("Recording sender timing");
    await page.evaluate(() => window.__sender.stop());
    await expect(page.locator("#sharing-status")).toBeHidden();
    await expect(page.locator('[data-saved-trace="true"]')).toBeVisible();
    expect(await page.evaluate(() => window.__sender.traceMessages.map(x => x.command))).toEqual(["start", "stop"]);
    expect(await page.evaluate(() => window.__sender.traceMessages.at(-1).scope_lost)).toBe(true);
    await page.evaluate(() => window.__sender.emitTrace("completed"));
    await page.getByRole("button", { name: "Copy sender trace", exact: true }).click();
    await page.evaluate(() => window.__sender.emitTrace("completed", { schema: "noxa.sender_trace.numeric.v1", complete: true,
        reason: "duration", elapsed_ms: 60000, timer_cooperative: true, marker_counts: { encode: 1, encoded: 1, enqueue: 1, process: 1 },
        events: [{ kind: "encode", trace_us: 123, rtp_timestamp: 456, secret: "private source title" }], dropped_unknown_events: 0, raw: "private payload" }));
    await expect.poll(() => page.evaluate(() => window.__sender.copied)).not.toBeNull();
    const copied = await page.evaluate(() => window.__sender.copied);
    expect(copied).not.toMatch(/private|payload|token|"scope"/);
    expect(JSON.parse(copied).sender_trace).toMatchObject({ complete: false, reason: "scope_changed", events: [{ kind: "encode", trace_us: 123, rtp_timestamp: 456 }] });
    await page.getByRole("button", { name: "Remove saved trace", exact: true }).click();
    await expect(page.locator(".sender-trace")).toHaveCount(0);
});

test("sender trace rejects camera and cancels late arm after share teardown", async ({ page }) => {
    await expect(page.locator(".sharing-meta")).toContainText("10 fps");
    await page.locator(".sharing-diagnostics summary").click();
    await page.evaluate(() => { window.__sender.camera = true; });
    await page.getByRole("button", { name: "Start 60-second sender trace", exact: true }).click();
    expect(await page.evaluate(() => window.__sender.traceMessages)).toEqual([]);
    await page.evaluate(() => { window.__sender.camera = false;
        window.go.main.App.ArmSenderTraceForTab = () => new Promise(resolve => { window.__sender.lateTraceArm = resolve; }); });
    await page.getByRole("button", { name: "Start 60-second sender trace", exact: true }).click();
    await expect(page.locator('.sender-trace [role="status"]')).toContainText("Preparing");
    await page.evaluate(() => { window.__sender.stop(); window.__sender.lateTraceArm("b".repeat(64)); });
    expect(await page.evaluate(() => window.__sender.traceMessages.map(x => x.command))).toEqual(["stop"]);
    await expect(page.locator('.sender-trace [role="status"]')).toContainText("incomplete");
});

test("sender trace timeout reports incomplete without an automatic export or retry", async ({ page }) => {
    await expect(page.locator(".sharing-meta")).toContainText("10 fps");
    await page.locator(".sharing-diagnostics summary").click();
    await page.getByRole("button", { name: "Start 60-second sender trace", exact: true }).click();
    await page.evaluate(() => window.__sender.emitTrace("recording"));
    await page.clock.runFor(91000);
    await expect(page.locator('.sender-trace [role="status"]')).toContainText("incomplete");
    expect(await page.evaluate(() => window.__sender.traceMessages.map(x => x.command))).toEqual(["start", "stop"]);
});


test("sender trace new explicit start invalidates an uncopied saved native session", async ({ page }) => {
    await expect(page.locator(".sharing-meta")).toContainText("10 fps");
    await page.locator(".sharing-diagnostics summary").click();
    await page.locator('#sharing-status [data-trace="start"]').click();
    await page.evaluate(() => { window.__sender.emitTrace("completed", null); window.__sender.stop(); window.__sender.start(); });
    await expect(page.locator('[data-saved-trace="true"] [data-trace="copy"]')).toBeEnabled();
    await page.locator(".sharing-diagnostics summary").click();
    await page.locator('#sharing-status [data-trace="start"]').click();
    await expect(page.locator('[data-saved-trace="true"] [data-trace="copy"]')).toBeDisabled();
    await expect(page.locator('[data-saved-trace="true"] [role="status"]')).toContainText("replaced");
    expect(await page.evaluate(() => window.__sender.traceMessages.map(x => x.command))).toEqual(["start", "start"]);
});
