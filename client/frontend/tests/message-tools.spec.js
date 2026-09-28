import { expect, test } from "./fixtures.js";

async function open(page) {
    await page.route("**/__message_tools__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><link rel="stylesheet" href="/src/style.css"><div id="chat-head"><button id="chat-search-btn">Search</button></div>' }));
    await page.goto("/__message_tools__");
    await page.evaluate(async () => {
        window.__noxa = { state: { activeTabID: "a", serverGeneration: 1, myUniqueID: "me", clients: [], channels: [{ ChannelID: 7, Name: "Lobby" }], settings: {} }, toast() {} };
        window.__requests = []; window.__refs = []; window.__jumps = [];
        window.go = { main: { App: {
            async SavedMessagesForTab(tab, req) { window.__requests.push([tab, req]); if (req.action === "save") window.__refs.push(req.reference); if (req.action === "move") window.__refs[0].collection = req.reference.collection; if (req.action === "remove") window.__refs = []; return { references: structuredClone(window.__refs) }; },
            async HistorySearchPageForTab(tab, filter) { window.__requests.push([tab, filter]); if (window.__hold) return new Promise(resolve => { window.__resolve = resolve; }); return { messages: [{ id: 42, body: "An older result", from_nickname: "Bob", sent_at: 1700000000 }], scanned: 200, undecryptable: 2, complete: true }; },
            async ConversationForTab() { return { conversations: [{ id: "g1", name: "Friends", unread_count: 2, latest_message_id: 5, members: [{ unique_id: "me" }] }] }; },
            async DiscussionForTab(tab, request) { return { threads: request.archived ? [] : [{ id: 9, channel_id: 7, title: "Followed thread", joined: true, subscribed: true, unread: true }], has_more: false }; },
        } } };
        const module = await import("/src/message-tools.js"); window.__tools = module;
        module.initMessageTools({ unread: () => [{ kind: "channel", channel_id: 7, message_id: 42, name: "Lobby", count: 1, mention: true }], jump: ref => window.__jumps.push(ref) });
    });
}
test("unified inbox shows unread channels mentions groups and followed threads", async ({ page }) => {
    await open(page); await page.getByRole("button", { name: "Inbox", exact: true }).click();
    await expect(page.locator(".message-tool-result")).toHaveCount(3);
    await page.getByLabel("Mentions only").check();
    await expect(page.locator(".message-tool-result:visible")).toHaveCount(1);
    await page.getByRole("button", { name: /Lobby/ }).click();
    expect(await page.evaluate(() => window.__jumps[0])).toMatchObject({ kind: "channel", channel_id: 7, message_id: 42 });
});
test("history search filters server pages and reports inaccessible messages", async ({ page }) => {
    await open(page); await page.getByRole("button", { name: "Search history", exact: true }).click();
    await page.getByLabel("Search text", { exact: true }).fill("older");
    await page.getByLabel("Sender", { exact: true }).fill("Bob");
    await page.getByLabel("Channel", { exact: true }).selectOption("7");
    await page.getByLabel("Has attachment").check();
    await page.getByRole("dialog").getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.locator(".message-tool-result")).toContainText("An older result");
    await expect(page.locator(".message-tool-status")).toContainText("2 could not be decrypted");
    expect(await page.evaluate(() => window.__requests.at(-1))).toMatchObject(["a", { query: "older", sender: "Bob", has_attachment: true, channel_id: 7 }]);
    await page.getByRole("button", { name: /An older result/ }).click();
    expect(await page.evaluate(() => window.__jumps[0].message_id)).toBe(42);
});
test("saved references can be organized and removed without storing message plaintext", async ({ page }) => {
    await open(page); await page.evaluate(() => window.__noxaMessageTools.save({ kind: "channel", channel_id: 7, message_id: 42 }));
    await page.getByRole("button", { name: "Saved messages", exact: true }).click();
    await expect(page.locator(".message-tool-result")).toHaveCount(1);
    await page.locator('.message-tool-result').getByLabel("Collection", { exact: true }).fill("Work");
    await page.getByRole("button", { name: "Move", exact: true }).click();
    expect(await page.evaluate(() => window.__refs[0])).toEqual({ kind: "channel", channel_id: 7, message_id: 42, collection: "Work" });
    await page.getByRole("button", { name: "Remove", exact: true }).click();
    await expect(page.locator(".message-tool-result")).toHaveCount(0);
});
test("late search results cannot enter another server session", async ({ page }) => {
    await open(page); await page.getByRole("button", { name: "Search history", exact: true }).click();
    await page.evaluate(() => { window.__hold = true; });
    await page.getByRole("dialog").getByRole("button", { name: "Search", exact: true }).click();
    await page.waitForFunction(() => !!window.__resolve);
    await page.evaluate(() => { window.__noxa.state.activeTabID = "b"; window.__noxa.state.serverGeneration++; window.__resolve({ messages: [{ id: 99, body: "Private late result" }], complete: true, scanned: 1 }); });
    await expect(page.getByText("Private late result")).toHaveCount(0);
});

