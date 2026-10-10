import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn(name, fn) { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async () => {
            if (method === "GetSettings") return { language: "en", activation_mode: "ptt", volume: 100, onboarding_done: true, alpha_dismissed: "0.5.0-dev+gabc123", window_opacity: 100 };
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (method === "Connected" || method === "IsGuest") return false;
            if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.renderTree);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "one", myClientID: "self", myUniqueID: "me", myChannelID: 1,
            channels: [{ ChannelID: 1, Name: "Public1" }, { ChannelID: 2, Name: "Other" }],
            clients: [{ client_id: "self", unique_id: "me", nickname: "Daniel", channel_id: 1 },
                { client_id: "cari", unique_id: "cari-uid", nickname: "Cari", channel_id: 1, is_speaking: true },
                { client_id: "away", unique_id: "away-uid", nickname: "Elsewhere", channel_id: 2, is_speaking: true }] });
        v.renderTree();
    });
});

const speaker = page => page.locator('#channel-tree .client[data-clid="cari"]');
const frame = page => speaker(page).locator("canvas").evaluate(canvas => canvas.toDataURL());

test("C20 animates the avatar without moving its row or covering the photo", async ({ page }, testInfo) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    const row = speaker(page);
    await expect(row.locator(".speaking-avatar canvas")).toBeVisible();
    await expect(page.locator(".client-voice-state")).toHaveCount(0);
    await expect(page.locator('.client[data-clid="away"] canvas')).toHaveCount(0);
    await expect(row).toHaveAccessibleName(/speaking/);
    const before = await frame(page);
    await expect.poll(() => frame(page)).not.toBe(before);
    const size = await row.locator(".avatar").boundingBox();
    expect(size.width).toBe(26); expect(size.height).toBe(26);
    const name = await row.locator(".client-name").boundingBox();
    expect(name.x - size.x - size.width).toBeGreaterThanOrEqual(14);
    await page.evaluate(() => { window.__noxa.state.clients[1].is_speaking = false; window.__noxa.renderTree(); });
    expect(await row.locator(".avatar").boundingBox()).toEqual(size);
    expect(await row.locator(".client-name").boundingBox()).toEqual(name);
    await page.evaluate(() => {
        const canvas = document.createElement("canvas"); canvas.width = canvas.height = 26;
        const ctx = canvas.getContext("2d"); ctx.fillStyle = "#d6a34e"; ctx.fillRect(0, 0, 26, 26);
        window.__noxa.state.avatars.set("cari-uid", canvas.toDataURL());
        window.__noxa.state.clients[1].is_speaking = true;
        window.__noxa.renderTree();
    });
    await expect(row.locator(".avatar img")).toBeVisible();
    await expect(row.locator(".speaking-avatar canvas")).toBeVisible();
    expect((await row.locator(".avatar").boundingBox()).width).toBe(26);
    await page.screenshot({ path: testInfo.outputPath("c20-speaking-avatar.png") });
});

test("mute, speech end and leaving voice remove C20 even if a stale speaking flag remains", async ({ page }) => {
    for (const key of ["input_muted", "self_muted", "self_deafened", "server_muted"]) {
        await page.evaluate(key => { const v = window.__noxa; v.state.clients[1][key] = true; v.renderTree(); }, key);
        await expect(speaker(page).locator("canvas")).toHaveCount(0);
        await page.evaluate(key => { const v = window.__noxa; v.state.clients[1][key] = false; v.renderTree(); }, key);
        await expect(speaker(page).locator("canvas")).toHaveCount(1);
    }
    await page.evaluate(() => { for (const fn of window.__events.event) fn(JSON.stringify({ type: "speaking_changed", data: { client_id: "cari", speaking: false } })); });
    await expect(speaker(page).locator("canvas")).toHaveCount(0);
    await page.evaluate(() => { const v = window.__noxa; v.state.clients[1].is_speaking = true; v.state.myChannelID = 0; v.renderTree(); });
    await expect(page.locator("#channel-tree canvas")).toHaveCount(0);
});

test("reduced motion keeps a static speaking glow and live preference changes resume animation", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    const row = speaker(page);
    await expect(row.locator("canvas")).toHaveCount(1);
    const before = await frame(page);
    await page.waitForTimeout(160);
    expect(await frame(page)).toBe(before);
    await expect(row.locator(".avatar")).toHaveCSS("animation-name", "none");
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await expect.poll(() => frame(page)).not.toBe(before);
    await page.evaluate(() => { document.documentElement.dataset.reduceMotion = "1"; });
    await expect.poll(() => frame(page)).toBe(before);
    await page.waitForTimeout(100);
    expect(await frame(page)).toBe(before);
    await page.evaluate(() => { delete document.documentElement.dataset.reduceMotion; });
    await expect.poll(() => frame(page)).not.toBe(before);
});

test("tree refreshes replace animation targets and collapsed speakers stop painting", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await expect(speaker(page).locator("canvas")).toBeVisible();
    await page.evaluate(() => { window.__oldSpeakingCanvas = document.querySelector('.speaking-avatar canvas'); window.__noxa.renderTree(); });
    const detached = await page.evaluate(() => window.__oldSpeakingCanvas.toDataURL());
    const active = await frame(page);
    await expect.poll(() => frame(page)).not.toBe(active);
    expect(await page.evaluate(() => window.__oldSpeakingCanvas.toDataURL())).toBe(detached);
    await page.evaluate(() => { window.__noxa.state.collapsedChannels.add(1); window.__noxa.renderTree(); });
    await expect(page.locator("#channel-tree canvas")).toHaveCount(0);
    await page.evaluate(() => { window.__noxa.state.collapsedChannels.delete(1); window.__noxa.renderTree(); });
    const reopened = await frame(page);
    await expect.poll(() => frame(page)).not.toBe(reopened);
});
