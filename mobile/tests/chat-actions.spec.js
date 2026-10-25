// chat-actions.spec.js — per-message actions and composer helpers in the
// server chat on the phone: reply with quote and jump into older history,
// reactions, pins, edit and delete, mention completion, keyword highlights,
// undo send and the unread markers. A phone linked to the owner acts; the
// owner's desktop session and a second member (lou) observe and write.
import { devices, expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
const run = Date.now().toString(36);
let info;
let page, desktop, chat, lou, louChat;

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ browser }) => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    chat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    await openServerToMembers(desktop);
    // Earlier specs leave the owner's phones registered; free their slots.
    for (const device of await deviceList(desktop)) if (!device.current) await revoke(desktop, device.device_id);
    lou = await desktopSession(server.controlAddr, server.accounts.lou, { version: info.version });
    louChat = new ChatSession(lou.conn, lou.identity, lou.auth);
    const context = await browser.newContext({ ...devices["Pixel 7"], permissions: ["microphone", "clipboard-read", "clipboard-write"] });
    page = await context.newPage();
    page.on("pageerror", error => console.log("pageerror:", error.message));
    await linkPhone(page, desktop, server.webURL, expect);
});

test.afterAll(async () => {
    const list = await deviceList(desktop).catch(() => []);
    for (const device of list) if (!device.current) await revoke(desktop, device.device_id);
    const self = list.find(d => d.current);
    if (self) await revoke(desktop, self.device_id);
    const louSelf = (await deviceList(lou).catch(() => [])).find(d => d.current);
    if (louSelf) await revoke(lou, louSelf.device_id);
    chat?.close();
    louChat?.close();
    await page?.context().close();
});

function revoke(session, deviceID) {
    return session.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: deviceID }, Msg.DeviceResult, { accept: r => r.action === "revoke", timeout: 5000 }).catch(() => {});
}

async function deviceList(session) {
    const list = await session.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    return list.devices || [];
}

/** Sends as lou, waiting out the account's chat rate limit. */
async function louSays(text, options) {
    for (let attempt = 0; ; attempt++) {
        try {
            return await louChat.send(0, text, options);
        } catch (error) {
            if (attempt >= 20 || !/rate limit/.test(error.message)) throw error;
            await new Promise(resolve => setTimeout(resolve, 700));
        }
    }
}

/** Resolves with the first server event of type matching data. */
function nextEvent(conn, type, match = () => true) {
    return new Promise(resolve => {
        const stop = conn.on(Msg.Event, event => {
            if (event.type === type && event.data && match(event.data)) { stop(); resolve(event.data); }
        });
    });
}

/** Resolves with the owner desktop's copy of the first message matching. */
function received(session, match) {
    return new Promise(resolve => {
        const check = () => {
            const found = (session.messages.get(0) || []).find(m => typeof m.text === "string" && match(m));
            if (found) { session.removeEventListener("messages", check); resolve(found); }
        };
        session.addEventListener("messages", check);
        check();
    });
}

const log = () => page.getByRole("log", { name: "Server chat" });
const composer = () => page.getByRole("textbox", { name: /Message/ });
const messageWith = text => log().locator(".msg", { hasText: text }).last();

async function openServerChat() {
    await page.goto(`${server.webURL}/app/#/home`);
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("link", { name: "Server chat" }).click();
    await expect(log()).toBeVisible();
}

async function actionsOf(text) {
    await messageWith(text).locator(".msg-from").click();
    const dialog = page.getByRole("dialog", { name: /^Message from/ });
    await expect(dialog).toBeVisible();
    return dialog;
}

async function sendFromPhone(text) {
    await composer().fill(text);
    await composer().press("Enter");
}

test("reply with a quote, and the quote jumps into older history", async () => {
    test.setTimeout(180000);
    await openServerChat();
    const original = await louSays(`original ${run}`);
    await expect(messageWith(`original ${run}`)).toBeVisible();

    const dialog = await actionsOf(`original ${run}`);
    await dialog.getByRole("button", { name: "Reply", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: `Replying to ${server.accounts.lou.nickname}` })).toBeVisible();
    const answered = received(chat, m => m.text === `answer ${run}`);
    await sendFromPhone(`answer ${run}`);
    const answer = await answered;
    expect(answer.replyTo).toBe(original.message_id);
    const quote = messageWith(`answer ${run}`).locator(".reply-quote");
    await expect(quote).toContainText(server.accounts.lou.nickname);
    await expect(quote).toContainText(`original ${run}`);
    await expect(page.getByRole("button", { name: "Cancel reply" })).toHaveCount(0);

    // Push the original out of the first history page, then reply to it.
    for (let i = 0; i < 52; i++) await louSays(`filler ${i} ${run}`);
    await louSays(`late answer ${run}`, { replyTo: original.message_id });

    await page.reload();
    await openServerChat();
    const late = messageWith(`late answer ${run}`);
    await expect(late).toBeVisible();
    await expect(log().locator(".msg", { hasText: `original ${run}` })).toHaveCount(0);
    await late.getByRole("button", { name: /Show the original message/ }).click();
    const target = log().locator(".msg", { hasText: `original ${run}` }).first();
    await expect(target).toBeInViewport();
    await expect(target).toHaveClass(/flash/);
});

