import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__saved = { language: "en", compact_mode: true, onboarding_done: true, alpha_dismissed: "test", bookmarks: [], notification_matrix: {} };
        window.__whatsNewCalls = 0;
        window.runtime = { EventsOn: () => () => {}, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) {
            return async (...args) => {
                if (method === "GetSettings") return structuredClone(window.__saved);
                if (method === "SaveSettings") { window.__saved = structuredClone(args[0]); return ""; }
                if (method === "WhatsNew") { window.__whatsNewCalls++; return "Release notes should not appear"; }
                if (["ListTabs", "GetPermissions"].includes(method)) return [];
                if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
                if (["Connected", "IsGuest"].includes(method)) return false;
                if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
                return "";
            };
        } }) } };
    });
    await page.clock.install();
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.state.settings);
});

test("restored compact mode has a keyboard-accessible exit that persists", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 640, height: 480 });
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await expect(page.locator("body")).toHaveClass(/compact/);
    const exit = page.getByRole("button", { name: "Exit compact mode", exact: true });
    await expect(exit).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("compact-exit.png") });
    await exit.focus(); await page.keyboard.press("Enter");
    await expect(page.locator("body")).not.toHaveClass(/compact/);
    await expect(page.locator("#menubar")).toBeVisible();
    await expect(exit).toBeHidden();
    expect(await page.evaluate(() => window.__saved.compact_mode)).toBe(false);
});

test("startup does not request or show release notes", async ({ page }) => {
    await page.clock.runFor(2000);
    expect(await page.evaluate(() => window.__whatsNewCalls)).toBe(0);
    await expect(page.getByRole("heading", { name: "What's new", exact: true })).toHaveCount(0);
});
