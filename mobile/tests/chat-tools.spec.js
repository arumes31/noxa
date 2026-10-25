// chat-tools.spec.js — search, inbox, saved messages, threads and forum
// posts, and polls on the phone. Abe's phone works in a text channel where
// @everyone may manage messages and the channel (so thread moderation and
// forum settings show); Bea and Cid write from desktop sessions.
import { devices, expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { fromBase64, sealDM } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers, publishKeyRecord, registerDesktop } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
const run = Date.now().toString(36);
let page, owner, abe, bea, cid;
let ownerChat, abeChat, beaChat, cidChat;
let channelID, channelName, needle;

test.describe.configure({ mode: "serial" });

async function session(account, version) {
    const desktop = await desktopSession(server.controlAddr, account, { version });
    return { desktop, chat: new ChatSession(desktop.conn, desktop.identity, desktop.auth) };
}

/** A root text channel whose @everyone may moderate threads and run the forum. */
async function createToolsChannel(name) {
    const state = await owner.conn.request(Msg.RoleChannelQuery, { kind: "channel_create", channel_id: 0 }, Msg.RoleChannelState);
    const allow = capability => ({ role_id: state.everyone_id, capability, effect: "allow" });
    const result = await owner.conn.request(Msg.RoleChannelChange, {
        kind: "channel_create", expected_revision: state.revision, parent_id: 0, channel_type: 2, text_only: true,
        settings: { name, opus_bitrate: 64000, opus_fec: true },
        access: { synced: false, overrides: ["view_channel", "read_history", "send_messages", "manage_messages", "manage_channels"].map(allow) },
    }, Msg.RoleChannelResult);
    return result.channel_id;
}

/** Subscribes a desktop chat to the channel and waits for its key. */
async function joinChat(chat, scope) {
    const keyed = new Promise(resolve => {
        const check = () => { if (chat.current.has(scope)) { chat.removeEventListener("keys", check); resolve(); } };
        chat.addEventListener("keys", check);
        check();
    });
    await chat.subscribe([scope]);
    await keyed;
}

/** Sends as a desktop, waiting out the account's chat rate limit (5 per 3 s). */
async function send(chat, text, scope = channelID) {
    for (let attempt = 0; ; attempt++) {
        try {
            return await chat.send(scope, text);
        } catch (error) {
            if (attempt >= 20 || !/rate limit|slow down/.test(error.message)) throw error;
            await new Promise(resolve => setTimeout(resolve, 700));
        }
    }
}

function discussion(desktop, request) {
    return desktop.conn.request(Msg.DiscussionRequest, request, Msg.DiscussionResult, { accept: r => r.action === request.action && r.channel_id === request.channel_id });
}

function revoke(desktop, deviceID) {
    return desktop.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: deviceID }, Msg.DeviceResult, { accept: r => r.action === "revoke", timeout: 5000 }).catch(() => {});
}

test.beforeAll(async ({ browser }) => {
    const info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    ({ desktop: owner, chat: ownerChat } = await session(server.accounts.owner, info.version));
    await openServerToMembers(owner);
    channelName = `tools-${run}`;
    channelID = await createToolsChannel(channelName);
    ({ desktop: abe, chat: abeChat } = await session(server.accounts.abe, info.version));
    ({ desktop: bea, chat: beaChat } = await session(server.accounts.bea, info.version));
    ({ desktop: cid, chat: cidChat } = await session(server.accounts.cid, info.version));
    for (const desktop of [abe, bea]) {
        await registerDesktop(desktop);
        await publishKeyRecord(desktop);
    }
    await Promise.all([ownerChat, abeChat, beaChat, cidChat].map(chat => joinChat(chat, channelID)));

    // An old message, then more than one history page on top of it.
    needle = `needle${run}`;
    await send(beaChat, `the ${needle} is here`);
    const senders = [ownerChat, abeChat, beaChat, cidChat];
    await Promise.all(senders.map(async (chat, index) => {
        for (let i = 0; i < 14; i++) await send(chat, `filler ${index}-${i}`);
    }));
    await send(beaChat, `kiwi from bea ${run}`);
    await send(cidChat, `kiwi from cid ${run}`);

    const context = await browser.newContext({ ...devices["Pixel 7"], permissions: ["microphone"] });
    page = await context.newPage();
    page.on("pageerror", error => console.log("pageerror:", error.message));
    await linkPhone(page, abe, server.webURL, expect);
});

