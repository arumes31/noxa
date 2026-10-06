import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.__infoCalls = [];
        window.__saved = { language: "en", activation_mode: "ptt", volume: 100, onboarding_done: true,
            alpha_dismissed: "0.5.0-dev+gabc123", chat_max_lines: 200, window_opacity: 100 };
        window.__info = { client_id: "c-wdaf", nickname: "wDAF", unique_id: "wdaf-account", channel_id: 7,
            client_version: "0.5.39", connected_at: 1791200000, idle_seconds: 2, ping_ms: 10,
            bytes_in: 54000, bytes_out: 420000 };
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; },
            EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(window.__saved);
            if (method === "SaveSettings") { window.__saved = structuredClone(args[0]); return ""; }
            if (method === "GetClientInfoForTab") {
                window.__infoCalls.push(args);
                if (window.__deferNextInfo) {
                    window.__deferNextInfo = false;
                    const captured = structuredClone(window.__info);
                    await new Promise(resolve => { window.__releaseInfo = resolve; });
                    window.__delayedInfoReturned = true;
                    return captured;
                }
                if (window.__infoFailure) throw new Error("request denied");
                return structuredClone(window.__info);
            }
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (method === "DMHistoryContextForTab") return { tab_id: "one", identity_uid: "me", activation: "0", identity_revision: "0" };
            if (method === "Connected" || method === "IsGuest") return false;
            if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true,
            value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openClientInfo);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "one", myClientID: "c-owner", myUniqueID: "owner-account", myChannelID: 7,
            ownAuthority: "owner", channels: [{ ChannelID: 7, Name: "Public1" }],
            clients: [{ client_id: "c-wdaf", unique_id: "wdaf-account", nickname: "wDAF", channel_id: 7 },
                { client_id: "c-alice", unique_id: "alice-account", nickname: "Alice", channel_id: 7 },
                { client_id: "c-bob", unique_id: "bob-account", nickname: "Bob", channel_id: 7 }] });
    });
});

async function openInfo(page) {
    await page.evaluate(() => window.__noxa.openClientInfo(window.__noxa.state.clients[0]));
    return page.getByRole("dialog", { name: /Connection Info|Verbindungsinformationen/ });
}

async function provideDiagnostics(page) {
    await page.evaluate(() => {
        window.__info.voice_diagnostics = {
            server_version: "0.5.39", observed_at: 1791200000, client_id: "c-wdaf", nickname: "wDAF", channel_id: 7, ping_ms: 10,
            client_report: { received_at: 1791200000, age_ms: 2500, stale: false, report: {
                channel_id: 7, client_version: "0.5.39", connection_state: "connected", output_state: "running", output_latency_ms: 12,
                rtt_ms: 10, muted: false, deafened: false, volume: 100, voice_limiter: true, gain_normalize: false,
                tracks: [
                    { track_id: "c-alice", publisher_id: "c-alice", codec: "audio/opus", sample_ms: 5000, packets_received: 950,
                        packets_lost: 50, bytes_received: 40000, total_samples: 240000, concealed_samples: 9600,
                        packets_discarded: 25, discard_percent: 20,
                        concealment_events: 4, jitter_ms: 7, loss_percent: 5, concealment_percent: 4, buffer_ms: 80, buffer_target_ms: 60, audio_level: 0.3 },
                    { track_id: "c-bob", publisher_id: "c-bob", codec: "audio/opus", sample_ms: 5000, packets_received: 200,
                        packets_lost: 0, loss_percent: 0, concealment_percent: 0, jitter_ms: 2, buffer_ms: 30, buffer_target_ms: 25, audio_level: 0.1 },
                ],
            } },
            transport: { connection_state: "connected", receiver_reports: [] },
        };
    });
}

