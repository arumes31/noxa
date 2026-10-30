import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.__saved = [];
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200, volume: 100,
            gaming_overlay: true, gaming_overlay_position: "center-left", gaming_overlay_opacity: 70, gaming_overlay_scale: 80, gaming_overlay_game_ui: "mmo", gaming_overlay_avoid_zones: true };
        const defaults = { volume: 100, chat_max_lines: 200, gaming_overlay: true, gaming_overlay_position: "center-left", gaming_overlay_opacity: 88, gaming_overlay_scale: 80,
            gaming_overlay_game_ui: "none", gaming_overlay_avoid_zones: true, gaming_overlay_animate: true, gaming_overlay_style: "default",
            gaming_overlay_self_color: "#43c798", gaming_overlay_blue_start: 195, gaming_overlay_blue_end: 235, gaming_overlay_x: 0, gaming_overlay_y: 0 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { Object.assign(settings, args[0]); window.__saved.push(structuredClone(args[0])); return ""; }
            if (method === "DefaultSettingsSnapshot") return window.__noDefaults ? "" : structuredClone(defaults);
            if (method === "GamingOverlayAvailable") return true;
            if (method === "GetGamingOverlayMonitors") return [{ id: "primary", name: "Display 1", width: 1920, height: 1080, primary: true }];
            if (method === "PreviewGamingOverlay") { window.__overlayPreview = args[0]; return ""; }
            if (["ListTabs", "GetPermissions", "ListIdentities"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("update switches keep their width and periodic preferences persist", async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 768 });
    await page.evaluate(() => window.__noxa.openSettings('general', { section: 'updates' }));
    const periodic = page.getByLabel('Check for updates periodically', { exact: true });
    await expect(periodic).toBeChecked();
    const interval = page.getByLabel('Check interval (hours)', { exact: true });
    await expect(interval).toHaveValue('2');
    const widths = await page.locator('[data-section="updates"] input.set-switch').evaluateAll(nodes => nodes.map(el => el.getBoundingClientRect().width));
    expect(widths.every(width => width === 42)).toBe(true);
    await periodic.uncheck(); await interval.selectOption('4');
    await page.getByRole('button', { name: 'Apply', exact: true }).click();
    expect(await page.evaluate(() => window.__saved.at(-1))).toMatchObject({ updates_check_periodically: false, updates_check_interval_hours: 4 });
});

test("Privacy contains lobby visibility and link previews with lobby hiding off by default", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings({ setting: 'hide_lobby_presence' }));
    await expect(page.getByRole('tab', { name: 'Privacy', exact: true })).toHaveAttribute('aria-selected', 'true');
    const hidden = page.getByLabel('Hide my presence in the lobby', { exact: true });
    await expect(hidden).not.toBeChecked();
    await expect(page.locator('[data-setting="link_previews"]')).toHaveCount(1);
    await hidden.check();
    await page.getByRole('button', { name: 'Apply', exact: true }).click();
    expect(await page.evaluate(() => window.__saved.at(-1)?.hide_lobby_presence)).toBe(true);
});

test("rail lists the categories, deep links open pages, sections and settings", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings());
    const dialog = page.getByRole("dialog", { name: "Settings", exact: true });
    await expect(dialog).toBeVisible();
    const tabs = page.getByRole("tab");
    await expect(tabs).toHaveText(["General", "Audio & voice", "Streams", "Fullscreen overlay", "Hotkeys", "Appearance", "Privacy", "Chat", "Notifications", "Whisper", "Connection"]);
    await expect(page.getByRole("tab", { name: "General", exact: true })).toHaveAttribute("aria-selected", "true");
    await expect(page.locator("#settings-page-title")).toHaveText("General");
    await expect(page.locator("#settings-page-desc")).not.toBeEmpty();
    expect(await page.evaluate(() => window.__noxa.settingsPages().map(item => item.id))).toEqual(
        ["general", "audio", "streams", "overlay", "hotkeys", "appearance", "privacy", "chat", "notifications", "whisper", "connection"]);

    // Legacy id with a section.
    await page.evaluate(() => window.__noxa.openSettings("playback"));
    await expect(page.getByRole("tab", { name: "Audio & voice", exact: true })).toHaveAttribute("aria-selected", "true");
    await expect(page.locator('[data-section="output"] .set-group-title')).toBeFocused();

    // Event-based deep link to a single setting.
    await page.evaluate(() => window.dispatchEvent(new CustomEvent("noxa-open-settings", { detail: { page: "overlay", setting: "gaming_overlay_avoid_zones" } })));
    await expect(page.getByRole("tab", { name: "Fullscreen overlay", exact: true })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByLabel("Avoid game UI zones", { exact: true })).toBeFocused();
    await page.evaluate(() => window.__noxa.openSettings({ setting: "chat_density" }));
    await expect(page.getByLabel("Density", { exact: true })).toBeFocused();
});