test.afterAll(async () => {
    // Sign the phone out again so the accounts keep free device slots.
    const list = await abe?.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" }).catch(() => null);
    for (const device of list?.devices || []) if (!device.current) await revoke(abe, device.device_id);
    for (const chat of [ownerChat, abeChat, beaChat, cidChat]) chat?.close();
    await page?.context().close();
});

async function openChannel() {
    await page.goto(`${server.webURL}/app/#/chat/${channelID}`);
    await expect(page.getByRole("log", { name: `# ${channelName}` })).toBeVisible({ timeout: 15000 });
}

test("in-chat search finds an older message after loading all history", async () => {
    await openChannel();
    const log = page.getByRole("log", { name: `# ${channelName}` });
    await expect(log).toContainText("kiwi from cid");
    await page.getByRole("button", { name: "Search in this chat" }).click();
    const input = page.getByRole("searchbox", { name: "Search in this chat" });
    await input.fill("kiwi");
    // The loaded log is filtered to the matches.
    await expect(page.getByRole("status").filter({ hasText: "2 matches in loaded messages" })).toBeVisible();
    await expect(log.locator(".msg:not(.search-miss)")).toHaveCount(2);

    await input.fill(needle);
    await expect(page.getByRole("status").filter({ hasText: "0 matches in loaded messages" })).toBeVisible();
    await page.getByRole("button", { name: "Search all history" }).click();
    await expect(page.getByRole("status").filter({ hasText: /Searched all \d+ messages · 1 matches/ })).toBeVisible({ timeout: 20000 });
    const result = page.getByRole("list", { name: "Search results" }).getByRole("button");
    await expect(result).toHaveCount(1);
    await expect(result).toContainText(server.accounts.bea.nickname);
    await expect(result.locator("mark")).toHaveText(needle);
    await result.click();
    const target = log.locator(".msg", { hasText: needle });
    await expect(target).toBeInViewport();
    await expect(target).toHaveClass(/flash/);
    await expect(log.locator(".msg.search-miss")).toHaveCount(0);
});

test("global search filters by person and opens the conversation at the message", async () => {
    await page.goto(`${server.webURL}/app/#/home`);
    await page.getByRole("link", { name: "Search", exact: true }).click();
    await page.getByRole("searchbox", { name: "Search messages" }).fill(`kiwi ${run}`);
    const results = page.getByRole("region", { name: "Search results" });
    await expect(results.getByRole("button")).toHaveCount(2);
    await page.getByText("Filters").click();
    await page.getByLabel("From", { exact: true }).selectOption({ label: server.accounts.cid.nickname });
    await expect(results.getByRole("button")).toHaveCount(1);
    const group = results.getByRole("region", { name: `# ${channelName}` });
    await expect(group).toContainText("kiwi from cid");
    await expect(group.getByRole("link", { name: "Open in conversation" })).toHaveAttribute("href", `#/chat/${channelID}`);
    await page.getByLabel("Order").selectOption("oldest");
    await results.getByRole("button").click();
    await expect(page).toHaveURL(new RegExp(`#/chat/${channelID}$`));
    await expect(page.locator(".msg", { hasText: "kiwi from cid" })).toHaveClass(/flash/);
});

