import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {}; window.__renames = []; window.__namedLogins = [];
        window.__settings = { language: "en", activation_mode: "ptt", volume: 100, bookmarks: [], onboarding_done: true, alpha_dismissed: "0.5.0-dev+gabc123", chat_max_lines: 200, window_opacity: 100 };
        window.__tabs = [];
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(window.__settings);
            if (method === "SaveSettings") { if (window.__saveError) return window.__saveError; window.__settings = structuredClone(args[0]); return ""; }
            if (method === "ConnectNamedBookmarkTabWithID") {
                window.__namedLogins.push(args);
                window.__tabs = [{ id: "one", addr: args[1], nickname: args[2], display_name: args[3], active: true, connected: true }];
                window.__session = { client_id: "self", nickname: args[3], connected: true, authorization_model: "roles-v1", is_guest: false };
                for (const fn of window.__events.tab_reset || []) fn("one");
                return { tab_id: "one", error: "" };
            }
            if (method === "SetDisplayNameForTab") {
                window.__renames.push(args);
                if (window.__renameWait) await window.__renameWait;
                return window.__renameError || "";
            }
            if (method === "SessionInfoForTab") return window.__session || {};
            if (method === "ListTabs") return structuredClone(window.__tabs);
            if (["GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (method === "DMHistoryContextForTab") return { tab_id: args[0], identity_uid: "device-uid", activation: "0", identity_revision: "0" };
            if (method === "Connected" || method === "IsGuest") return false;
            if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.state?.settings);
});

async function connected(page) {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        const connection = { addr: "server.example:12333", nick: "owner", pw: "", spw: "", displayName: "", bookmark: "" };
        Object.assign(v.state, { activeTabID: "one", serverGeneration: 1, myClientID: "self", myUniqueID: "device-uid", myNickname: "owner", lastConnect: connection });
        v.state.tabConnects.set("one", connection);
    });
}

async function openRename(page) {
    await page.locator("#menubar > .menu-item").filter({ hasText: /^Self/ }).click();
    await page.getByRole("menuitem", { name: "Change display name…", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Change display name", exact: true })).toBeVisible();
}

test("login keeps owner credentials separate from the public display name", async ({ page }, testInfo) => {
    await page.getByLabel("Account login / guest name", { exact: true }).fill("owner");
    const optional = page.locator("#login-options > summary");
    await expect(page.locator("#login-display-name")).toBeHidden();
    await page.locator("#login-nick").press("Tab");
    await expect(optional).toBeFocused();
    await optional.press("Enter");
    await page.getByLabel("Display name (optional)", { exact: true }).fill("Daniel");
    await page.getByLabel("Account password (optional)", { exact: true }).fill("test-account-password");
    await optional.click();
    await expect(page.locator("#login-accountpw")).toBeHidden();
    await optional.press("Tab");
    await expect(page.locator("#login-connect")).toBeFocused();
    await page.locator(".login-card").screenshot({ path: testInfo.outputPath("display-name-login.png") });
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-overlay")).toBeHidden();
    expect(await page.evaluate(() => window.__namedLogins[0])).toEqual(["", "127.0.0.1:12333", "owner", "Daniel", "test-account-password", ""]);
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myNickname)).toBe("Daniel");
    await expect.poll(() => page.evaluate(() => window.__settings.display_name)).toBe("Daniel");
    expect(await page.evaluate(() => window.__noxa.state.lastConnect.nick)).toBe("owner");
    expect(await page.evaluate(() => JSON.stringify(window.__settings))).not.toContain("test-account-password");
});