test("owner can inspect bounded history, selected transport and correlated media stages", async ({ page }, testInfo) => {
    await provideDiagnostics(page);
    await page.evaluate(() => {
        const d = window.__info.voice_diagnostics;
        d.client_report.report.transport = { protocol: "udp", local_candidate: "relay", remote_candidate: "host", relay_protocol: "tcp" };
        d.history = [{ observed_at: Date.now(), buffer_ms: 420, loss_percent: 0, discard_percent: 2,
            concealment_percent: 1, track_count: 2 }];
        d.paths = [{ publisher_id: "c-alice", slot: "mic", output_ssrc: 123,
            sender: { ssrc: 456, packets_per_second: 50, bitrate_bps: 32000 }, sender_age_ms: 1000,
            ingress: { publication: "41", ssrc: 456, packets_per_second: 49, jitter_ms: 61, max_gap_ms: 480, burst_packets: 20 },
            receiver: { buffer_ms: 420, non_silent_concealment_percent: 1 },
            feedback: { fraction_lost: 0, jitter_ms: 72 } }];
    });
    const dialog = await openInfo(page);
    const diagnostics = dialog.locator('[data-role="receiver-diagnostics"]');
    await diagnostics.locator(":scope > summary").click();
    await expect(diagnostics).toContainText("UDP · relay → host · TURN tcp");
    await expect(diagnostics).toContainText("50.0 packets/s · 32.0 kbit/s");
    await expect(diagnostics).toContainText("49.0 packets/s · 61.0 ms");
    await expect(diagnostics).toContainText("Clocks are not synchronized");
    const history = diagnostics.locator('[data-role="voice-history"]');
    await history.locator("summary").click();
    await expect(history.locator("tbody tr")).toHaveCount(1);
    await expect(history).toContainText("420.0 ms");
    await expect.poll(() => page.evaluate(() => window.__infoCalls.length)).toBeGreaterThan(1);
    await expect(history).toHaveAttribute("open", "");
    await history.scrollIntoViewIfNeeded();
    await dialog.screenshot({ path: testInfo.outputPath("voice-history-transport.png") });
});

test("actual poor playback appears separately from a healthy server ping and clears on scope change", async ({ page }, testInfo) => {
    await page.evaluate(async () => {
        const { createConnectionQuality } = await import("/src/connection-quality.js");
        const state = window.__noxa.state;
        const peer = { connectionState: "connected", getStats: async () => new Map() };
        state.pc = peer;
        state.voiceTelemetry = { at: Date.now(), peer,
            scope: JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]),
            report: { connection_state: "connected", output_state: "running",
                transport: { protocol: "udp", local_candidate: "relay", remote_candidate: "host", relay_protocol: "tcp" }, tracks: [{ sample_ms: 5000,
                loss_percent: 0, discard_percent: 0, non_silent_concealment_percent: 0, buffer_ms: 500 }] } };
        const quality = createConnectionQuality({ $: id => document.getElementById(id), state });
        window.__stopDiagnosticQuality = quality.stopQualitySampler;
        quality.startQualitySampler();
    });
    await expect(page.locator("#voice-latency")).toHaveText("10 ms");
    await expect(page.locator("#voice-latency")).toHaveAttribute("data-quality", "good");
    await expect(page.locator("#voice-playback-quality")).toHaveText("Voice: poor");
    await expect(page.locator("#voice-playback-quality")).toHaveAttribute("data-quality", "poor");
    const badge = page.locator("#voice-playback-quality");
    await expect(badge).not.toHaveAttribute("title");
    await badge.focus();
    const tooltip = page.getByRole("tooltip", { includeHidden: true });
    await expect(tooltip).toBeVisible();
    await expect(badge).toHaveAttribute("aria-describedby", await tooltip.getAttribute("id"));
    await expect(tooltip).toContainText("UDP · relay → host · TURN tcp");
    await expect(tooltip).toContainText("Increased playback buffering");
    await expect(tooltip.locator(".voice-quality-tooltip-notes p")).toHaveCount(2);
    await page.keyboard.press("Escape");
    await expect(tooltip).toBeHidden();
    await expect(badge).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(tooltip).toBeVisible();
    await badge.blur();
    await badge.hover();
    await expect(tooltip).toBeVisible();
    await tooltip.hover();
    await expect(tooltip).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("voice-tooltip-en.png") });
    await page.keyboard.press("Escape");
    await expect(tooltip).toBeHidden();
    await badge.hover();
    await expect(tooltip).toBeVisible();
    await page.mouse.move(1000, 10);
    await expect(tooltip).toBeHidden();
    await badge.focus();
    await expect(tooltip).toBeVisible();
    await page.evaluate(() => { window.__noxa.state.myChannelID = 8; });
    await expect(badge).toBeHidden();
    await expect(tooltip).toBeHidden();
    await expect(tooltip).toBeEmpty();
    await page.evaluate(() => window.__stopDiagnosticQuality());
    await expect(tooltip).toHaveCount(0);
});