test("the inbox lists an unread direct message and a mention", async () => {
    await page.goto(`${server.webURL}/app/#/inbox`);
    await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();

    // Bea writes to Abe directly (one copy per device) and mentions Abe in the channel.
    const directory = await bea.conn.request(Msg.KeyRequest, { unique_id: abe.uniqueID, devices: true }, Msg.KeyResponse, { accept: r => r.unique_id === abe.uniqueID });
    const copies = [...directory.devices, { device_id: bea.deviceID, x25519_public_key: btoa(String.fromCharCode(...bea.identity.xPublic)) }]
        .map(d => ({ device_id: d.device_id, text: sealDM(`direct hello ${run}`, fromBase64(d.x25519_public_key), bea.identity.xSecret) }));
    await bea.conn.request(Msg.ChatSend, { to_unique_id: abe.uniqueID, enc: true, ack_requested: true, client_msg_id: `bea-dm-${run}`, copies },
        154, { accept: r => r.client_msg_id === `bea-dm-${run}` });
    await send(beaChat, `@${server.accounts.abe.nickname} please look ${run}`);

    const unread = page.getByRole("list", { name: "Unread" });
    const dmRow = unread.getByRole("link").filter({ hasText: `direct hello ${run}` });
    await expect(dmRow).toContainText(server.accounts.bea.nickname, { timeout: 15000 });
    const channelRow = unread.getByRole("link").filter({ hasText: channelName });
    await expect(channelRow).toContainText("please look");
    await expect(channelRow.getByLabel("1 unread")).toHaveText("@1");
    const mentions = page.getByRole("list", { name: "Mentions" });
    await expect(mentions.getByRole("link")).toHaveCount(1);
    await expect(mentions.getByRole("link")).toContainText(`${server.accounts.bea.nickname} in # ${channelName}`);

    await page.getByRole("button", { name: "Mark all read" }).click();
    await expect(page.getByText("You're all caught up.")).toBeVisible();
    await expect(mentions).toHaveCount(0);
});

let threadID;

test("a thread is created on the phone, answered and moderated, and its message saved", async () => {
    await openChannel();
    await page.getByRole("link", { name: /^Threads/ }).click();
    await expect(page).toHaveURL(new RegExp(`#/threads/${channelID}$`));
    await page.getByRole("button", { name: "New thread" }).click();
    const form = page.getByRole("dialog", { name: "New thread" });
    await form.getByLabel("Title").fill(`Release plan ${run}`);
    await form.getByLabel("First message").fill("What ships on Friday?");
    await form.getByRole("button", { name: "Create thread" }).click();
    await expect(page).toHaveURL(new RegExp(`#/thread/${channelID}/\\d+$`));
    threadID = Number(new URL(page.url()).hash.split("/").pop());
    await expect(page.getByRole("heading", { name: `Release plan ${run}` })).toBeVisible();
    const log = page.getByRole("log", { name: "Thread" });
    await expect(log).toContainText("What ships on Friday?");

    // The reply reaches a desktop reading the thread's history.
    const reply = page.getByRole("textbox", { name: "Reply in thread" });
    await reply.fill("The search screen.");
    await reply.press("Enter");
    await expect(log).toContainText("The search screen.");
    const history = await discussion(bea, { action: "history", channel_id: channelID, thread_id: threadID });
    expect(history.messages.map(m => beaChat.decrypt(channelID, m.key_id, m.body_enc))).toContain("The search screen.");

    // Bea answers from the desktop; the phone shows it live.
    await discussion(bea, { action: "join", channel_id: channelID, thread_id: threadID });
    const keyID = beaChat.current.get(channelID);
    const { sealScope } = await import("../src/core/crypto.js");
    await discussion(bea, { action: "send", channel_id: channelID, thread_id: threadID, request_id: `bea-${run}`, key_id: keyID,
        body_enc: sealScope("And polls.", beaChat.keys.get(channelID).get(keyID)) });
    await expect(log).toContainText("And polls.");

    // Follow, pin and resolve; a desktop sees each change.
    const follow = page.getByRole("button", { name: "Following" });
    await expect(follow).toHaveAttribute("aria-pressed", "true");
    await follow.click();
    await expect(page.getByRole("button", { name: "Follow", exact: true })).toHaveAttribute("aria-pressed", "false");
    await expect.poll(async () => (await discussion(abe, { action: "state", channel_id: channelID, thread_id: threadID })).threads[0].subscribed).toBe(false);
    await page.getByRole("button", { name: "Follow", exact: true }).click();
    await expect(page.getByRole("button", { name: "Following" })).toHaveAttribute("aria-pressed", "true");
    await page.getByRole("button", { name: "Pin", exact: true }).click();
    await expect(page.getByRole("button", { name: "Pinned" })).toHaveAttribute("aria-pressed", "true");
    await page.getByRole("button", { name: "Mark resolved" }).click();
    await expect(page.getByRole("button", { name: "Resolved" })).toHaveAttribute("aria-pressed", "true");
    const state = (await discussion(bea, { action: "state", channel_id: channelID, thread_id: threadID })).threads[0];
    expect(state).toMatchObject({ pinned: true, resolved: true, title: `Release plan ${run}` });

    // Saved messages: keep the thread's first message and find it in Saved.
    await page.getByRole("button", { name: "More options" }).click();
    await page.getByRole("button", { name: "Save first message" }).click();
    await page.evaluate(() => { location.hash = "#/saved"; });
    const saved = page.getByRole("tabpanel", { name: "Saved" });
    await expect(saved.getByRole("listitem")).toHaveCount(1);
    await expect(saved).toContainText("What ships on Friday?");
    await expect(saved).toContainText(`Release plan ${run}`);
    // Moving it to a new collection adds a tab.
    await saved.getByRole("button", { name: "Move", exact: true }).click();
    await page.getByRole("button", { name: "New collection" }).click();
    await page.getByLabel("Collection name").fill("Later");
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.getByRole("tab", { name: /Later/ }).click();
    const later = page.getByRole("tabpanel", { name: "Later" });
    await expect(later).toContainText("What ships on Friday?");
    await later.getByRole("button", { name: "Open", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`#/thread/${channelID}/${threadID}$`));

    // The list shows the thread with its badges.
    await page.getByRole("link", { name: "Back" }).click();
    const row = page.getByRole("list", { name: "Threads" }).getByRole("link", { name: new RegExp(`Release plan ${run}`) });
    await expect(row).toContainText("Pinned");
    await expect(row).toContainText("Resolved");
    await page.getByRole("button", { name: "Following only" }).click();
    await expect(row).toBeVisible();
});

