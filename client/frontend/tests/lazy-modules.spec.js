import { expect, test } from "./fixtures.js";

// (83) Settings is a code-split chunk: what other modules rely on at startup
// (openSettings, settingsPages, the deep-link event, hotkey_status) must work
// before and after the chunk loads.
test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__handlers = {};
        window.runtime = {
            EventsOn: (name, fn) => { (window.__handlers[name] ||= []).push(fn); return () => {}; },
            EventsEmit() {}, WindowIsFullscreen: async () => false,
        };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], volume: 100, vad_threshold: 50, activation_mode: "ptt", hotkey_ptt: "F9" };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { Object.assign(settings, args[0]); return ""; }
            if (["ListTabs", "ListIdentities", "GetPermissions", "SubscriptionsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("Settings opens through the startup launcher and publishes its pages once loaded", async ({ page }) => {
    const opened = await page.evaluate(async () => {
        const result = window.__noxa.openSettings("application");
        await result;
        return true;
    });
    expect(opened).toBe(true);
    await expect(page.getByLabel("Search settings", { exact: true })).toBeVisible();
    const pages = await page.evaluate(() => window.__noxa.settingsPages());
    expect(Array.isArray(pages) && pages.length > 5).toBe(true);
    expect(pages.some(p => p.id === "hotkeys")).toBe(true);
    await expect(page.locator("body")).not.toHaveAttribute("aria-busy", "true");
});

test("the noxa-open-settings deep link opens the requested page", async ({ page }) => {
    await page.evaluate(() => window.dispatchEvent(new CustomEvent("noxa-open-settings", { detail: "hotkeys" })));
    await expect(page.locator(".hk-map-row").first()).toBeVisible();
});

test("a hotkey conflict reported before Settings first opens is shown on the hotkeys page", async ({ page }) => {
    await page.evaluate(() => {
        for (const fn of window.__handlers.hotkey_status || []) fn({ action: "ptt", error: "already used by another app" });
    });
    await page.evaluate(() => window.__noxa.openSettings("hotkeys"));
    const row = page.locator('.hk-map-row[data-hotkey-action="ptt"]');
    await expect(row.locator(".hk-err")).toHaveText("already used by another app");
    // A later status clears it in the open dialog.
    await page.evaluate(() => {
        for (const fn of window.__handlers.hotkey_status || []) fn({ action: "ptt", error: "" });
    });
    await expect(row.locator(".hk-err")).toHaveText("");
});
