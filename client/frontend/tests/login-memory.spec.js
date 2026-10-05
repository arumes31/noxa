import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {}; window.__logins = []; window.__forgotten = [];
        window.__settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", display_name: "Global name", recents: [] };
        window.__passwordStatus = { supported: true, available: true, account_saved: false, server_saved: false };
        window.runtime = { EventsOn(name, fn) { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") { if (window.__settingsWait) await window.__settingsWait; return structuredClone(window.__settings); }
            if (method === "GetLoginPasswordStatus") { if (window.__statusWait) await window.__statusWait; return { ...window.__passwordStatus }; }
            if (method === "ForgetLoginPasswords") { window.__forgotten.push(args); return window.__forgetError || ""; }
            if (method === "ConnectLogin") {
                window.__logins.push(args[0]);
                if (!window.__loginSuccess) return { error: "test connection rejected" };
                const r = args[0];
                if (r.remember_passwords) {
                    window.__passwordStatus.account_saved = !!(r.password || r.use_saved_account);
                    window.__passwordStatus.server_saved = !!(r.server_password || r.use_saved_server);
                }
                window.__tabs = [{ id: "one", addr: r.addr, nickname: r.nickname, active: true, connected: true }];
                for (const fn of window.__events.tab_reset || []) fn("one");
                return { tab_id: "one" };
            }
            if (method === "DisconnectTab") {
                window.__tabs = [];
                for (const fn of window.__events.tab_reset || []) fn("");
                return "";
            }
            if (method === "ListTabs") return window.__tabs || [];
            if (method === "SessionInfoForTab") return args[0] ? { connected: true, client_id: "self", unique_id: "self-uid", nickname: "owner", authorization_model: "roles-v1" } : {};
            if (["GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
    });
});

test("fresh startup has empty connection fields and does not inherit the global display name", async ({ page }) => {
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.state.settings);
    await expect(page.locator("#login-addr")).toHaveValue("");
    await expect(page.locator("#login-nick")).toHaveValue("");
    await expect(page.locator("#login-display-name")).toHaveValue("");
    await expect(page.locator("#login-remember-passwords")).not.toBeChecked();
});

test("startup restores the most recent successful profile and Enter uses saved passwords without exposing them", async ({ page }) => {
    await page.addInitScript(() => {
        window.__settings.recents = [{ addr: "voice.example:12333", nickname: "owner", display_name: "" }, { addr: "older.example", nickname: "Other" }];
        window.__passwordStatus.account_saved = window.__passwordStatus.server_saved = true;
    });
    await page.goto("/");
    await expect(page.locator("#login-addr")).toHaveValue("voice.example:12333");
    await expect(page.locator("#login-nick")).toHaveValue("owner");
    await expect(page.locator("#login-display-name")).toHaveValue("");
    await expect(page.locator("#login-remember-passwords")).toBeChecked();
    await expect(page.locator("#login-accountpw")).toHaveValue("");
    await expect(page.locator("#login-accountpw")).toHaveAttribute("placeholder", "Saved password");
    await page.locator("#login-nick").press("Enter");
    await expect.poll(() => page.evaluate(() => window.__logins.length)).toBe(1);
    expect(await page.evaluate(() => window.__logins[0])).toMatchObject({ addr: "voice.example:12333", nickname: "owner", display_name: "", password: "", server_password: "", use_saved_account: true, use_saved_server: true, remember_passwords: true });
    await expect(page.locator("#login-error")).toHaveText("test connection rejected");
    expect(await page.evaluate(() => window.__settings.recents[0].addr)).toBe("voice.example:12333");
});

