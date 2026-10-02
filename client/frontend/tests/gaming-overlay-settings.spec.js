import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { Object.assign(settings, args[0]); window.__overlaySaved = args[0]; return ""; }
            if (method === "GamingOverlayAvailable") return window.__overlayAvailable !== false;
            if (method === "UpdateGamingOverlay") { (window.__overlayUpdates ||= []).push(structuredClone(args[0])); return ""; }
            if (method === "GetGamingOverlayMonitors") return [{ id: "primary", name: "Display 1", width: 1920, height: 1080, primary: true }, { id: "left", name: "Display 2", width: 1280, height: 720, primary: false }];
            if (method === "PreviewGamingOverlay") { window.__overlayPreview = args[0]; return ""; }
            if (["ListTabs", "GetPermissions"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("voice overlay hides after five seconds and reappears only on a new connection", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state, { myChannelID: 1, pc: {}, channels: [{ ChannelID: 1, Name: "Lobby" }], clients: [] });
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates?.at(-1)?.active)).toBe(true);
    await page.evaluate(() => { window.__noxa.state.clients.push({ channel_id: 1, nickname: "Speaker", is_speaking: true }); });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0]?.name)).toBe("Speaker");
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active), { timeout: 7000 }).toBe(false);
    await page.evaluate(() => { window.__noxa.state.muted = true; });
    // Polling and speaking/mute changes must not restart the expired notice.
    await page.waitForTimeout(600);
    expect(await page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
    await page.evaluate(() => { window.__noxa.state.myChannelID = 2; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
    await page.evaluate(() => { window.__noxa.state.pc = null; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.pc = {}; window.__noxa.state.sessionGeneration++; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
});

test("overlay preferences stay drafts until Apply, support keyboard positioning and native preview", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.getByLabel("Gaming overlay", { exact: true })).toBeChecked();
    await page.getByLabel("Overlay monitor", { exact: true }).selectOption("left");
    await page.getByLabel("Overlay size", { exact: true }).fill("150");
    await page.getByLabel("Overlay opacity", { exact: true }).fill("40");
    await page.getByLabel("Show only speaking members", { exact: true }).check();
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.focus(); await page.keyboard.press("Shift+ArrowLeft"); await page.keyboard.press("ArrowDown");
    await expect(page.getByLabel("Overlay position", { exact: true })).toHaveValue("custom");
    await expect(page.locator(".overlay-position-readout")).toContainText("90% horizontal, 1% vertical");
    await expect(sample).not.toContainText("Sam");
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Preview on monitor", exact: true }).click();
    const draft = await page.evaluate(() => window.__overlayPreview);
    expect(draft).toMatchObject({ gaming_overlay_monitor: "left", gaming_overlay_scale: 150, gaming_overlay_opacity: 40, gaming_overlay_speakers_only: true, gaming_overlay_position: "custom", gaming_overlay_x: 90, gaming_overlay_y: 1 });
    expect(Object.keys(draft).every(key => key.startsWith("gaming_overlay"))).toBe(true);
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toMatchObject(draft);
});

test("overlay preview drag remains bounded and Cancel discards draft position", async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 900 });
    await page.evaluate(() => window.__noxa.openSettings("application"));
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.scrollIntoViewIfNeeded();
    const tile = await sample.boundingBox(), area = await page.locator(".overlay-position-preview").boundingBox();
    await page.mouse.move(tile.x + 8, tile.y + 8); await page.mouse.down();
    await page.mouse.move(area.x - 100, area.y + area.height + 100, { steps: 5 }); await page.mouse.up();
    await expect(page.locator(".overlay-position-readout")).toContainText("0% horizontal, 100% vertical");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.getByLabel("Overlay position", { exact: true })).toHaveValue("top-right");
});

test("unsupported native overlays disable controls with a clear explanation", async ({ page }) => {
    await page.evaluate(() => { window.__overlayAvailable = false; window.__noxa.openSettings("application"); });
    await expect(page.getByLabel("Gaming overlay", { exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Preview on monitor", exact: true })).toBeDisabled();
    await expect(page.getByText("The native gaming overlay is available on Windows.", { exact: true })).toBeVisible();
});
