import { test, expect } from "./fixtures.js";

// (501) Windows Action Center notifications: the settings, the request sent
// to Go, and quick reply routed through the conversation's own composer.
test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.__calls = [];
        window.__saved = { language: "en", activation_mode: "ptt", hotkey_ptt: "F5", volume: 100, user_volumes: {}, bookmarks: [], onboarding_done: true, alpha_dismissed: "0.5.0-dev+gabc123", chat_max_lines: 200, window_opacity: 100 };
        window.runtime = {
            EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {},
            WindowIsFullscreen: async () => false,
        };
        window.go = { main: { App: new Proxy({}, { get(_target, method) {
            return async (...args) => {
                window.__calls.push([method, args]);
                if (method === "GetSettings") return structuredClone(window.__saved);
                if (method === "SaveSettings") { window.__saved = structuredClone(args[0]); return ""; }
                if (method === "NativeToastsAvailable") return true;
                if (method === "ListTabs") return [{ id: "one", addr: "", connected: true }];
                if (method === "SessionInfoForTab") return { connected: true, client_id: "self" };
                if (["GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
                if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
                if (method === "DMHistoryContextForTab") return { tab_id: "one", identity_uid: "me", activation: "0", identity_revision: "0" };
                if (method === "Connected" || method === "IsGuest") return false;
                if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
                if (method === "ServerInfoForTab") return { name: "Test server", version: "0.5.0+gdef456", chat_max_bytes: 100 };
                return "";
            };
        } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings && !!window.__noxaNotify);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "one", myClientID: "self", myUniqueID: "me", myChannelID: 1,
            channels: [{ ChannelID: 1, Name: "Lobby" }],
            clients: [{ client_id: "self", unique_id: "me", nickname: "Me", channel_id: 1 }, { client_id: "alice", unique_id: "alice-uid", nickname: "Alice", channel_id: 1 }] });
        v.state.settings = structuredClone(window.__saved);
        v.renderTree();
    });
});

const calls = (page, method) => page.evaluate(name => window.__calls.filter(call => call[0] === name).map(call => call[1]), method);

test("the notification settings offer Windows notifications and hidden previews", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("notifications"));
    const windows = page.getByRole("checkbox", { name: "Use Windows notifications" });
    const previews = page.getByRole("checkbox", { name: "Show sender and message text in Windows notifications" });
    await expect(windows).toBeChecked();
    await expect(previews).toBeChecked();
    await windows.uncheck();
    await previews.uncheck();
    await expect.poll(async () => (await calls(page, "ClearNativeToasts")).map(args => args[0])).toContain("");
    await page.locator("#set-apply").click();
    await expect.poll(() => page.evaluate(() => [window.__saved.windows_notifications, window.__saved.native_notification_previews])).toEqual([false, false]);
});

test("an unfocused DM becomes a toast and a quick reply goes through the DM composer", async ({ page }) => {
    await page.evaluate(() => {
        document.hasFocus = () => false;
        window.__noxaNotify.notify("dm", "Alice: dinner at 8?", { uid: "alice-uid", className: "messages", announce: false,
            reference: { kind: "dm", peer_id: "alice-uid", client_message_id: "m1", local_seq: 0 } });
    });
    await expect.poll(async () => (await calls(page, "ShowNativeToast")).length).toBe(1);
    const [[request]] = await calls(page, "ShowNativeToast");
    expect(request).toMatchObject({ conversation: "one|dm:alice-uid", title: "Alice", body: "dinner at 8?", replyable: true, reply_label: "Reply", read_label: "Mark as read" });
    expect(request.token).toMatch(/^[0-9a-f]{32}$/);
    expect(await calls(page, "Notify")).toEqual([]);

    // A forged token is ignored; the issued one opens the DM and sends.
    await page.evaluate(token => {
        for (const fn of window.__events.native_toast_activation) {
            fn({ token: "f".repeat(32), action: "reply", conversation: "one|dm:alice-uid", reply: "forged" });
            fn({ token, action: "reply", conversation: "one|dm:alice-uid", reply: "Yes, see you!" });
        }
    }, request.token);
    // Sending may wait for the undo-send window.
    await expect.poll(() => page.evaluate(() => window.__calls.some(([method, args]) =>
        method === "SendChatOperationForTab" && args[1] === "direct" && args[2] === "alice-uid" && args[3] === "Yes, see you!")), { timeout: 15_000 }).toBe(true);
    expect(await page.evaluate(() => window.__calls.some(([, args]) => JSON.stringify(args).includes("forged")))).toBe(false);
    await expect(page.locator("#chat-text")).toHaveValue("");
});

test("a quick reply never replaces an unsent draft", async ({ page }) => {
    await page.evaluate(() => {
        document.hasFocus = () => false;
        window.__noxaNotify.notify("dm", "Alice: ping", { uid: "alice-uid", reference: { kind: "dm", peer_id: "alice-uid" } });
    });
    await expect.poll(async () => (await calls(page, "ShowNativeToast")).length).toBe(1);
    const [[request]] = await calls(page, "ShowNativeToast");
    await page.evaluate(() => window.__noxaChat.openPM("alice-uid", "Alice"));
    await page.locator("#chat-text").fill("half-written thought");
    await page.evaluate(token => {
        for (const fn of window.__events.native_toast_activation) fn({ token, action: "reply", conversation: "one|dm:alice-uid", reply: "pong" });
    }, request.token);
    await expect(page.locator("#chat-text")).toHaveValue("half-written thought pong");
    expect(await page.evaluate(() => window.__calls.some(([, args]) => JSON.stringify(args).includes("pong")))).toBe(false);
});

test("hidden previews keep sender and text out of the toast", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.native_notification_previews = false;
        document.hasFocus = () => false;
        window.__noxaNotify.notify("dm", "Alice: the code is 1234", { uid: "alice-uid", reference: { kind: "dm", peer_id: "alice-uid" } });
    });
    await expect.poll(async () => (await calls(page, "ShowNativeToast")).length).toBe(1);
    const [[request]] = await calls(page, "ShowNativeToast");
    expect(request).toMatchObject({ title: "noXa", body: "New message", replyable: true });
    expect(JSON.stringify(request)).not.toContain("1234");
});
