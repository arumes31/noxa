import { expect, test } from "./fixtures.js";

async function open(page) {
    await page.route("**/__discussion_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Discussions</title><link rel="stylesheet" href="/src/style.css"><button id="chat-emoji">Emoji</button>' }));
    await page.goto("/__discussion_test__");
    await page.evaluate(async () => {
        window.__requests = []; window.__notifications = [];
        window.__noxa = { state: { activeTabID: "server-a", serverGeneration: 1, myUniqueID: "alice", settings: {}, channels: [], clients: [] }, toast() {} };
        window.__noxaNotify = { notify: (...args) => window.__notifications.push(args) };
        window.__discussionData = { forum: false, tags: [], threads: [], messages: new Map() };
        window.go = { main: { App: { DiscussionForTab: async (tabID, request) => {
            window.__requests.push({ tabID, ...request });
            if (window.__hold) return new Promise(resolve => { window.__resolve = resolve; });
            if (window.__failure) throw new Error(window.__failure);
            const db = window.__discussionData;
            let thread = db.threads.find(thread => thread.id === request.thread_id);
            if (request.action === "configure") { db.forum = request.forum; db.tags = request.tags; db.auto_archive_hours=request.auto_archive_hours; }
            if (request.action === "edit") { thread.title=request.title;thread.tags=request.tags; }
            if (request.action === "pin") thread.pinned=request.pinned;
            if (request.action === "resolve") thread.resolved=request.resolved;
            if (request.action === "create") {
                thread = { id: db.threads.length + 1, title: request.title, tags: request.tags, author: "alice", archived: false, joined: true, subscribed: true, message_count: 0 };
                db.threads.push(thread); db.messages.set(thread.id, []);
            }
            if (request.action === "send" || request.action === "create") {
                const messages = db.messages.get(thread.id);
                messages.push({ id: messages.length + 1, body: request.text, from_nickname: "Alice", sent_at: 1700000000 });
                thread.message_count++;
            }
            if (request.action === "archive" || request.action === "reopen") thread.archived = request.action === "archive";
            if (request.action === "join" || request.action === "leave") { thread.joined = request.action === "join"; thread.subscribed = thread.joined; }
            if (request.action === "subscribe") thread.subscribed = request.subscribed;
            if (request.action === "delete_message") {
                const message = db.messages.get(thread.id).find(message => message.id === request.message_id);
                if (!message.deleted) thread.message_count--;
                Object.assign(message, { deleted: true, body: "" });
            }
            if (request.action === "delete") {
                db.threads = db.threads.filter(value => value.id !== thread.id);
                db.messages.delete(thread.id); thread = null;
            }
            return { action: request.action, channel_id: request.channel_id, thread_id: thread?.id || request.thread_id || 0, forum: db.forum, tags: db.tags, auto_archive_hours:db.auto_archive_hours||0, can_manage: true, can_moderate: window.__canModerate !== false, has_more: false,
                threads: structuredClone(thread ? [thread] : db.threads.filter(thread => thread.archived === Boolean(request.archived) && (request.tags || []).every(tag => thread.tags.includes(tag)))),
                messages: structuredClone(thread ? db.messages.get(thread.id) : []) };
        } } } };
        window.__discussions = await import("/src/discussions.js");
        window.__discussions.initDiscussions(() => 7);
    });
    await page.getByRole("button", { name: "Threads and forum" }).click();
    await expect(page.getByRole("button", { name: "New post", exact: true })).toBeVisible();
}

async function create(page) {
    await page.getByRole("button", { name: "New post", exact: true }).click();
    await page.getByLabel("Title", { exact: true }).fill("Audio troubleshooting");
    await page.getByLabel("Message", { exact: true }).fill("Which microphone works best?");
    await page.getByRole("button", { name: "Create post", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Audio troubleshooting" })).toBeVisible();
}

test("moderators confirm deletion of another author's reply and whole thread", async ({ page }) => {
    await open(page); await create(page);
    await page.evaluate(() => { window.__discussionData.threads[0].author = "other"; });
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.locator(".discussion-message").getByRole("button", { name: "Delete message", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__requests.some(r => r.action === "delete_message"))).toBe(false);
    await page.locator(".discussion-message").getByRole("button", { name: "Delete message", exact: true }).click();
    await page.getByRole("dialog", { name: "Delete message?", exact: true }).getByRole("button", { name: "Delete message", exact: true }).click();
    await expect(page.locator(".discussion-message-body")).toHaveText("Message deleted");
    await expect(page.locator(".discussion-message button")).toHaveCount(0);
    expect(await page.evaluate(() => window.__requests.find(r => r.action === "delete_message"))).toMatchObject({ tabID: "server-a", channel_id: 7, thread_id: 1, message_id: 1 });
    await page.getByRole("button", { name: "Delete thread", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Delete thread?", exact: true })).toContainText("all its replies");
    await page.getByRole("dialog", { name: "Delete thread?", exact: true }).getByRole("button", { name: "Delete thread", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Channel threads", exact: true })).toBeVisible();
    await expect(page.locator(".discussion-post")).toHaveCount(0);
    expect(await page.evaluate(() => window.__requests.find(r => r.action === "delete"))).toMatchObject({ tabID: "server-a", channel_id: 7, thread_id: 1 });
});

test("members cannot delete discussion content and server changes cancel confirmed deletion", async ({ page }) => {
    await open(page); await create(page);
    await page.evaluate(() => { window.__canModerate = false; });
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(page.getByRole("button", { name: "Delete thread", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Delete message", exact: true })).toHaveCount(0);
    await page.evaluate(() => { window.__canModerate = true; });
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByRole("button", { name: "Delete thread", exact: true }).click();
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; });
    await page.getByRole("dialog", { name: "Delete thread?", exact: true }).getByRole("button", { name: "Delete thread", exact: true }).click();
    expect(await page.evaluate(() => window.__requests.some(r => r.action === "delete"))).toBe(false);
});

for (const wholeThread of [false, true]) {
    test(`external ${wholeThread ? "thread" : "reply"} deletion clears content despite an older history response`, async ({ page }) => {
        await open(page); await create(page);
        await page.evaluate(() => { window.__hold = true; });
        await page.getByRole("button", { name: "Refresh", exact: true }).click();
        await expect.poll(() => page.evaluate(() => typeof window.__resolve)).toBe("function");
        await page.evaluate(async wholeThread => {
            const stale = { action: "get", channel_id: 7, thread_id: 1, threads: structuredClone(window.__discussionData.threads), messages: structuredClone(window.__discussionData.messages.get(1)), can_moderate: true };
            await window.__discussions.discussionChanged({ channel_id: 7, thread_id: 1, ...(wholeThread ? { deleted: true } : { deleted_message_id: 1 }) });
            window.__hold = false;
            if (wholeThread) window.__discussionData.threads = [];
            window.__resolve(stale);
        }, wholeThread);
        await expect(page.locator(".discussion-content")).not.toContainText("Which microphone works best?");
        if (wholeThread) await expect(page.getByRole("heading", { name: "Channel threads", exact: true })).toBeVisible();
        else await expect(page.locator(".discussion-message-body")).toHaveText("Message deleted");
    });
}

test("forum configuration, tagged posts, replies, membership and archive lifecycle", async ({ page }) => {
    await open(page);
    await page.getByRole("button", { name: "Forum settings" }).click();
    await page.getByLabel("Use this channel as a forum").check();
    await page.getByLabel("Available tags").fill("Help\nSolved");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Forum", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "New post", exact: true }).click();
    await page.getByLabel("Title", { exact: true }).fill("Headset setup");
    await page.getByLabel("Message", { exact: true }).fill("Help me configure my headset.");
    await page.getByLabel("Help", { exact: true }).check();
    await page.getByRole("button", { name: "Create post", exact: true }).click();
    await page.getByLabel("Reply", { exact: true }).fill("I found the input selector.");
    await page.getByRole("button", { name: "Send reply" }).click();
    await expect(page.locator(".discussion-message")).toHaveCount(2);
    await expect(page.getByLabel("Reply", { exact: true })).toHaveValue("");
    await page.getByRole("button", { name: "Unfollow updates" }).click();
    await expect(page.getByRole("button", { name: "Follow updates", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Archive thread" }).click();
    await expect(page.getByLabel("Reply", { exact: true })).toHaveCount(0);
    await page.getByRole("button", { name: "Reopen thread" }).click();
    await page.getByRole("button", { name: "Leave thread" }).click();
    await expect(page.getByText("Join this thread to reply and follow updates.", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Join thread", exact: true }).click();
    await expect(page.getByLabel("Reply", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "All posts", exact: true }).click();
    await page.getByLabel("Tags", { exact: true }).selectOption("Solved");
    await expect(page.locator(".discussion-post")).toHaveCount(0);
    await page.getByLabel("Tags", { exact: true }).selectOption("Help");
    await expect(page.locator(".discussion-post")).toHaveCount(1);
    expect(await page.evaluate(() => window.__requests.every(request => request.tabID === "server-a" && request.channel_id === 7))).toBe(true);
});

test("background updates preserve a drafted reply and failed sends retain text", async ({ page }) => {
    await open(page); await create(page);
    await page.getByLabel("Reply", { exact: true }).fill("Still composing this reply");
    await page.evaluate(() => window.__discussions.discussionChanged({ channel_id: 7, thread_id: 1 }));
    await expect(page.getByLabel("Reply", { exact: true })).toHaveValue("Still composing this reply");
    await page.evaluate(() => { window.__failure = "Permission revoked"; });
    await page.getByRole("button", { name: "Send reply" }).click();
    await expect(page.getByRole("status")).toContainText("Permission revoked");
    await expect(page.getByLabel("Reply", { exact: true })).toHaveValue("Still composing this reply");
});

test("late results cannot render into another server session", async ({ page }) => {
    await open(page);
    await page.evaluate(() => { window.__hold = true; });
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.evaluate(() => {
        window.__noxa.state.activeTabID = "server-b"; window.__noxa.state.serverGeneration++;
        window.__resolve({ action: "list", channel_id: 7, forum: false, tags: [], threads: [{ id: 999, title: "Private old server result" }] });
    });
    await expect(page.getByText("Private old server result")).toHaveCount(0);
    expect(await page.evaluate(() => window.__requests.at(-1).tabID)).toBe("server-a");
});

test("thread subscriptions notify only after a current authorized state response", async ({ page }) => {
    await open(page); await create(page);
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await page.evaluate(async () => {
        window.__discussionData.threads[0].unread = true;
        await window.__discussions.discussionChanged({ channel_id: 7, thread_id: 1, new_message: true, author: "bob" });
    });
    expect(await page.evaluate(() => window.__notifications.length)).toBe(1);
    expect(await page.evaluate(() => window.__requests.at(-1).action)).toBe("state");
    await page.evaluate(async () => { window.__failure = "Channel revoked"; await window.__discussions.discussionChanged({ channel_id: 7, thread_id: 1, new_message: true, author: "bob" }); });
    expect(await page.evaluate(() => window.__notifications.length)).toBe(1);
});

test("forum form and posts fit a narrow client without horizontal overflow", async ({ page }) => {
    await page.setViewportSize({ width: 430, height: 760 }); await open(page); await create(page);
    expect(await page.locator(".discussion-dialog").evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.getByLabel("Reply", { exact: true }).fill("Keyboard-accessible reply");
    await page.getByRole("button", { name: "Send reply" }).focus();
    await page.keyboard.press("Enter");
    await expect(page.locator(".discussion-message")).toHaveCount(2);
});

test("configured forum channels show post boards and preserve the channel chat fallback", async ({ page }) => {
    await open(page); await create(page);
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await page.evaluate(() => {
        window.__discussionData.forum = true;
        const log = document.createElement("div"); log.id = "chat-log";
        const input = document.createElement("div"); input.id = "chat-input-row"; input.textContent = "Existing draft";
        document.body.append(log, input);
        const render = () => { log.replaceChildren(); if (!window.__discussions.renderForumChannel(log, 7, render)) log.append(document.createTextNode("Existing channel history")); };
        render();
    });
    await expect(page.getByRole("region", { name: "Forum", exact: true })).toBeVisible();
    await expect(page.locator("#chat-input-row")).toBeHidden();
    await expect(page.locator("#chat-log .discussion-post")).toContainText("Audio troubleshooting");
    await page.getByRole("button", { name: "Channel chat", exact: true }).click();
    await expect(page.locator("#chat-input-row")).toBeVisible();
    await expect(page.locator("#chat-log")).toContainText("Existing channel history");
    await page.getByRole("button", { name: "Forum", exact: true }).click();
    await page.locator("#chat-log .discussion-post").click();
    await expect(page.getByRole("heading", { name: "Audio troubleshooting" })).toBeVisible();
});

test("thread authors can edit tags and resolution while moderators pin and configure archive", async ({ page }) => {
    await open(page);
    await page.getByRole("button", { name: "Forum settings" }).click();
    await page.getByLabel("Available tags").fill("Help\nSolved");
    await page.getByLabel("Archive inactive threads").selectOption("72");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await create(page);
    await page.getByRole("button", { name: "Edit title and tags" }).click();
    await page.getByLabel("Title", { exact: true }).fill("Headset fixed");
    await page.getByLabel("Solved", { exact: true }).check();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Headset fixed" })).toBeVisible();
    await page.getByRole("button", { name: "Mark resolved", exact: true }).click();
    await expect(page.getByRole("button", { name: "Mark unresolved", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Pin post", exact: true }).click();
    await expect(page.getByRole("button", { name: "Unpin post", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "All posts", exact: true }).click();
    await expect(page.locator(".discussion-post")).toContainText("Pinned");
    await expect(page.locator(".discussion-post")).toContainText("Resolved");
    await expect(page.locator(".discussion-post")).toContainText("Solved");
    await page.getByRole("button", { name: "Forum settings" }).click();
    await expect(page.getByLabel("Archive inactive threads")).toHaveValue("72");
});

test("opening a saved thread message focuses its exact message", async ({ page }) => {
    await open(page); await create(page);
    await page.getByLabel("Reply", { exact: true }).fill("Jump target");
    await page.getByRole("button", { name: "Send reply" }).click();
    await page.evaluate(() => window.__discussions.openDiscussions(7, null, 1, 2));
    await expect(page.locator('[data-discussion-message="2"]')).toBeFocused();
});
