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

test("voice overlay shows only active speakers and hides immediately on silence", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state, { myClientID: 1, myChannelID: 1, pc: {}, channels: [{ ChannelID: 1, Name: "Lobby" }], clients: [
            { client_id: 1, channel_id: 1, nickname: "Self", unique_id: "self-uid", is_speaking: false },
            { client_id: 2, channel_id: 1, nickname: "Peer", unique_id: "peer-uid", is_speaking: false },
        ] });
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates?.at(-1)?.active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.clients[0].is_speaking = true; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Self", speaking: true });
    // Continuous speech must survive the old five-second visibility limit.
    await page.waitForTimeout(5500);
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
    await page.evaluate(() => { window.__noxa.state.muted = true; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.clients[1].is_speaking = true; });
    await page.waitForTimeout(600);
    expect(await page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
    await page.evaluate(() => { window.__noxa.state.muted = false; window.__noxa.state.clients[0].is_speaking = false; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Peer", speaking: true });
    await page.evaluate(() => { window.__noxa.state.deafened = true; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
    await page.evaluate(() => { window.__noxa.state.deafened = false; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Peer", speaking: true });
    await page.evaluate(() => { window.__noxa.state.pc = null; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.pc = {}; window.__noxa.state.sessionGeneration++; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
});

test("global and private-call mute controls hide the overlay until cleared", async ({ page }) => {
    await page.evaluate(() => {
        window.__overlayCall = { active: true, label: "Private call", speakers: [{ id: "peer", name: "Peer", speaking: true }] };
        window.__noxaPrivateCalls.overlaySnapshot = () => window.__overlayCall;
    });
    for (const target of ["global", "private-call"]) {
        for (const flag of ["muted", "deafened"]) {
            await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Peer", speaking: true });
            await page.evaluate(({ target, flag }) => {
                (target === "global" ? window.__noxa.state : window.__overlayCall)[flag] = true;
            }, { target, flag });
            await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
            await page.evaluate(({ target, flag }) => {
                (target === "global" ? window.__noxa.state : window.__overlayCall)[flag] = false;
            }, { target, flag });
        }
    }
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
});

test("overlay preferences stay drafts until Apply, support keyboard positioning and native preview", async ({ page }, testInfo) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.getByLabel("Voice overlay", { exact: true })).toHaveCount(0);
    await page.getByRole("tab", { name: "Overlay", exact: true }).click();
    await expect(page.getByRole("tabpanel", { name: "Overlay", exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("overlay-settings-section.png") });
    await expect(page.getByLabel("Voice overlay", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Overlay monitor", { exact: true })).toHaveValue("");
    await page.getByLabel("Overlay monitor", { exact: true }).selectOption("left");
    await page.getByLabel("Overlay size", { exact: true }).fill("150");
    await page.getByLabel("Overlay opacity", { exact: true }).fill("40");
    await expect(page.getByLabel("Show only speaking members", { exact: true })).toHaveCount(0);
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.focus(); await page.keyboard.press("Shift+ArrowRight"); await page.keyboard.press("ArrowDown");
    await expect(page.getByLabel("Overlay position", { exact: true })).toHaveValue("custom");
    await expect(page.locator(".overlay-position-readout")).toContainText("10% horizontal, 46% vertical");
    await expect(sample).toContainText("Sam");
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Preview on monitor", exact: true }).click();
    const draft = await page.evaluate(() => window.__overlayPreview);
    expect(draft).toMatchObject({ gaming_overlay_monitor: "left", gaming_overlay_scale: 150, gaming_overlay_opacity: 40, gaming_overlay_position: "custom", gaming_overlay_x: 10, gaming_overlay_y: 46 });
    expect(Object.keys(draft).every(key => key.startsWith("gaming_overlay"))).toBe(true);
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toMatchObject(draft);
    await page.getByRole("tab", { name: "Application", exact: true }).click();
    await page.locator("#settings-search").fill("Overlay opacity");
    const hit = page.locator(".set-search-hit");
    await expect(hit).toHaveCount(1);
    await expect(hit.locator(".set-search-page")).toHaveText("Overlay");
    await hit.click();
    await expect(page.getByRole("tab", { name: "Overlay", exact: true })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByLabel("Overlay opacity", { exact: true })).toHaveValue("40");
});

test("speaker avatars reach the native overlay as bounded thumbnails and clear across sessions", async ({ page }) => {
    await page.evaluate(() => {
        const canvas = document.createElement("canvas"); canvas.width = 128; canvas.height = 256;
        const ctx = canvas.getContext("2d"); ctx.fillStyle = "#dc0000"; ctx.fillRect(0, 0, 128, 256);
        const state = window.__noxa.state;
        state.avatars.set("peer-uid", canvas.toDataURL("image/png"));
        Object.assign(state, { myClientID: 1, myChannelID: 1, pc: {}, clients: [
            { client_id: 2, channel_id: 1, unique_id: "peer-uid", nickname: "Peer", is_speaking: true },
            { client_id: 3, channel_id: 1, unique_id: "quiet-uid", nickname: "Quiet", is_speaking: false },
        ] });
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0]?.avatar)).toMatch(/^data:image\/png;base64,/);
    const avatar = await page.evaluate(async () => {
        const snapshot = window.__overlayUpdates.at(-1);
        const source = snapshot.speakers[0].avatar;
        const img = new Image(); img.src = source; await img.decode();
        return { count: snapshot.speakers.length, width: img.naturalWidth, height: img.naturalHeight, bytes: source.length };
    });
    expect(avatar).toMatchObject({ count: 1, width: 64, height: 64 }); expect(avatar.bytes).toBeLessThanOrEqual(32768);
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.serverGeneration++; state.avatars.clear();
        state.avatars.set("peer-uid", "https://invalid.example/avatar.png");
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0]?.avatar)).toBe("");
    await page.evaluate(() => { window.__noxa.state.clients[0].is_speaking = false; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
});

test("overlay animation overrides system motion settings and has its own saved toggle", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const bars = page.locator(".overlay-sample-wave i");
    await expect(bars).toHaveCount(22);
    const motion = await bars.evaluateAll(items => items.map(item => {
        const css = getComputedStyle(item); return { animation: css.animationName, delay: css.animationDelay };
    }));
    expect(motion.every(item => item.animation === "overlay-speaking")).toBe(true);
    expect(motion[0].delay).not.toBe(motion[11].delay);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await expect(bars.first()).toHaveCSS("animation-name", "overlay-speaking");
    const animation = page.getByLabel("Animate speaking indicators", { exact: true });
    await expect(animation).toBeChecked();
    await animation.uncheck();
    await expect(bars.first()).toHaveCSS("animation-name", "none");
    await page.getByRole("button", { name: "Preview on monitor", exact: true }).click();
    expect(await page.evaluate(() => window.__overlayPreview.gaming_overlay_animate)).toBe(false);
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved.gaming_overlay_animate)).toBe(false);
    await animation.check();
    await expect(bars.first()).toHaveCSS("animation-name", "overlay-speaking");
});

test("overlay preview drag remains bounded and Cancel discards draft position", async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 900 });
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.scrollIntoViewIfNeeded();
    const tile = await sample.boundingBox(), area = await page.locator(".overlay-position-preview").boundingBox();
    await page.mouse.move(tile.x + 8, tile.y + 8); await page.mouse.down();
    await page.mouse.move(area.x - 100, area.y + area.height + 100, { steps: 5 }); await page.mouse.up();
    await expect(page.locator(".overlay-position-readout")).toContainText("0% horizontal, 100% vertical");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    await expect(page.getByLabel("Overlay position", { exact: true })).toHaveValue("center-left");
});

test("unsupported native overlays disable controls with a clear explanation", async ({ page }) => {
    await page.evaluate(() => { window.__overlayAvailable = false; window.__noxa.openSettings("overlay"); });
    await expect(page.getByLabel("Voice overlay", { exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Preview on monitor", exact: true })).toBeDisabled();
    await expect(page.getByText("The native voice overlay is available on Windows.", { exact: true })).toBeVisible();
});