test("a thread opens from a channel message, created the first time", async () => {
    const message = beaChat.messages.get(channelID).find(m => typeof m.text === "string" && m.text.startsWith("kiwi from bea"));
    await openChannel();
    await page.evaluate(detail => window.dispatchEvent(new CustomEvent("noxa:open-thread", { detail })), { scope: channelID, messageID: message.id });
    const form = page.getByRole("dialog", { name: "New thread" });
    await expect(form.getByLabel("First message")).toHaveValue(message.text);
    await form.getByLabel("Title").fill(`About kiwis ${run}`);
    await form.getByRole("button", { name: "Create thread" }).click();
    await expect(page).toHaveURL(new RegExp(`#/thread/${channelID}/\\d+$`));
    const created = Number(new URL(page.url()).hash.split("/").pop());
    expect((await discussion(bea, { action: "state", channel_id: channelID, thread_id: created })).threads[0].root_message_id).toBe(message.id);

    // The second time the existing thread opens.
    await openChannel();
    await page.evaluate(detail => window.dispatchEvent(new CustomEvent("noxa:open-thread", { detail })), { scope: channelID, messageID: message.id });
    await expect(page).toHaveURL(new RegExp(`#/thread/${channelID}/${created}$`));
});

test("a poll is created on the phone, voted on from a desktop, voted and closed", async () => {
    await openChannel();
    await page.getByRole("button", { name: "Attach a file" }).click();
    await page.getByRole("button", { name: "Create poll" }).click();
    const form = page.getByRole("dialog", { name: "Create poll" });
    await form.getByLabel("Question").fill(`Lunch ${run}?`);
    await form.getByLabel("Option 1").fill("Pizza");
    await form.getByLabel("Option 2").fill("Sushi");
    await form.getByRole("button", { name: "Add option" }).click();
    await form.getByLabel("Option 3").fill("pizza");
    await form.getByRole("button", { name: "Create poll" }).click();
    await expect(form.getByRole("alert")).toHaveText("Each option must be different.");
    await form.getByRole("button", { name: "Remove option" }).nth(2).click();
    await form.getByRole("radio", { name: "1 hour" }).click();
    await form.getByRole("button", { name: "Create poll" }).click();
    await expect(form).toHaveCount(0);

    const card = page.getByRole("group", { name: `Poll: Lunch ${run}?` });
    await expect(card.getByRole("status")).toHaveText("Open · 0 voters");
    await expect(card).toContainText(/Closes in (59|60) min|Closes in 1 h 0 min/);

    // Bea votes from her desktop; the phone's card follows the poll event.
    const findPoll = () => beaChat.messages.get(channelID).find(m => m.id && m.text?.includes(`Lunch ${run}?`));
    await expect.poll(() => !!findPoll()).toBe(true);
    const pollMessage = findPoll();
    await beaChat.poll(pollMessage.id, "vote", [1]);
    await expect(card.getByRole("status")).toHaveText("Open · 1 voters");

    // The phone votes Pizza and sees the results.
    await card.getByRole("button", { name: "Pizza" }).click();
    await card.getByRole("button", { name: "Vote" }).click();
    await expect(card.getByRole("button", { name: "Pizza: 1 · 50%" })).toHaveAttribute("aria-pressed", "true");
    await expect(card.getByRole("button", { name: "Sushi: 1 · 50%" })).toBeVisible();
    await expect(card.getByRole("status")).toHaveText("Open · 2 voters · You voted");
    expect((await beaChat.poll(pollMessage.id, "get")).counts).toEqual([1, 1]);

    // Closing ends voting everywhere.
    await card.getByRole("button", { name: "Close poll" }).click();
    await expect(card.getByRole("status")).toHaveText("Closed · 2 voters");
    await expect(card.getByRole("button", { name: "Vote" })).toBeHidden();
    await expect(card.getByRole("button", { name: "Sushi: 1 · 50%" })).toBeDisabled();
    expect((await beaChat.poll(pollMessage.id, "get")).closed).toBe(true);
});

