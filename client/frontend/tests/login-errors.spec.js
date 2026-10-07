import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {}; window.__attempts = [];
        window.__failure = "invalid server password";
        window.runtime = { EventsOn(name, fn) { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return { language: window.__language || "en", onboarding_done: true, alpha_dismissed: "test", recents: [] };
            if (method === "GetLoginPasswordStatus") return { supported: true, available: true, account_saved: false, server_saved: false };
            if (method === "ConnectLogin") { window.__attempts.push(args[0]); if (window.__hold) await window.__hold; return { error: window.__failure }; }
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["SessionInfoForTab", "IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.state.settings);
    await page.locator("#login-addr").fill("voice.example");
    await page.locator("#login-nick").fill("Daniel");
});

test("server password failure reveals its field, explains recovery and clears on edit", async ({ page }, testInfo) => {
    await page.locator("#login-connect").click();
    const field = page.locator("#login-serverpw");
    await expect(field).toBeVisible();
    await expect(field).toBeFocused();
    await expect(field).toHaveAttribute("aria-invalid", "true");
    await expect(field).toHaveAccessibleDescription(/server.{0,4} password/i);
    await expect(page.locator("#login-error")).toContainText("server password");
    await expect(page.locator("#login-serverpw-error")).toBeInViewport({ ratio: 1 });
    await page.screenshot({ path: testInfo.outputPath("server-password-error.png") });
    await field.fill("replacement");
    await expect(page.locator("#login-error")).toBeEmpty();
    await expect(field).not.toHaveAttribute("aria-invalid");
    await expect(page.locator(".login-field-error")).toHaveCount(0);
});

test("account failure preserves typed secrets and gives account-specific recovery", async ({ page }) => {
    await page.evaluate(() => { window.__failure = "invalid credentials"; });
    await page.locator("#login-options > summary").click();
    await page.locator("#login-accountpw").fill("keep-for-retry");
    await page.locator("#login-options > summary").click();
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-accountpw")).toBeFocused();
    await expect(page.locator("#login-accountpw")).toHaveValue("keep-for-retry");
    await expect(page.locator("#login-error")).toContainText("account login or password");
    await expect(page.locator("#login-serverpw")).not.toHaveAttribute("aria-invalid");
});

test("late failure cannot change a retargeted login", async ({ page }) => {
    await page.evaluate(() => { window.__hold = new Promise(resolve => { window.__finish = resolve; }); });
    await page.locator("#login-connect").click();
    await expect.poll(() => page.evaluate(() => window.__attempts.length)).toBe(1);
    await page.locator("#login-addr").fill("other.example");
    await page.evaluate(() => window.__finish());
    await expect(page.locator("#login-connect")).toBeEnabled();
    await expect(page.locator("#login-error")).toBeEmpty();
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect(page.locator("#login-serverpw")).toBeHidden();
});

test("late failure cannot reveal fields or steal focus after login is hidden", async ({ page }) => {
    await page.evaluate(() => { window.__hold = new Promise(resolve => { window.__finish = resolve; }); });
    await page.locator("#login-connect").click();
    await expect.poll(() => page.evaluate(() => window.__attempts.length)).toBe(1);
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await expect(page.locator("#center")).toBeFocused();
    await page.locator("#chat-text").focus();
    await page.evaluate(() => window.__finish());
    await expect(page.locator("#login-connect")).toBeEnabled();
    await expect(page.locator("#login-error")).toBeEmpty();
    await expect(page.locator("#chat-text")).toBeFocused();
    await expect(page.locator("#login-options")).not.toHaveAttribute("open");
});

test("unknown server text remains literal and does not blame a password", async ({ page }) => {
    await page.evaluate(() => { window.__failure = '<img src=x onerror="window.__injected=true"> unusual rejection'; });
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-error")).toHaveText('<img src=x onerror="window.__injected=true"> unusual rejection');
    await expect(page.locator("#login-error img")).toHaveCount(0);
    await expect(page.locator("#login-options")).not.toHaveAttribute("open");
    expect(await page.evaluate(() => window.__injected)).toBeUndefined();
});

test("empty address is caught before a native connection is attempted", async ({ page }) => {
    await page.locator("#login-addr").fill("");
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect(page.locator("#login-addr")).toHaveAttribute("aria-invalid", "true");
    expect(await page.evaluate(() => window.__attempts)).toEqual([]);
});

test("German guidance wraps at narrow width without exposing password values", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 740 });
    await page.addInitScript(() => { window.__language = "de"; });
    await page.reload();
    await page.waitForFunction(() => window.__noxa?.state.settings?.language === "de");
    await page.locator("#login-addr").fill("voice.example");
    await page.locator("#login-nick").fill("Daniel");
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-error")).toContainText("Serverpasswort");
    await expect(page.locator("#login-serverpw")).toBeFocused();
    await expect(page.locator("#login-serverpw-error")).toBeInViewport({ ratio: 1 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath("server-password-error-de.png") });
});