test("voice tooltip preserves unknown measurements and fits a narrow German footer", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 360, height: 640 });
    await page.evaluate(async () => {
        (await import("/src/i18n.js")).setLanguage("de");
        const { createConnectionQuality } = await import("/src/connection-quality.js");
        const state = window.__noxa.state;
        const peer = { connectionState: "connected" };
        state.pc = peer;
        state.voiceTelemetry = { at: Date.now(), peer,
            scope: JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]),
            report: { connection_state: "connected", output_state: "running", tracks: [] } };
        const quality = createConnectionQuality({ $: id => document.getElementById(id), state });
        window.__stopDiagnosticQuality = quality.stopQualitySampler;
        quality.startQualitySampler();
    });
    const badge = page.locator("#voice-playback-quality");
    await expect(badge).toHaveText("Sprache: Messung läuft");
    await badge.focus();
    const tooltip = page.getByRole("tooltip", { includeHidden: true });
    await expect(tooltip).toBeVisible();
    await expect(tooltip).toContainText("Fehlende Messwerte bleiben unbekannt");
    await expect(tooltip).not.toContainText("UDP");
    await expect(tooltip).not.toContainText("0.0%");
    const bounds = await tooltip.boundingBox(), trigger = await badge.boundingBox();
    expect(bounds.x).toBeGreaterThanOrEqual(8);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(352);
    expect(bounds.y).toBeGreaterThanOrEqual(8);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(trigger.y);
    await page.screenshot({ path: testInfo.outputPath("voice-tooltip-de-narrow.png") });
    await page.setViewportSize({ width: 960, height: 720 });
    await expect(tooltip).toBeVisible();
    await page.evaluate(() => window.__stopDiagnosticQuality());
    await expect(tooltip).toHaveCount(0);
});

test("member connection info exposes the selected client's version and ping", async ({ page }) => {
    await page.evaluate(() => { window.__noxa.state.ownAuthority = "member"; });
    const dialog = await openInfo(page);
    await expect(dialog.locator('[data-f="version"]')).toHaveText("0.5.39");
    await expect(dialog.locator('[data-f="ping"]')).toHaveText("10 ms");
    expect(await page.evaluate(() => window.__infoCalls[0])).toEqual(["one", "c-wdaf"]);
    await expect(dialog.locator('[data-role="receiver-diagnostics"]')).toBeHidden();
});

test("old clients display an unknown version without inventing the owner's version", async ({ page }) => {
    await page.evaluate(() => { delete window.__info.client_version; });
    const dialog = await openInfo(page);
    await expect(dialog.locator('[data-f="version"]')).toHaveText(/unknown/i);
});

test("owner diagnostics display the selected receiver's report and each incoming publisher", async ({ page }, testInfo) => {
    await provideDiagnostics(page);
    const dialog = await openInfo(page);
    const diagnostics = dialog.locator('[data-role="receiver-diagnostics"]');
    await expect(diagnostics).toBeVisible();
    await diagnostics.locator("summary").click();
    await expect(diagnostics).toContainText("wDAF");
    await expect(diagnostics).toContainText(/running/i);
    await expect(diagnostics).toContainText(/Alice|c-alice/);
    await expect(diagnostics).toContainText(/Bob|c-bob/);
    await expect(diagnostics).toContainText(/5(?:\.0+)?\s*%/);
    await expect(diagnostics).toContainText(/4(?:\.0+)?\s*%/);
    await expect(diagnostics).toContainText(/80(?:\.0+)?\s*ms/);
    await expect(diagnostics).toContainText(/30(?:\.0+)?\s*ms/);
    await expect(diagnostics).toContainText("Received packets discarded by playout");
    await expect(diagnostics).toContainText(/20(?:\.0+)?\s*%/);
    await diagnostics.getByText("Bob", { exact: true }).scrollIntoViewIfNeeded();
    await expect(diagnostics.getByText("Alice", { exact: true })).toBeVisible();
    await expect(diagnostics.getByText("Bob", { exact: true })).toBeVisible();
    await dialog.screenshot({ path: testInfo.outputPath("owner-receiver-diagnostics.png") });
});

