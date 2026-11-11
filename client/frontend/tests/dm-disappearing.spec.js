import { expect, test } from "./fixtures.js";

// Disappearing direct messages (plan item 28). Go enforces expiry (fake here);
// the view shows timers, starts received ones on display, changes the
// setting through the dialog and follows "dm_expired".
test("disappearing messages: setting, countdown, start on display and expiry", async ({ page }) => {
    await page.addInitScript(() => {
        window.__calls = [];
        window.__events = {};
        const now = Math.floor(Date.now() / 1000);
        window.__dmRows = [
            { seq: 1, client_msg_id: "timer:1", body: "Bob turned it on", from_unique_id: "bob", from_nickname: "Bob", sent_at: now - 120, enc_verified: true, protocol: "noxa-olm-v1", timer: 3600 },
            { seq: 2, client_msg_id: "secret", body: "Self-destructing hello", from_unique_id: "bob", from_nickname: "Bob", sent_at: now - 60, enc_verified: true, protocol: "noxa-olm-v1", ttl: 3600 },
            { seq: 3, client_msg_id: "mine", body: "My vanishing reply", from_unique_id: "me", from_nickname: "Me", self: true, sent_at: now - 30, enc_verified: true, protocol: "noxa-olm-v1", ttl: 3600, expires_at: now + 3570 },
            { seq: 4, client_msg_id: "plain", body: "A normal message", from_unique_id: "bob", from_nickname: "Bob", sent_at: now - 20, enc_verified: true, protocol: "noxa-olm-v1" },
        ];
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
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
            if (method === "DirectDisappearingForTab") return 3600;
            if (method === "StartDirectDisappearingForTab") {
                window.__calls.push([method, ...args]);
                return [{ from_unique_id: "bob", client_msg_id: "secret", expires_at: Math.floor(Date.now() / 1000) + 3600 }];
            }
            if (method === "SetDirectDisappearingForTab") { window.__calls.push([method, ...args]); return ""; }
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/"); await page.waitForFunction(() => !!window.__noxaChat);
    await page.bringToFront();
    await page.evaluate(() => {
        const v = window.__noxa; v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "a", myChannelID: 0, myUniqueID: "me", channels: [], clients: [] });
        window.__noxaChat.openPM("bob", "Bob");
    });
    const log = page.locator("#chat-log");
    await expect(log).toContainText("Self-destructing hello");

    // The setting change is a system line; the header shows the running timer.
    await expect(log.locator(".disappear-notice")).toHaveText("⏱ Bob turned on disappearing messages (1 hour)");
    await expect(page.locator("#chat-head-title .disappear-indicator")).toHaveText("⏱ 1 hour");

    // Own copy counts from sending; a normal message has no countdown.
    const mine = log.locator(".msg", { hasText: "My vanishing reply" });
    await expect(mine.locator(".msg-disappear")).toHaveText(/⏱ 5\dm/);
    await expect(log.locator(".msg", { hasText: "A normal message" }).locator(".msg-disappear")).toHaveCount(0);

    // A received one starts when the conversation is on screen.
    await expect.poll(() => page.evaluate(() => window.__calls.find(c => c[0] === "StartDirectDisappearingForTab"))).toEqual(["StartDirectDisappearingForTab", "a", "bob"]);
    const theirs = log.locator(".msg", { hasText: "Self-destructing hello" });
    await expect(theirs.locator(".msg-disappear")).toHaveAttribute("title", /Disappears in (59m|1h)/);

    // Changing the timer goes through the dialog with a readable fallback line.
    await page.locator("#chat-head-actions .channel-actions > summary").click();
    const button = page.locator("#chat-disappear-btn");
    await expect(button).toBeVisible();
    await expect(button).toHaveAttribute("title", "Disappearing messages");
    await button.click();
    const dialog = page.getByRole("dialog", { name: "Disappearing messages" });
    await expect(dialog).toContainText("screenshot");
    await expect(dialog.getByRole("radio", { name: "1 hour" })).toBeChecked();
    await dialog.getByRole("radio", { name: "1 day" }).check();
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__calls.find(c => c[0] === "SetDirectDisappearingForTab"))).toEqual(
        ["SetDirectDisappearingForTab", "a", "bob", 86400, "I turned on disappearing messages (1 day). Update noXa to see messages disappear."]);
    await expect(page.locator("#chat-head-title .disappear-indicator")).toHaveText("⏱ 1 day");

    // Go expired the message: its text is gone from the view.
    await page.evaluate(() => window.__events.dm_expired.forEach(fn => fn({ peer: "bob", messages: [{ from_unique_id: "bob", client_msg_id: "secret" }] })));
    await expect(log).not.toContainText("Self-destructing hello");
    await expect(log).toContainText("This message disappeared");
    await expect(log).toContainText("My vanishing reply");

});
