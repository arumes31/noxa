import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__auditSaved = [];
        window.__auditRequests = [];
        window.runtime = { EventsOn: () => () => {}, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200, volume: 100, vad_threshold: 50, activation_mode: "ptt" };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { window.__auditSaved.push(structuredClone(args[0])); Object.assign(settings, args[0]); return ""; }
            if (["ListTabs", "ListIdentities", "GetPermissions", "SubscriptionsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            if (method === "HistorySearchPageForTab") { window.__auditRequests.push(args); return { messages: [], scanned: 0, complete: true }; }
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("settings search selection focuses the matching control and navigation clears the query", async ({ page }, testInfo) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await page.getByLabel("Search settings", { exact: true }).fill("chat max lines");
    await page.locator(".set-search-hit").first().click();
    await page.screenshot({ path: testInfo.outputPath("settings-search-focus.png") });
    await expect(page.getByLabel("Chat max lines", { exact: true })).toBeFocused();
    await page.getByLabel("Search settings", { exact: true }).fill("voice");
    await page.getByRole("tab", { name: "Camera", exact: true }).click();
    await expect(page.getByLabel("Search settings", { exact: true })).toHaveValue("");
});

test("history search submits with Enter and explains invalid dates", async ({ page }, testInfo) => {
    await page.evaluate(() => { window.__noxa.showWorkspace(false); Object.assign(window.__noxa.state, { activeTabID: "a", serverGeneration: 1, myUniqueID: "me", channels: [{ ChannelID: 7, Name: "Lobby" }] }); });
    await page.getByRole("button", { name: "Search history", exact: true }).click();
    await page.getByLabel("After", { exact: true }).fill("2026-09-29");
    await page.getByLabel("Before", { exact: true }).fill("2026-09-28");
    await page.getByRole("dialog").getByRole("button", { name: "Search", exact: true }).click();
    await page.screenshot({ path: testInfo.outputPath("search-dates.png") });
    await expect(page.locator(".message-tool-status")).toContainText("start date");
    expect(await page.evaluate(() => window.__auditRequests.length)).toBe(0);
    await page.getByLabel("After", { exact: true }).fill("2026-09-27");
    await page.getByLabel("Search text", { exact: true }).fill("hello");
    await page.getByLabel("Search text", { exact: true }).press("Enter");
    await expect.poll(() => page.evaluate(() => window.__auditRequests.length)).toBeGreaterThan(0);
});

test("settings controls remain reachable in a narrow window", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 500, height: 780 });
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    await expect(page.getByRole("tablist")).toHaveAttribute("aria-orientation", "horizontal");
    await page.getByRole("tab", { name: "Capture", exact: true }).focus();
    await page.keyboard.press("ArrowRight");
    await expect(page.getByRole("tab", { name: "Camera", exact: true })).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await page.screenshot({ path: testInfo.outputPath("settings-narrow-capture.png") });
    expect(await page.locator("#settings-content").evaluate(content => content.scrollWidth <= content.clientWidth + 1)).toBe(true);
    await page.getByRole("tab", { name: "Application", exact: true }).click();
    await page.screenshot({ path: testInfo.outputPath("settings-narrow-application.png") });
    const overflowing = await page.locator("#settings-content").evaluate(content => [...content.querySelectorAll("input,select,textarea,button")].filter(control => control.getBoundingClientRect().right > content.getBoundingClientRect().right).map(control => ({label: control.closest(".set-row")?.querySelector(".set-label")?.textContent || control.textContent, right: control.getBoundingClientRect().right})));
    expect(overflowing).toEqual([]);
});

test("invalid numeric settings stay local and focus their control before saving", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    const lines = page.getByLabel("Chat max lines", { exact: true });
    for (const value of ["", "9", "5001", "12.5"]) {
        await lines.fill(value);
        await page.locator("#set-apply").click();
        await expect(lines).toBeFocused();
        expect(await page.evaluate(() => window.__auditSaved.length)).toBe(0);
    }
    await page.getByRole("tab", { name: "Camera", exact: true }).click();
    await page.getByRole("tab", { name: "Application", exact: true }).click();
    await expect(lines).toHaveValue("200");
    await lines.fill("777");
    await page.locator("#set-apply").click();
    expect(await page.evaluate(() => window.__auditSaved.at(-1).chat_max_lines)).toBe(777);
});

test("switching hotkey profiles cancels capture from the removed row", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state.settings, { hotkey_ptt: "F9", hotkey_profiles: { Studio: { ptt: "Ctrl+F7" } } });
        window.__noxa.openSettings("hotkeys");
    });
    await page.locator(".hotkey-capture").first().click();
    await page.locator("#settings-content select").selectOption("Studio");
    await page.keyboard.press("F8");
    await page.locator("#set-apply").click();
    await expect.poll(() => page.evaluate(() => window.__auditSaved.length)).toBe(1);
    expect(await page.evaluate(() => window.__auditSaved[0].hotkey_profiles.Studio.ptt)).toBe("Ctrl+F7");
    expect(await page.evaluate(() => window.__auditSaved[0].hotkey_ptt)).toBe("F9");
});

test("starting a different hotkey capture restores the previous binding label", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state.settings, { hotkey_ptt: "F9", hotkey_mute: "Ctrl+M" });
        window.__noxa.openSettings("hotkeys");
    });
    const bindings = page.locator(".hotkey-capture");
    await bindings.nth(0).click();
    await bindings.nth(1).click();
    await expect(bindings.nth(0)).toHaveText("F9");
    await expect(bindings.nth(0)).not.toHaveClass(/capturing/);
    await page.keyboard.press("Control+F8");
    await expect(bindings.nth(1)).toHaveText("Ctrl+F8");
    await page.locator("#set-apply").click();
    expect(await page.evaluate(() => window.__auditSaved[0].hotkey_ptt)).toBe("F9");
    expect(await page.evaluate(() => window.__auditSaved[0].hotkey_mute)).toBe("Ctrl+F8");
});

test("theme swatches and User CSS preserve each other's edits", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    const css = page.getByLabel("User CSS", { exact: true });
    const background = page.getByLabel("Background", { exact: true });
    await background.fill("#123456");
    await background.dispatchEvent("input");
    await expect(css).toHaveValue(/--bg: #123456;/);
    await css.fill((await css.inputValue()).replace("#123456", "#345678") + "\n.audit-rule { opacity: .8; }");
    await css.press("Tab");
    await expect(background).toHaveValue("#345678");
    await page.getByLabel("Panel", { exact: true }).fill("#456789");
    await page.getByLabel("Panel", { exact: true }).dispatchEvent("input");
    await page.locator("#set-apply").click();
    const saved = await page.evaluate(() => window.__auditSaved[0].user_css);
    expect(saved).toContain("--bg: #345678;");
    expect(saved).toContain("--bg-panel: #456789;");
    expect(saved).toContain(".audit-rule { opacity: .8; }");
});
