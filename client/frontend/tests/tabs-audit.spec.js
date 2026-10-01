import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__tabEvents = {};
        window.__tabActions = [];
        window.__auditTabs = [
            { id: "tabs-a", addr: "a.test:12333", nickname: "Me", active: true, connected: true },
            { id: "tabs-b", addr: "b.test:12333", nickname: "Other", active: false, connected: true },
        ];
        window.runtime = { EventsOn(name, callback) { (window.__tabEvents[name] ||= []).push(callback); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], activation_mode: "ptt", chat_max_lines: 200 };
            if (method === "ListTabs") return structuredClone(window.__auditTabs);
            if (["SetActiveTab", "DisconnectTab"].includes(method)) { window.__tabActions.push([method, ...args]); return ""; }
            if (["ListIdentities", "GetPermissions", "SubscriptionsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxaTabs);
    await page.evaluate(() => {
        for (const callback of window.__tabEvents.tab_update) callback(structuredClone(window.__auditTabs));
    });
    await expect(page.locator('[data-tab-id="tabs-b"]')).toBeVisible();
});

for (const control of ["select", "close", "plus"]) test(`server-tab ${control} keeps keyboard focus across background updates`, async ({ page }) => {
    const selector = control === "plus" ? "#srv-tab-plus" : `[data-tab-id="tabs-b"] .srv-tab-${control === "close" ? "x" : "select"}`;
    const button = page.locator(selector);
    await button.focus();
    await page.evaluate(selector => {
        window.__focusedTabControl = document.querySelector(selector);
        window.__auditTabs[1].unread = 3;
        window.__auditTabs[1].connected = false;
        window.__auditTabs[1].nickname = "Updated name";
        for (const callback of window.__tabEvents.tab_update) callback(structuredClone(window.__auditTabs));
    }, selector);
    await expect(button).toBeFocused();
    expect(await page.evaluate(() => window.__focusedTabControl.isConnected)).toBe(true);
    await expect(page.locator('[data-tab-id="tabs-b"]')).toHaveClass(/offline/);
    await expect(page.locator('[data-tab-id="tabs-b"] .srv-badge')).toHaveText("3");
    await expect(page.locator('[data-tab-id="tabs-b"] .srv-tab-select')).toHaveAccessibleName("Updated name @ b.test:12333, offline");
    await page.keyboard.press("Enter");
    if (control === "plus") await expect(page.locator("#login-overlay")).toBeVisible();
    else await expect.poll(() => page.evaluate(() => window.__tabActions)).toEqual([[control === "close" ? "DisconnectTab" : "SetActiveTab", "tabs-b"]]);
});

test("tab updates keep controls attached when another tab is removed or inserted", async ({ page }) => {
    const button = page.locator('[data-tab-id="tabs-b"] .srv-tab-select');
    await button.focus();
    await page.evaluate(() => {
        window.__focusedTabControl = document.activeElement;
        window.__auditTabs = [{ id: "tabs-c", addr: "c.test:12333", nickname: "New", connected: true }, window.__auditTabs[1]];
        for (const callback of window.__tabEvents.tab_update) callback(structuredClone(window.__auditTabs));
    });
    await expect(button).toBeFocused();
    expect(await page.evaluate(() => window.__focusedTabControl.isConnected)).toBe(true);
    await expect(page.locator(".srv-tab-label")).toHaveText(["c.test:12333", "b.test:12333"]);
});