test("keyboard: rail arrows and Home/End, focus stays in the dialog, Escape closes", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("hotkeys"));
    const active = page.getByRole("tab", { name: "Hotkeys", exact: true });
    await expect(active).toBeFocused();
    await page.keyboard.press("End");
    await expect(page.getByRole("tab", { name: "Connection", exact: true })).toBeFocused();
    await expect(page.locator("#settings-page-title")).toHaveText("Connection");
    await page.keyboard.press("Home");
    await expect(page.getByRole("tab", { name: "General", exact: true })).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("ArrowDown");
    await expect(page.getByRole("tab", { name: "Audio & voice", exact: true })).toBeFocused();
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("ArrowUp");
    await expect(page.getByRole("tab", { name: "Connection", exact: true })).toBeFocused();
    await page.getByRole("tab", { name: "General", exact: true }).click();
    for (let i = 0; i < 40; i++) {
        await page.keyboard.press("Tab");
        expect(await page.evaluate(() => !!document.activeElement?.closest("#settings-overlay"))).toBe(true);
    }
    await expect(page.getByRole("button", { name: "Close settings", exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    await page.evaluate(() => window.__noxa.openSettings());
    await page.getByRole("button", { name: "Close settings", exact: true }).click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
});

test("Reset to default restores only the current page in the draft", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    await expect(page.getByLabel("Overlay opacity", { exact: true })).toHaveValue("70");
    await page.getByRole("button", { name: "Reset to default", exact: true }).click();
    await expect(page.getByLabel("Overlay opacity", { exact: true })).toHaveValue("88");
    await expect(page.getByLabel("Game UI preset", { exact: true })).toHaveValue("none");
    await expect(page.locator(".settings-save-status")).toContainText("Fullscreen overlay reset to defaults");
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    await expect(page.getByLabel("Overlay opacity", { exact: true })).toHaveValue("70");
    await page.getByRole("button", { name: "Reset to default", exact: true }).click();
    await page.getByRole("button", { name: "Done", exact: true }).click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    const saved = await page.evaluate(() => window.__saved.at(-1));
    expect(saved).toMatchObject({ gaming_overlay_opacity: 88, gaming_overlay_game_ui: "none", chat_max_lines: 200 });

    await page.evaluate(() => { window.__noDefaults = true; window.__noxa.openSettings("chat"); });
    await page.getByRole("button", { name: "Reset to default", exact: true }).click();
    await expect(page.locator(".settings-save-status")).toContainText("Defaults are unavailable");
});