test("forum mode shows the post list when the channel opens", async () => {
    await page.goto(`${server.webURL}/app/#/threads/${channelID}`);
    await page.getByRole("button", { name: "Forum settings" }).click();
    const settings = page.getByRole("dialog", { name: "Forum settings" });
    await settings.getByRole("checkbox").check();
    await settings.getByLabel("Tags (one per line)").fill("release\nquestion");
    await settings.getByLabel("Archive inactive threads").selectOption("72");
    await settings.getByRole("button", { name: "Save" }).click();
    await expect(settings).toHaveCount(0);
    const configured = await discussion(bea, { action: "list", channel_id: channelID });
    expect(configured).toMatchObject({ forum: true, tags: ["release", "question"], auto_archive_hours: 72 });

    // Opening the channel now shows its posts; Chat still reaches the log.
    await page.goto(`${server.webURL}/app/#/home`);
    await page.goto(`${server.webURL}/app/#/chat/${channelID}`);
    await expect(page).toHaveURL(new RegExp(`#/threads/${channelID}$`), { timeout: 15000 });
    await expect(page.getByText("This channel is a forum")).toBeVisible();
    await page.getByRole("button", { name: "New post" }).click();
    const post = page.getByRole("dialog", { name: "New post" });
    await post.getByLabel("Title").fill(`Tagged ${run}`);
    await post.getByLabel("First message").fill("With a tag");
    await post.getByRole("checkbox", { name: "question" }).check();
    await post.getByRole("button", { name: "Publish post" }).click();
    await expect(page.locator(".chips").first()).toContainText("question");
    await page.getByRole("link", { name: "Back" }).click();
    await page.getByLabel("Filter by tag").selectOption("question");
    await expect(page.getByRole("list", { name: "Threads" }).getByRole("link")).toHaveCount(1);
    await page.getByRole("link", { name: "Chat", exact: true }).click();
    await expect(page.getByRole("log", { name: `# ${channelName}` })).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`#/chat/${channelID}$`));

    // Leave the channel a plain chat again.
    await discussion(owner, { action: "configure", channel_id: channelID, forum: false, tags: [] });
});

test("the tool screens fit a 360 px wide phone", async () => {
    await page.setViewportSize({ width: 360, height: 740 });
    for (const route of ["home", "search", "inbox", "saved", `threads/${channelID}`, `thread/${channelID}/${threadID}`, `chat/${channelID}`]) {
        await page.goto(`${server.webURL}/app/#/${route}`);
        await expect(page.locator(".screen-header h1")).toBeVisible({ timeout: 15000 });
        const widest = await page.evaluate(() => Math.max(...[...document.querySelectorAll(".screen, .screen *")].map(node => node.getBoundingClientRect().right)));
        expect(widest, route).toBeLessThanOrEqual(360);
    }
});
