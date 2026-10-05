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
                        concealment_events: 4, jitter_ms: 7, loss_percent: 5, concealment_percent: 4, buffer_ms: 80, buffer_target_ms: 60, audio_level: 0.3 },
                    { track_id: "c-bob", publisher_id: "c-bob", codec: "audio/opus", sample_ms: 5000, packets_received: 200,
                        packets_lost: 0, loss_percent: 0, concealment_percent: 0, jitter_ms: 2, buffer_ms: 30, buffer_target_ms: 25, audio_level: 0.1 },
                ],
            } },
            transport: { connection_state: "connected", receiver_reports: [] },
        };
    });
}

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