test("reactions toggle and the desktop sees them", async () => {
    await openServerChat();
    await louSays(`react to me ${run}`);
    const dialog = await actionsOf(`react to me ${run}`);
    const added = nextEvent(desktop.conn, "chat_reaction", d => d.emoji === "👍" && d.by === desktop.uniqueID);
    await dialog.getByRole("button", { name: "React with 👍" }).click();
    expect((await added).added).toBe(true);
    const chip = messageWith(`react to me ${run}`).getByRole("button", { name: /^👍 1/ });
    await expect(chip).toHaveAttribute("aria-pressed", "true");

    const removed = nextEvent(desktop.conn, "chat_reaction", d => d.emoji === "👍" && d.by === desktop.uniqueID);
    await chip.click();
    expect((await removed).added).toBe(false);
    await expect(messageWith(`react to me ${run}`).locator(".reaction")).toHaveCount(0);
});

test("pin a message and unpin it from the pinned list", async () => {
    await openServerChat();
    await louSays(`pin me ${run}`);
    const dialog = await actionsOf(`pin me ${run}`);
    const pinned = nextEvent(desktop.conn, "chat_pinned");
    await dialog.getByRole("button", { name: "Pin", exact: true }).click();
    await pinned;
    await expect(messageWith(`pin me ${run}`).getByRole("img", { name: "Pinned message" })).toBeVisible();

    await page.getByRole("button", { name: "More options" }).click();
    await page.getByRole("dialog", { name: "More options" }).getByRole("button", { name: "Pinned messages" }).click();
    const pins = page.getByRole("dialog", { name: "Pinned messages" });
    const row = pins.locator(".act-pin", { hasText: `pin me ${run}` });
    await expect(row).toBeVisible();
    const unpinned = nextEvent(desktop.conn, "chat_unpinned");
    await row.getByRole("button", { name: /^Unpin/ }).click();
    await unpinned;
    await expect(row).toHaveCount(0);
    await pins.getByRole("button", { name: "Close" }).click();
    await expect(messageWith(`pin me ${run}`).getByRole("img", { name: "Pinned message" })).toHaveCount(0);
});

test("edit and delete an own channel message", async () => {
    await openServerChat();
    const sent = received(chat, m => m.text === `edit me ${run}`);
    await sendFromPhone(`edit me ${run}`);
    const original = await sent;
    await expect(messageWith(`edit me ${run}`)).toHaveAttribute("data-id", String(original.id));

    let dialog = await actionsOf(`edit me ${run}`);
    await dialog.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(composer()).toHaveValue(`edit me ${run}`);
    await expect(page.getByRole("status").filter({ hasText: "Editing message" })).toBeVisible();
    const edited = nextEvent(desktop.conn, "chat_edited", d => d.message_id === original.id);
    await composer().fill(`edited ${run}`);
    await composer().press("Enter");
    await edited;
    await expect.poll(() => chat.messageByID(0, original.id)?.text).toBe(`edited ${run}`);
    await expect(messageWith(`edited ${run}`).locator(".msg-edited")).toHaveText("edited");

    dialog = await actionsOf(`edited ${run}`);
    const deleted = nextEvent(desktop.conn, "chat_deleted", d => d.message_id === original.id);
    await dialog.getByRole("button", { name: "Delete" }).click();
    await page.getByRole("dialog", { name: "Delete message?" }).getByRole("button", { name: "Delete" }).click();
    await deleted;
    await expect(log().locator(`.msg[data-id="${original.id}"]`)).toContainText("Message deleted");
});