test("missing receiver reports are unavailable rather than healthy zero measurements", async ({ page }) => {
    await provideDiagnostics(page);
    await page.evaluate(() => { window.__info.voice_diagnostics.client_report = null; });
    const dialog = await openInfo(page);
    const diagnostics = dialog.locator('[data-role="receiver-diagnostics"]');
    await expect(diagnostics).toBeVisible();
    await expect(diagnostics).toContainText(/no.*report|not.*received|unavailable/i);
    await expect(diagnostics).not.toContainText(/0(?:\.0+)?\s*%/);
});

test("stale receiver reports are labelled as stale", async ({ page }) => {
    await provideDiagnostics(page);
    await page.evaluate(() => { Object.assign(window.__info.voice_diagnostics.client_report, { stale: true, age_ms: 95000 }); });
    const dialog = await openInfo(page);
    await expect(dialog.locator('[data-role="receiver-diagnostics"]')).toContainText(/stale|out.of.date/i);
});

for (const reason of ["permission revoked", "refresh failed"]) {
    test(`owner receiver details are cleared when ${reason}`, async ({ page }) => {
        await provideDiagnostics(page);
        const dialog = await openInfo(page);
        const diagnostics = dialog.locator('[data-role="receiver-diagnostics"]');
        await expect(diagnostics).toContainText(/Alice|c-alice/);
        await page.evaluate(reason => {
            if (reason === "refresh failed") window.__infoFailure = true;
            else { delete window.__info.voice_diagnostics; window.__noxa.state.ownAuthority = "member"; }
        }, reason);
        await expect(diagnostics).toBeHidden();
        expect(await diagnostics.evaluateAll(nodes => nodes.map(node => node.textContent).join(""))).not.toMatch(/Alice|c-alice/);
    });
}

test("a delayed owner snapshot cannot restore diagnostics after a newer denied refresh", async ({ page }) => {
    await provideDiagnostics(page);
    const dialog = await openInfo(page);
    const diagnostics = dialog.locator('[data-role="receiver-diagnostics"]');
    await expect(diagnostics).toContainText(/Alice|c-alice/);
    await page.evaluate(() => { window.__deferNextInfo = true; });
    await expect.poll(() => page.evaluate(() => !!window.__releaseInfo)).toBe(true);
    await page.evaluate(() => { window.__infoFailure = true; });
    await expect(diagnostics).toBeHidden();
    await page.evaluate(async () => {
        window.__releaseInfo();
        await new Promise(resolve => setTimeout(resolve, 0));
    });
    expect(await page.evaluate(() => window.__delayedInfoReturned)).toBe(true);
    expect(await diagnostics.count()).toBe(0);
});