test("forgetting passwords is immediate and retargeting cannot reuse the previous connection secrets", async ({ page }) => {
    await page.addInitScript(() => {
        window.__settings.recents = [{ addr: "one.example", nickname: "Alice", display_name: "Public Alice" }];
        window.__passwordStatus.account_saved = true;
    });
    await page.goto("/");
    await page.locator("#login-options > summary").click();
    await expect(page.locator("#login-remember-passwords")).toBeChecked();
    await page.locator("#login-remember-passwords").uncheck();
    await expect.poll(() => page.evaluate(() => window.__forgotten)).toEqual([["one.example", "Alice"]]);
    await expect(page.locator("#login-accountpw")).not.toHaveAttribute("placeholder", "Saved password");
    await page.evaluate(() => { window.__passwordStatus.account_saved = false; });
    await page.locator("#login-accountpw").fill("typed-secret");
    await page.locator("#login-serverpw").fill("server-secret");
    await page.locator("#login-addr").fill("two.example");
    await expect(page.locator("#login-accountpw")).toHaveValue("");
    await expect(page.locator("#login-serverpw")).toHaveValue("");
    await expect(page.locator("#login-display-name")).toHaveValue("");
    await page.locator("#login-connect").click();
    await expect.poll(() => page.evaluate(() => window.__logins.length)).toBe(1);
    expect(await page.evaluate(() => window.__logins[0])).toMatchObject({ addr: "two.example", password: "", server_password: "", use_saved_account: false, use_saved_server: false });
});

test("late startup settings do not overwrite an in-progress login", async ({ page }) => {
    await page.addInitScript(() => {
        window.__settings.recents = [{ addr: "old.example", nickname: "Old", display_name: "Old name" }];
        window.__settingsWait = new Promise(resolve => { window.__finishSettings = resolve; });
    });
    await page.goto("/");
    await page.locator("#login-addr").fill("new.example");
    await page.locator("#login-nick").fill("New");
    await page.evaluate(() => window.__finishSettings());
    await page.waitForFunction(() => !!window.__noxa.state.settings);
    await expect(page.locator("#login-addr")).toHaveValue("new.example");
    await expect(page.locator("#login-nick")).toHaveValue("New");
});

test("a newly remembered login can reopen the form and later reconnect without exposing saved secrets", async ({ page }) => {
    await page.addInitScript(() => { window.__loginSuccess = true; });
    await page.goto("/");
    await page.locator("#login-addr").fill("voice.example");
    await page.locator("#login-nick").fill("owner");
    await page.locator("#login-options > summary").click();
    await page.locator("#login-accountpw").fill("test-account");
    await page.locator("#login-serverpw").fill("test-server");
    await page.locator("#login-remember-passwords").check();
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-overlay")).toBeHidden();
    await page.evaluate(() => window.__noxa.showLogin());
    await expect(page.locator("#login-accountpw")).toHaveValue("");
    await expect(page.locator("#login-serverpw")).toHaveValue("");
    await expect(page.locator("#login-accountpw")).toHaveAttribute("placeholder", "Saved password");
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-overlay")).toBeHidden();
    expect(await page.evaluate(() => window.__logins[1])).toMatchObject({ password: "", server_password: "", use_saved_account: true, use_saved_server: true });
    await page.evaluate(async () => {
        await window.__noxa.disconnect();
        for (const fn of window.__events.tray_reconnect || []) fn();
    });
    await expect.poll(() => page.evaluate(() => window.__logins.length)).toBe(3);
    expect(await page.evaluate(() => window.__logins[2])).toMatchObject({ addr: "voice.example", nickname: "owner", password: "", server_password: "", use_saved_account: true, use_saved_server: true });
});

test("failed forgetting preserves saved-password intent and reports the failure", async ({ page }) => {
    await page.addInitScript(() => {
        window.__settings.recents = [{ addr: "voice.example", nickname: "owner" }];
        window.__passwordStatus.account_saved = true;
        window.__forgetError = "storage unavailable";
    });
    await page.goto("/");
    await page.locator("#login-options > summary").click();
    await expect(page.locator("#login-remember-passwords")).toBeChecked();
    await page.locator("#login-remember-passwords").click();
    await expect(page.locator("#login-error")).toHaveText("storage unavailable");
    await expect(page.locator("#login-remember-passwords")).toBeChecked();
    await expect(page.locator("#login-accountpw")).toHaveAttribute("placeholder", "Saved password");
});
