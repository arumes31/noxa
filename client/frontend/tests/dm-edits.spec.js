import { expect, test } from "./fixtures.js";

test("own direct messages can be edited and deleted for everyone", async ({ page }) => {
    await page.addInitScript(() => {
        window.__calls = [];
        window.__dmRows = [
            { seq: 1, client_msg_id: "dm-bob", body: "Hi from Bob", from_unique_id: "bob", from_nickname: "Bob", sent_at: 1700000000, enc_verified: true },
            { seq: 2, client_msg_id: "dm-own", body: "My first draft", from_unique_id: "me", from_nickname: "Me", self: true, sent_at: 1700000060, enc_verified: true, protocol: "noxa-olm-v1" },
            // An own legacy (static-key) message stays read-only (review b46 P2-2).
            { seq: 3, client_msg_id: "dm-old", body: "An old legacy note", from_unique_id: "me", from_nickname: "Me", self: true, sent_at: 1700000030, enc_verified: true },
        ];
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
            if (method === "DMHistoryLoadForContext") return structuredClone(window.__dmRows);
            if (method === "EditDirectMessageForTab" || method === "DeleteDirectMessageForTab") { window.__calls.push([method, ...args]); return ""; }
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/"); await page.waitForFunction(() => !!window.__noxaChat);
    await page.evaluate(() => {
        const v = window.__noxa; v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "a", myChannelID: 0, myUniqueID: "me", channels: [], clients: [] });
        window.__noxaChat.openPM("bob", "Bob");
    });
    const log = page.locator("#chat-log");
    await expect(log).toContainText("My first draft");

    // Only the own message offers edit and delete.
    const own = log.locator(".msg", { hasText: "My first draft" });
    const theirs = log.locator(".msg", { hasText: "Hi from Bob" });
    await theirs.hover();
    await expect(theirs.getByRole("button", { name: "edit", exact: true })).toHaveCount(0);
    const legacy = log.locator(".msg", { hasText: "An old legacy note" });
    await legacy.hover();
    await expect(legacy.getByRole("button", { name: "edit", exact: true })).toHaveCount(0);
    await expect(legacy.getByRole("button", { name: "delete", exact: true })).toHaveCount(0);
    await own.hover();
    await own.getByRole("button", { name: "edit", exact: true }).click();
    const input = log.locator(".msg-edit-input");
    await input.fill("My final text");
    await input.press("Enter");
    await expect.poll(() => page.evaluate(() => window.__calls[0])).toEqual(["EditDirectMessageForTab", "a", "bob", "dm-own", "My final text"]);

    // The change applies when it comes back; a forged one from Bob does not.
    await page.evaluate(() => {
        // An Olm message's change arrives as an authenticated ratchet record.
        const change = (sender, purpose, text) => ({ protocol: "noxa-olm-v1", scope: "direct", peer: "bob", purpose, sender, reference: "dm-own", body: text === undefined ? {} : { text }, self: sender === "me", status: sender === "me" ? "accepted" : "" });
        window.__noxaChat.onRatchetMessage(change("me", "edit", "My final text"));
        window.__noxaChat.onRatchetMessage(change("bob", "edit", "Forged by Bob"));
    });
    await expect(log).toContainText("My final text");
    await expect(log).not.toContainText("Forged by Bob");

    await log.locator(".msg", { hasText: "My final text" }).hover();
    await log.locator(".msg", { hasText: "My final text" }).getByRole("button", { name: "delete", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: /delete/i }).last().click();
    await expect.poll(() => page.evaluate(() => window.__calls[1])).toEqual(["DeleteDirectMessageForTab", "a", "bob", "dm-own"]);
    await page.evaluate(() => window.__noxaChat.onRatchetMessage({ protocol: "noxa-olm-v1", scope: "direct", peer: "bob", purpose: "delete", sender: "me", reference: "dm-own", body: {}, self: true, status: "accepted" }));
    await expect(log).not.toContainText("My final text");
});