test("fullscreen overlay: preset zones block anchors, drops snap clear and Apply saves the placement", async ({ page }, testInfo) => {
    test.setTimeout(60_000);
    await page.setViewportSize({ width: 1440, height: 960 });
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const grid = page.getByRole("radiogroup", { name: "Speaker anchor", exact: true });
    await expect(grid.getByRole("radio")).toHaveCount(8);
    await expect(page.locator(".ovl-anchor-name")).toHaveText("Middle left");
    await expect(page.locator(".ovl-anchor-free")).toHaveText("4 of 8 anchors free with MMO preset");
    await expect(page.locator(".ovl-zone")).toHaveText(["Unit frames", "Map", "Game chat", "Action bars"]);
    const topLeft = grid.getByRole("radio", { name: /^Top left/ });
    await expect(topLeft).toHaveAttribute("aria-disabled", "true");
    await expect(topLeft).toHaveAccessibleName("Top left (blocked by Unit frames)");
    await page.locator(".ovl-stage").screenshot({ path: testInfo.outputPath("overlay-stage-mmo.png") });

    // A blocked anchor explains itself and keeps the placement (aria-disabled
    // cells stay clickable so the explanation reaches pointer users too).
    await topLeft.click({ force: true });
    await expect(page.locator(".ovl-status")).toContainText("would cover Unit frames");
    await expect(grid.getByRole("radio", { name: "Middle left", exact: true })).toHaveAttribute("aria-checked", "true");

    // Arrow keys move between anchors; Space picks one.
    await grid.getByRole("radio", { name: "Middle left", exact: true }).focus();
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("ArrowRight");
    await expect(grid.getByRole("radio", { name: "Top center", exact: true })).toBeFocused();
    await page.keyboard.press("Space");
    await expect(grid.getByRole("radio", { name: "Top center", exact: true })).toHaveAttribute("aria-checked", "true");
    await expect(page.locator(".overlay-position-readout")).toContainText("50% horizontal, 0% vertical");

    // Dropping the sample on the game chat snaps it to the nearest free anchor.
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    const box = await sample.boundingBox(), stage = await page.locator(".ovl-stage").boundingBox();
    await page.mouse.move(box.x + 4, box.y + 4); await page.mouse.down();
    await page.mouse.move(stage.x + 2, stage.y + stage.height - 2, { steps: 6 }); await page.mouse.up();
    await expect(page.locator(".ovl-status")).toContainText("Game chat is a no-go zone");
    await expect(page.locator(".ovl-anchor-name")).toHaveText("Middle left");

    // Turning avoidance off lets the overlay sit on a hatched anchor.
    await page.getByLabel("Avoid game UI zones", { exact: true }).uncheck();
    await topLeft.click();
    await expect(topLeft).toHaveAttribute("aria-checked", "true");
    await page.getByLabel("Avoid game UI zones", { exact: true }).check();
    await expect(page.locator(".ovl-status")).toContainText("Moved to");
    await expect(topLeft).toHaveAttribute("aria-checked", "false");

    // A preset change re-evaluates the anchors.
    await page.getByLabel("Game UI preset", { exact: true }).selectOption("moba");
    await expect(page.locator(".ovl-anchor-free")).toHaveText("3 of 8 anchors free with MOBA preset");
    await page.getByLabel("Game UI preset", { exact: true }).selectOption("none");
    await expect(page.locator(".ovl-anchor-free")).toHaveText("All 8 anchors free");
    await expect(page.locator(".ovl-zone")).toHaveCount(0);

    // The behaviour toggles are live controls now; nothing is "coming soon".
    await expect(page.getByText("Coming soon")).toHaveCount(0);
    for (const name of ["Only show who's speaking", "Chat peek", "Auto-hide when idle"]) await expect(page.getByLabel(name, { exact: true })).toBeEnabled();

    await page.getByRole("button", { name: "Preview in game", exact: true }).click();
    expect(await page.evaluate(() => window.__overlayPreview.gaming_overlay_position)).toBeTruthy();
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    const saved = await page.evaluate(() => window.__saved.at(-1));
    expect(saved.gaming_overlay_game_ui).toBe("none");
    expect(saved.gaming_overlay_avoid_zones).toBe(true);
    expect(saved.gaming_overlay_style).toBe("default");
    expect(saved.gaming_overlay_animate).not.toBe(false);
});

test("overlay keyboard moves jump across no-go zones instead of entering them", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.focus();
    // Middle left -> down: the game chat zone starts below, so the move is refused.
    for (let i = 0; i < 4; i++) await page.keyboard.press("Shift+ArrowDown");
    await expect(page.locator(".ovl-status")).toContainText("Game UI is in the way");
    const placement = await page.evaluate(() => {
        const text = document.querySelector(".overlay-position-readout").textContent;
        return text.match(/(\d+)% horizontal, (\d+)% vertical/).slice(1).map(Number);
    });
    expect(placement[1]).toBeLessThan(70);
});