test("full client saves a channel message and opens older authorized context", async ({ page }) => {
    await page.addInitScript(() => {
        window.__refs = []; window.__historyCalls = []; window.__dmLoads = [];
        window.__dmRows = [{ seq: 1, client_msg_id: "dm-message", body: "Private direct message", from_unique_id: "bob", from_nickname: "Bob", sent_at: 1700000000, enc_verified: true }];
        window.runtime = { EventsOn: () => () => {}, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            if (method === "DMHistoryContextForTab") return { tab_id: "a", identity_uid: "me", activation: "0", identity_revision: "0" };
            if (method === "DMHistoryLoadForContext") { window.__dmLoads.push(args); return structuredClone(window.__dmRows); }
            if (method === "SavedMessagesForTab") { if (args[1].action === "save") window.__refs.push(args[1].reference); return { references: structuredClone(window.__refs) }; }
            if (method === "ChatHistoryForTab") { window.__historyCalls.push(args); return { messages: args[2] ? [{ id: args[2] - 1, from_nickname: "Bob", body: "Older authorized context", sent_at: 1700000000, enc_verified: true }] : [] }; }
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/"); await page.waitForFunction(() => !!window.__noxaChat);
    await page.evaluate(() => {
        const v = window.__noxa; v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "a", myChannelID: 7, myUniqueID: "me", channels: [{ ChannelID: 7, Name: "Lobby" }], clients: [] });
        window.__noxaChat.onSubscriptions({ channel_ids: [7] });
        window.__noxaChat.addChat({ id: 42, channel_id: 7, from_unique_id: "bob", from: "Bob", text: "Save this private body", enc_verified: true });
    });
    await page.locator('[data-msg-id="42"]').hover();
    await page.getByRole("button", { name: "Save message", exact: true }).click();
    expect(await page.evaluate(() => window.__refs)).toEqual([{ kind: "channel", channel_id: 7, message_id: 42, collection: "" }]);
    await page.evaluate(() => { window.__refs[0].message_id = 20; });
    await page.getByRole("button", { name: "Saved messages", exact: true }).click();
    await page.getByRole("button", { name: "Lobby · Message 20" }).click();
    await expect(page.getByRole("dialog")).toContainText("Older authorized context");
    expect(await page.evaluate(() => window.__historyCalls.at(-1))).toEqual(["a", 7, 21, 20]);
    await page.setViewportSize({ width: 390, height: 780 });
    const bounds = await page.locator(".message-tools-dialog").boundingBox();
    expect(bounds.x).toBeGreaterThanOrEqual(0); expect(bounds.x + bounds.width).toBeLessThanOrEqual(390);
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await page.evaluate(() => window.__noxaChat.openPM("bob", "Bob"));
    await expect(page.locator("#chat-log")).toContainText("Private direct message");
    await page.getByRole("button", { name: "Save message", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__refs.length)).toBe(2);
    expect(await page.evaluate(() => window.__refs[1])).toMatchObject({ kind: "dm", peer_id: "bob", client_message_id: "dm-message", local_seq: 1, message_id: 0 });
    await page.getByRole("button", { name: "Saved messages", exact: true }).click();
    await page.getByRole("button", { name: "bob · Message 1" }).click();
    await expect(page.getByRole("dialog")).toContainText("Private direct message");
    expect(await page.evaluate(() => window.__dmLoads.at(-1)[0])).toEqual({ tab_id: "a", identity_uid: "me", activation: "0", identity_revision: "0" });
});

test("search stop waits for current page and does not issue another page", async ({ page }) => {
    await open(page); await page.getByRole("button", { name: "Search history", exact: true }).click();
    await page.evaluate(() => { window.__hold = true; });
    await page.getByRole("dialog").getByRole("button", { name: "Search", exact: true }).click();
    await page.waitForFunction(() => !!window.__resolve);
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await page.evaluate(() => window.__resolve({ messages: [], scanned: 200, complete: false, next_before_id: 500 }));
    await expect(page.getByRole("button", { name: "Continue searching", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.__requests.filter(([, request]) => Object.hasOwn(request, "query")).length)).toBe(1);
});