test("administrator receiver diagnostics fit a narrow German client window", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 480, height: 760 });
    await page.evaluate(async () => {
        window.__noxa.state.ownAuthority = "administrator";
        (await import("/src/i18n.js")).setLanguage("de");
    });
    await provideDiagnostics(page);
    const dialog = await openInfo(page);
    const diagnostics = dialog.locator('[data-role="receiver-diagnostics"]');
    await expect(diagnostics).toBeVisible();
    await diagnostics.locator("summary").click();
    await diagnostics.getByText("Bob", { exact: true }).scrollIntoViewIfNeeded();
    await expect(diagnostics.getByText("Alice", { exact: true })).toBeVisible();
    await expect(diagnostics.getByText("Bob", { exact: true })).toBeVisible();
    const layout = await page.locator(".client-info").evaluate(node => ({
        width: node.clientWidth, scrollWidth: node.scrollWidth, left: node.getBoundingClientRect().left, right: node.getBoundingClientRect().right,
    }));
    expect(layout.scrollWidth).toBeLessThanOrEqual(layout.width + 1);
    expect(layout.left).toBeGreaterThanOrEqual(0);
    expect(layout.right).toBeLessThanOrEqual(480);
    await dialog.screenshot({ path: testInfo.outputPath("administrator-receiver-diagnostics-de-narrow.png") });
});

test.describe("recent adaptive playback measurements", () => {

test.beforeEach(async ({ page }) => {
    await page.route("**/voice-diagnostics-test", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"></head><body><div class="dlg-overlay"><div class="dlg client-info"><div class="dlg-buttons"><button>Close</button></div></div></div></body></html>` }));
    await page.goto("/voice-diagnostics-test");
});

const render = async (page, track) => page.evaluate(async track => {
    const { setLanguage } = await import("/src/i18n.js");
    const { renderReceiverDiagnostics } = await import("/src/voice-diagnostics-ui.js");
    setLanguage("en");
    renderReceiverDiagnostics(document.querySelector(".dlg-overlay"), {
        nickname: "Receiver", client_report: { age_ms: 0, stale: false, report: {
            volume: 100, voice_limiter: true, gain_normalize: false,
            output_state: "running", output_latency_ms: 20, tracks: [track],
        } },
    }, [{ client_id: "publisher", nickname: "Alice <admin>" }]);
}, track);

test("owner diagnostics show recent delay, silence and playback adaptation separately", async ({ page }) => {
    await render(page, { publisher_id: "publisher", sample_ms: 5000,
        buffer_ms: 501, buffer_target_ms: 400, buffer_minimum_ms: 399,
        loss_percent: 0, jitter_ms: 20, concealment_percent: 1.5,
        non_silent_concealment_percent: 0.3, silent_concealment_percent: 1.2,
        acceleration_percent: 2.1, deceleration_percent: 0,
    });
    await page.getByText("Reception on this member’s client · Owner / Admin", { exact: true }).click();
    const track = page.locator(".ci-diagnostic-track");
    await expect(track.getByText("Alice <admin>", { exact: true })).toBeVisible();
    await expect(track.getByText("5.0 s", { exact: true })).toBeVisible();
    await expect(track.getByText("501.0 ms / 400.0 ms / 399.0 ms", { exact: true })).toBeVisible();
    await expect(track.getByText("1.5% / 0.3% / 1.2%", { exact: true })).toBeVisible();
    await expect(track.getByText("2.1% / 0.0%", { exact: true })).toBeVisible();
    await expect(page.getByText(/not end-to-end latency/)).toBeVisible();
    await expect(track.locator("admin")).toHaveCount(0);
});

test("older clients display unknown optional measurements instead of healthy zeros", async ({ page }) => {
    await render(page, { publisher_id: "publisher", sample_ms: 5000,
        buffer_ms: 501, buffer_target_ms: 400, concealment_percent: 1.5,
    });
    await page.getByText("Reception on this member’s client · Owner / Admin", { exact: true }).click();
    const track = page.locator(".ci-diagnostic-track");
    await expect(track.getByText("501.0 ms / 400.0 ms / —", { exact: true })).toBeVisible();
    await expect(track.getByText("1.5% / — / —", { exact: true })).toBeVisible();
    await expect(track.getByText("— / —", { exact: true })).toHaveCount(2);
    await expect(track).not.toContainText("0.0%");
    await page.evaluate(async () => {
        const { renderReceiverDiagnostics } = await import("/src/voice-diagnostics-ui.js");
        renderReceiverDiagnostics(document.querySelector(".dlg-overlay"), null);
    });
    await expect(page.locator('[data-role="receiver-diagnostics"]')).toHaveCount(0);
});

});
