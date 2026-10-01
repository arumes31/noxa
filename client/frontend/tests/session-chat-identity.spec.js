import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {}; window.__dmAppends = []; window.__delivered = [];
        window.__history = []; window.__dmHistory = [];
        window.runtime = { EventsOn: (name, callback) => { (window.__events[name] ||= []).push(callback); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return { language: "en", activation_mode: "ptt", volume: 100, bookmarks: [], onboarding_done: true, alpha_dismissed: "0.5.0-dev+gabc123", chat_max_lines: 200, window_opacity: 100 };
            if (method === "DMHistoryContextForTab") return { tab_id: args[0], identity_uid: "device-storage-key", activation: "0", identity_revision: "0" };
            if (method === "DMHistoryAppendForContext") { window.__dmAppends.push(args); return ""; }
            if (method === "DMHistoryLoadForContext") return window.__dmHistory;
            if (method === "SendChatDeliveredForTab") { window.__delivered.push(args); return ""; }
            if (method === "ChatHistoryForTab") return { messages: window.__history, more: false };
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext", "ChatPinsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (method === "Connected" || method === "IsGuest") return false;
            if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxaChat);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "account-server", serverGeneration: 1, myClientID: "self-session", myUniqueID: "device-storage-key", myNickname: "Me", myChannelID: 7,
            channels: [{ ChannelID: 7, Name: "Lobby" }], clients: [
                { client_id: "self-session", unique_id: "account-uid", nickname: "Me", channel_id: 7 },
                { client_id: "peer-session", unique_id: "peer-uid", nickname: "Peer", channel_id: 7 },
            ] });
        v.renderTree();
    });
});

test("account-authenticated outgoing DM stays in the recipient thread and retains device history ownership", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxaChat.openPM("peer-uid", "Peer");
        await new Promise(resolve => setTimeout(resolve, 0));
        window.__outgoingResult = window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: "account-uid", to_unique_id: "peer-uid", from: "Me", client_msg_id: "sent-one", text: "My outgoing direct message" });
    });
    await expect(page.locator('#chat-log .msg.own')).toContainText("My outgoing direct message");
    await expect(page.locator('#pm-tabs')).not.toContainText("Me");
    await expect.poll(() => page.evaluate(() => window.__dmAppends.length)).toBe(1);
    const saved = await page.evaluate(() => ({ append: window.__dmAppends[0], delivered: window.__delivered, result: window.__outgoingResult, storage: window.__noxa.state.myUniqueID }));
    expect(saved.append[0].identity_uid).toBe("device-storage-key");
    expect(saved.append[1]).toBe("peer-uid");
    expect(saved.append[3]).toMatchObject({ self: true, from_unique_id: "account-uid" });
    expect(saved.storage).toBe("device-storage-key");
    expect(saved.result.incomingDM).toBe(false);
    expect(saved.delivered).toEqual([]);
});

test("account-authenticated channel messages retain own-message controls live and in history", async ({ page }) => {
    await page.evaluate(() => {
        window.__history = [{ id: 101, channel_id: 7, from_unique_id: "account-uid", from: "Me", text: "My stored channel message" }];
        window.__noxaChat.resetView();
        window.__noxaChat.onMyChannelChanged();
    });
    await expect(page.locator('#chat-log .msg.own')).toContainText("My stored channel message");
    await page.evaluate(() => window.__noxaChat.addChat({ id: 102, channel_id: 7, from_unique_id: "account-uid", from: "Me", text: "My live channel message" }));
    await expect(page.locator('#chat-log .msg.own')).toHaveCount(2);
    await page.locator('#chat-log .msg.own').last().hover();
    await expect(page.locator('#chat-log .msg.own').last().getByRole('button', { name: /^edit$/i })).toBeVisible();
});

test("account-authenticated reaction updates highlight my reactions", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxaChat.resetView();
        window.__noxaChat.addChat({ id: 103, channel_id: 7, from_unique_id: "peer-uid", from: "Peer", text: "React here" });
        window.__noxaChat.onChatReaction({ message_id: 103, by: "account-uid", added: true, emoji: "👍", reactions: { "👍": 1 } });
    });
    await expect(page.locator('#chat-log .react-chip')).toHaveClass(/own/);
    await page.evaluate(() => window.__noxaChat.onChatReaction({ message_id: 103, by: "account-uid", added: false, emoji: "👍", reactions: { "👍": 1 } }));
    await expect(page.locator('#chat-log .react-chip')).not.toHaveClass(/own/);
});

test("account-authenticated quick switcher excludes my own member entry", async ({ page }) => {
    await page.keyboard.press('Control+k');
    await expect(page.locator('.qs-overlay')).toBeVisible();
    await expect(page.locator('.qs-list')).toContainText('@ Peer');
    await expect(page.locator('.qs-list')).not.toContainText('@ Me');
});

test("restored direct-message ownership stays independent of the current account session", async ({ page }) => {
    await page.evaluate(() => {
        window.__dmHistory = [{ seq: 1, self: true, from_unique_id: "previous-account-uid", from_nickname: "Me", body: "My saved direct message", client_msg_id: "saved-one", sent_at: 1, enc_verified: true }];
        window.__noxaChat.openPM("peer-uid", "Peer");
    });
    await expect(page.locator('#chat-log .msg.own')).toContainText("My saved direct message");
    await page.evaluate(() => { window.__incomingResult = window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: "peer-uid", to_unique_id: "account-uid", from: "Peer", client_msg_id: "received-one", text: "Peer reply" }); });
    await expect(page.locator('#chat-log .msg:not(.own)')).toContainText("Peer reply");
    await expect.poll(() => page.evaluate(() => window.__dmAppends.length)).toBe(1);
    const saved = await page.evaluate(() => ({ append: window.__dmAppends[0], delivered: window.__delivered, result: window.__incomingResult }));
    expect(saved.append[0].identity_uid).toBe("device-storage-key");
    expect(saved.append[1]).toBe("peer-uid");
    expect(saved.append[3].self).toBe(false);
    expect(saved.result.incomingDM).toBe(true);
    expect(saved.delivered).toEqual([["account-server", "peer-uid", "received-one"]]);
});

test("typing from another connection of my account is not shown as another user", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxaChat.onMyChannelChanged();
        window.__noxaChat.onTyping({ client_id: "another-device", unique_id: "account-uid", nickname: "Me", channel_id: 7 });
        window.__noxaChat.onTyping({ client_id: "peer-session", unique_id: "peer-uid", nickname: "Peer", channel_id: 7 });
    });
    await expect(page.locator('#chat-typing')).toContainText('Peer');
    await expect(page.locator('#chat-typing')).not.toContainText('Me');
});