test("live edits wait for server success and preserve the login", async ({ page }) => {
    await connected(page);
    await openRename(page);
    await page.getByLabel("Display name", { exact: true }).fill("Daniel");
    await page.evaluate(() => { window.__renameWait = new Promise(resolve => { window.__finishRename = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.locator("#display-name-value")).toBeDisabled();
    expect(await page.evaluate(() => window.__noxa.state.myNickname)).toBe("owner");
    await page.evaluate(() => window.__finishRename());
    await expect(page.locator("#display-name-value")).toHaveCount(0);
    expect(await page.evaluate(() => window.__renames)).toEqual([["one", "Daniel"]]);
    expect(await page.evaluate(() => window.__noxa.state.lastConnect)).toMatchObject({ nick: "owner", displayName: "Daniel" });
    await expect(page.locator("#login-nick")).toHaveValue("");
    await expect.poll(() => page.evaluate(() => window.__settings.display_name)).toBe("Daniel");
});

test("rejected changes keep the editor and old name", async ({ page }) => {
    await connected(page);
    await openRename(page);
    await page.getByLabel("Display name", { exact: true }).fill("Daniel");
    await page.evaluate(() => { window.__renameError = "display name is already in use"; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("dialog").getByRole("alert")).toHaveText("display name is already in use");
    await expect(page.locator("#display-name-value")).toBeEnabled();
    expect(await page.evaluate(() => window.__noxa.state.myNickname)).toBe("owner");
    expect(await page.evaluate(() => window.__settings.display_name)).toBeUndefined();
});

test("invalid names never reach the server", async ({ page }) => {
    await connected(page);
    await openRename(page);
    for (const value of ["   ", "ä".repeat(65)]) {
        await page.getByLabel("Display name", { exact: true }).fill(value);
        await page.getByRole("button", { name: "Save", exact: true }).click();
        await expect(page.getByRole("dialog").getByRole("alert")).toContainText("1–64");
    }
    expect(await page.evaluate(() => window.__renames)).toEqual([]);
});

test("tab switches discard a delayed rename result", async ({ page }) => {
    await connected(page);
    await openRename(page);
    await page.getByLabel("Display name", { exact: true }).fill("Daniel");
    await page.evaluate(() => { window.__renameWait = new Promise(resolve => { window.__finishRename = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__renames.length)).toBe(1);
    await page.evaluate(async () => {
        (await import("/src/modal.js")).closeServerDialogs();
        Object.assign(window.__noxa.state, { activeTabID: "two", serverGeneration: 2, myNickname: "Other server name" });
        window.__finishRename();
    });
    await expect(page.locator("#display-name-value")).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.myNickname)).toBe("Other server name");
    expect(await page.evaluate(() => window.__settings.display_name)).toBeUndefined();
});

test("offline edits save a preference without claiming a live rename", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await openRename(page);
    await page.getByLabel("Display name", { exact: true }).fill("Daniel");
    await page.locator("#display-name-value").press("Enter");
    await expect(page.locator("#display-name-value")).toHaveCount(0);
    expect(await page.evaluate(() => window.__renames)).toEqual([]);
    expect(await page.evaluate(() => window.__settings.display_name)).toBe("Daniel");
    await expect(page.locator("#login-display-name")).toHaveValue("Daniel");
});

test("bookmark display names preserve the stored account login", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.settings.display_name = "Default name";
        window.__noxa.state.settings.bookmarks = [{ name: "My server", addr: "server.example:12333", nickname: "owner", nickname_override: "Daniel" }];
    });
    await page.getByRole("menuitem", { name: "Bookmarks", exact: true }).click();
    await page.getByRole("menuitem", { name: /My server/ }).click();
    await expect(page.locator("#login-nick")).toHaveValue("owner");
    await expect(page.locator("#login-display-name")).toHaveValue("Daniel");
    await page.locator("#login-connect").click();
    await expect.poll(() => page.evaluate(() => window.__namedLogins.length)).toBe(1);
    expect(await page.evaluate(() => window.__namedLogins[0].slice(0, 4))).toEqual(["My server", "server.example:12333", "owner", "Daniel"]);
});

test("a failed preference save reports the failure after a successful public rename", async ({ page }) => {
    await connected(page);
    await openRename(page);
    await page.getByLabel("Display name", { exact: true }).fill("Daniel");
    await page.evaluate(() => { window.__saveError = "disk unavailable"; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.locator("#display-name-value")).toHaveCount(0);
    await expect(page.locator("#toasts")).toContainText("disk unavailable");
    expect(await page.evaluate(() => window.__noxa.state.myNickname)).toBe("Daniel");
    expect(await page.evaluate(() => window.__settings.display_name)).toBeUndefined();
});

test("saved display names are restored on startup", async ({ page }) => {
    await page.addInitScript(() => { window.__settings.display_name = "Daniel"; });
    await page.reload();
    await expect(page.locator("#login-display-name")).toHaveValue("Daniel");
    await expect(page.locator("#login-nick")).toHaveValue("");
});

test("short windows can scroll the whole login form", async ({ page }) => {
    await page.setViewportSize({ width: 560, height: 480 });
    expect((await page.locator("#login-addr").boundingBox()).y).toBeGreaterThanOrEqual(0);
    await page.locator("#login-options > summary").click();
    await page.locator("#login-serverpw").fill("server-password");
    await page.locator("#login-connect").scrollIntoViewIfNeeded();
    const button = await page.locator("#login-connect").boundingBox();
    expect(button.y).toBeGreaterThanOrEqual(0);
    expect(Math.round(button.y + button.height)).toBeLessThanOrEqual(480);
    expect(await page.locator("#login-overlay").evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true);
});