test("mention completion inserts a name and the member is mentioned", async () => {
    await openServerChat();
    const nickname = server.accounts.lou.nickname;
    await composer().fill("");
    await composer().pressSequentially(`hey @${nickname.slice(0, 5)}`);
    const list = page.getByRole("listbox", { name: "Mention suggestions" });
    const option = list.getByRole("option", { name: new RegExp(nickname) });
    await expect(option).toBeVisible();
    await expect(list.getByRole("option", { name: /@everyone/ })).toHaveCount(0);
    await option.click();
    await expect(composer()).toHaveValue(`hey @${nickname} `);
    await expect(list).toBeHidden();

    // Tab completes the nearest name as well.
    await composer().pressSequentially("and @lo");
    await composer().press("Tab");
    await expect(composer()).toHaveValue(`hey @${nickname} and @${nickname} `);

    const mentioned = nextEvent(lou.conn, "chat", d => (d.mentions || []).includes(lou.uniqueID));
    await composer().press("Enter");
    const event = await mentioned;
    expect(event.from_unique_id).toBe(desktop.uniqueID);
});

/** You → the settings registry's collapsible Chat section. */
async function openChatSettings() {
    await page.goto(`${server.webURL}/app/#/you`);
    const section = page.locator('details[data-section="chat"]');
    // Sections stay open while the app runs, so only a closed one is opened.
    await expect(section).toBeVisible();
    if (await section.getAttribute("open") === null) await section.locator("summary").click();
    await expect(section).toHaveAttribute("open", "");
}

test("keywords highlight messages like mentions", async () => {
    await openChatSettings();
    const field = page.getByLabel("Highlight keywords");
    await field.fill(`zebra${run}, giraffe`);
    await field.press("Enter");
    await field.blur();
    // Away from the chat, a keyword counts as a mention on the unread badge.
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await louSays(`nothing zebra${run}ish here`);
    await expect(page.getByRole("link", { name: /Server chat/ }).locator(".badge")).toHaveText("1");
    await louSays(`look, a ZEBRA${run} here`);
    await expect(page.getByRole("link", { name: /Server chat/ }).locator(".badge.badge-mention")).toHaveText("@2");
    await page.getByRole("link", { name: /Server chat/ }).click();
    await expect(log()).toBeVisible();
    await expect(messageWith(`ZEBRA${run} here`)).toHaveClass(/\bmention\b/);
    await expect(messageWith(`zebra${run}ish`)).not.toHaveClass(/\bmention\b/);
});

test("undo send keeps a message back and can cancel it", async () => {
    await openChatSettings();
    await page.getByLabel("Undo send").selectOption("3");
    await openServerChat();
    let leaked = false;
    const stop = desktop.conn.on(Msg.Event, event => {
        if (event.type === "chat" && chat.decrypt(0, event.data.key_id, event.data.text) === `take back ${run}`) leaked = true;
    });
    await sendFromPhone(`take back ${run}`);
    const held = messageWith(`take back ${run}`);
    await expect(held.getByRole("status")).toContainText(/Sending in \d s/);
    await held.getByRole("button", { name: "Undo" }).click();
    await expect(log().locator(".msg", { hasText: `take back ${run}` })).toHaveCount(0);
    await expect(composer()).toHaveValue(`take back ${run}`);

    // A held message does leave once its countdown ends.
    const arrived = received(chat, m => m.text === `goes out ${run}`);
    await composer().fill(`goes out ${run}`);
    await composer().press("Enter");
    await expect(messageWith(`goes out ${run}`).getByRole("status")).toContainText(/Sending in/);
    await arrived;
    await expect(messageWith(`goes out ${run}`)).not.toHaveClass(/\bheld\b/);
    // The undone one stayed back well past its countdown.
    expect(leaked).toBe(false);
    stop();

    await openChatSettings();
    await page.getByLabel("Undo send").selectOption("0");
});

test("unread divider on open and the new-messages pill", async () => {
    test.setTimeout(90000);
    await page.goto(`${server.webURL}/app/#/home`);
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    for (let i = 0; i < 3; i++) await louSays(`while away ${i} ${run}`);
    // Wait until the phone counted them (the badge on Home's server chat row).
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await expect(page.getByRole("link", { name: /Server chat/ }).getByLabel(/3 unread/)).toBeVisible();
    await page.getByRole("link", { name: /Server chat/ }).click();
    const divider = log().getByRole("separator", { name: "New messages" });
    await expect(divider).toBeVisible();
    const next = await divider.evaluate(node => node.nextElementSibling?.textContent || "");
    expect(next).toContain(`while away 0 ${run}`);

    await log().evaluate(node => { node.scrollTop = 0; });
    await louSays(`fresh one ${run}`);
    await louSays(`fresh two ${run}`);
    const pill = page.getByRole("button", { name: "2 new messages, scroll down" });
    await expect(pill).toBeVisible();
    await expect(pill).toHaveText("↓ 2 new");
    await pill.click();
    await expect(pill).toBeHidden();
    await expect(messageWith(`fresh two ${run}`)).toBeInViewport();
});
