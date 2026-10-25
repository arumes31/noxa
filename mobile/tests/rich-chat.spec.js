// rich-chat.spec.js — rich message rendering on the phone: markdown, links,
// emoji and custom emoji, link previews, inline images and the lightbox,
// attachment sources, the share screen, blocked senders and export.
import { readFile } from "node:fs/promises";

import { devices, expect, test } from "@playwright/test";

import { openExport } from "../src/core/chat-export.js";
import { ChatSession } from "../src/core/chat-session.js";
import { parseFileTokens } from "../src/core/files.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
// A 1×1 PNG.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
let info;
// One linked phone for the whole file: every link adds a device to the
// owner's account, and accounts have a device limit.
let page, desktop, chat;

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ browser }) => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    chat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    // Earlier specs leave the owner's phones and desktops registered; free
    // their slots (the first device cannot be revoked from here and stays).
    for (const device of await deviceList()) if (!device.current) await revoke(device.device_id);
    const context = await browser.newContext({ ...devices["Pixel 7"], permissions: ["microphone"] });
    page = await context.newPage();
    page.on("pageerror", error => console.log("pageerror:", error.message));
    await linkPhone(page, desktop, server.webURL, expect);
});

test.afterAll(async () => {
    // Sign the phone and this desktop out again so later specs keep free slots.
    const list = await deviceList().catch(() => []);
    for (const device of list) if (!device.current) await revoke(device.device_id);
    const self = list.find(d => d.current);
    if (self) await revoke(self.device_id);
    chat?.close();
    await page?.context().close();
});

/** Sends as the desktop, waiting out the account's chat rate limit (5 per 3 s). */
async function send(text) {
    for (let attempt = 0; ; attempt++) {
        try {
            return await chat.send(0, text);
        } catch (error) {
            if (attempt >= 10 || !/rate limit/.test(error.message)) throw error;
            await new Promise(resolve => setTimeout(resolve, 1000));
        }
    }
}

function revoke(deviceID) {
    return desktop.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: deviceID }, Msg.DeviceResult, { accept: r => r.action === "revoke", timeout: 5000 }).catch(() => {});
}

async function deviceList() {
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    return list.devices || [];
}

/** Opens Home first so each test starts from the same place. */
async function openServerChat() {
    await page.goto(`${server.webURL}/app/#/home`);
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("link", { name: "Server chat" }).click();
    await expect(page.getByRole("log", { name: "Server chat" })).toBeVisible();
}

function received(chat, match) {
    return new Promise(resolve => {
        const check = () => {
            const found = (chat.messages.get(0) || []).find(m => typeof m.text === "string" && match(m.text));
            if (found) { chat.removeEventListener("messages", check); resolve(found); }
        };
        chat.addEventListener("messages", check);
        check();
    });
}

test("markdown, links, emoji and custom server emoji render", async () => {
    const emoji = `party${Date.now().toString(36)}`;
    const added = new Promise(resolve => {
        const stop = desktop.conn.on(Msg.Event, event => {
            if (event.type === "emoji_added" && event.data?.name === emoji) { stop(); resolve(); }
        });
    });
    desktop.conn.send(Msg.EmojiUpload, { name: emoji, data_base64: PNG.toString("base64") });
    await added;
    await openServerChat();

    const long = `https://example.com/${"segment/".repeat(12)}end`;
    await send([
        `**bold** *it* __under__ ~~gone~~ \`inline\` :fire: :${emoji}:`,
        "```js", "let x = 1;", "```",
        "| a | b |", "|---|---|", "| 1 | 2 |",
        `see ${long}`,
    ].join("\n"));
    const log = page.getByRole("log", { name: "Server chat" });
    const message = log.locator(".msg", { hasText: "bold" }).last();
    await expect(message.locator("strong")).toHaveText("bold");
    await expect(message.locator("em")).toHaveText("it");
    await expect(message.locator("u")).toHaveText("under");
    await expect(message.locator("s")).toHaveText("gone");
    await expect(message.locator("code.md-code")).toHaveText("inline");
    await expect(message.locator(".emoji")).toHaveText("🔥");
    const custom = message.getByRole("img", { name: `:${emoji}:` });
    await expect(custom).toHaveAttribute("src", /^data:image\//);
    await expect(message.locator(".md-pre figcaption")).toHaveText("js");
    await expect(message.locator(".md-pre pre")).toHaveText("let x = 1;");
    await expect(message.getByRole("region", { name: "Table" }).locator("th").first()).toHaveText("a");
    await expect(message.locator("td").last()).toHaveText("2");
    const link = message.locator("a.md-link");
    await expect(link).toHaveAttribute("href", long);
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect(link).toHaveAttribute("title", long);
    const shown = await link.textContent();
    expect(shown.length).toBe(60);
    expect(shown).toContain("…");
    await expect(message.getByRole("img", { name: "Encrypted with the channel key" })).toBeVisible();
    await expect(log.getByRole("separator", { name: "Today" })).toBeVisible();

    // The picker inserts at the caret; search finds built-in and server emoji.
    const composer = page.getByRole("textbox", { name: /Message/ });
    await composer.fill("hot ");
    await page.getByRole("button", { name: "Insert emoji" }).click();
    const picker = page.getByRole("dialog", { name: "Emoji" });
    await expect(picker.getByRole("button", { name: `:${emoji}:` })).toBeVisible();
    await picker.getByRole("searchbox", { name: "Search emoji by name" }).fill("fire");
    await picker.getByRole("button", { name: ":fire:" }).click();
    await expect(picker).toHaveCount(0);
    await expect(composer).toHaveValue("hot 🔥");
    await page.getByRole("button", { name: "Insert emoji" }).click();
    await page.getByRole("dialog", { name: "Emoji" }).getByRole("button", { name: `:${emoji}:` }).first().click();
    await expect(composer).toHaveValue(`hot 🔥:${emoji}:`);
});

test("a link preview card appears under the first link", async () => {
    // The server fetches the page itself and refuses local addresses, so this
    // needs the internet; without it the test is skipped.
    const online = await fetch("https://example.com/", { signal: AbortSignal.timeout(5000) }).then(r => r.ok, () => false);
    test.skip(!online, "example.com is not reachable from this machine");
    await openServerChat();
    await send("preview this https://example.com/ please");
    const card = page.getByRole("group", { name: "Link preview" }).last();
    await expect(card).toContainText("Example Domain", { timeout: 20000 });
    await expect(card).toContainText("example.com");
    await card.getByRole("button", { name: "Hide preview" }).click();
    await expect(card.getByRole("button", { name: "Show preview" })).toHaveAttribute("aria-expanded", "false");
    await expect(card.locator(".lp-body")).toBeHidden();
});

test("attachments: camera source, inline image with lightbox, video on tap", async () => {
    await openServerChat();

    // The attach button offers library, camera and file.
    await page.getByRole("button", { name: "Attach a file" }).click();
    const sheet = page.getByRole("dialog", { name: "Attach a file" });
    await expect(sheet.getByRole("button", { name: "Photo library" })).toBeVisible();
    await expect(sheet.getByRole("button", { name: "File" })).toBeVisible();
    // Record what would open instead of opening a native chooser (a pending
    // chooser makes setInputFiles stall now and then).
    const input = page.locator("input[type=file]");
    await input.evaluate(node => { node.click = () => { window.noxaPicked = { capture: node.getAttribute("capture"), accept: node.getAttribute("accept") }; }; });
    await sheet.getByRole("button", { name: "Take photo" }).click();
    expect(await page.evaluate(() => window.noxaPicked)).toEqual({ capture: "environment", accept: "image/*" });
    const sent = received(chat, text => text.includes("[file:") && text.includes("camera.png"));
    await input.setInputFiles({ name: "camera.png", mimeType: "image/png", buffer: PNG });
    const message = await sent;
    expect(parseFileTokens(message.text)[0].name).toBe("camera.png");

    // inlineImages is on by default: the image loads without a tap.
    const image = page.getByRole("img", { name: "camera.png" });
    await expect(image).toBeVisible();
    expect(await image.evaluate(img => img.naturalWidth)).toBe(1);
    await image.click();
    const lightbox = page.getByRole("dialog", { name: "camera.png" });
    await expect(lightbox.getByRole("img", { name: "camera.png" })).toBeVisible();
    await expect(lightbox.getByRole("button", { name: "Share or save" })).toBeVisible();
    await lightbox.getByRole("button", { name: "Zoom" }).click();
    await expect(lightbox.getByRole("button", { name: "Zoom" })).toHaveAttribute("aria-pressed", "true");
    await lightbox.getByRole("button", { name: "Close" }).click();
    await expect(lightbox).toHaveCount(0);

    // Videos wait for a tap, then play in the native player.
    const videoSent = received(chat, text => text.includes("clip.mp4"));
    await page.getByRole("button", { name: "Attach a file" }).click();
    await page.getByRole("dialog", { name: "Attach a file" }).getByRole("button", { name: "File" }).click();
    expect(await page.evaluate(() => window.noxaPicked)).toEqual({ capture: null, accept: null });
    // The account's chat rate limit (5 per 3 s) may refuse a send this soon; the
    // user would just try again, and so does the test.
    await expect(async () => {
        await input.setInputFiles({ name: "clip.mp4", mimeType: "video/mp4", buffer: Buffer.from("not really a video") });
        await Promise.race([videoSent, new Promise((_, reject) => setTimeout(() => reject(new Error("clip.mp4 not delivered yet")), 4000))]);
    }).toPass({ timeout: 30000 });
    await page.getByRole("button", { name: "Load video clip.mp4" }).last().click();
    await expect(page.locator("video.file-video").last()).toHaveAttribute("controls", "");
});

test("shared content waits on the share screen and goes to the picked chat", async () => {
    // Without a service worker (plain http in tests) the server still turns
    // the share target's POST into the share screen.
    const posted = await fetch(`${server.webURL}/app/share`, { method: "POST", body: new FormData(), redirect: "manual" });
    expect(posted.status).toBe(303);
    expect(posted.headers.get("location")).toBe("/app/#/share");

    const marker = `shared-${Date.now().toString(36)}`;
    // What the service worker stores for a POST to /app/share (core/share-store.js).
    await page.evaluate(async ({ text }) => {
        const db = await new Promise((resolve, reject) => {
            const request = indexedDB.open("noxa"); // the current version, whatever stores the app added
            request.onsuccess = () => resolve(request.result);
            request.onerror = () => reject(request.error);
        });
        const key = await new Promise((resolve, reject) => {
            const request = db.transaction("device").objectStore("device").get("wrapping-key");
            request.onsuccess = () => resolve(request.result);
            request.onerror = () => reject(request.error);
        });
        const seal = async bytes => {
            const iv = crypto.getRandomValues(new Uint8Array(12));
            return { iv, sealed: new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, bytes)) };
        };
        const encode = value => new TextEncoder().encode(value);
        const value = { at: Date.now(), text: await seal(encode(text)), files: [{ name: "note.txt", type: "text/plain", record: await seal(encode("file body")) }] };
        await new Promise((resolve, reject) => {
            const tx = db.transaction("share", "readwrite");
            tx.objectStore("share").put(value, "pending");
            tx.oncomplete = resolve;
            tx.onerror = () => reject(tx.error);
        });
        db.close();
    }, { text: marker });
    await page.goto(`${server.webURL}/app/#/share`);
    await expect(page.getByRole("textbox", { name: "Message" })).toHaveValue(marker, { timeout: 15000 });
    await expect(page.getByText("note.txt")).toBeVisible();
    const sent = received(chat, text => text.startsWith(marker) && text.includes("note.txt"));
    await page.getByRole("button", { name: /Server chat/ }).click();
    await sent;
    await expect(page.getByRole("log", { name: "Server chat" })).toContainText(marker);
    // The share is gone once sent.
    await page.goto(`${server.webURL}/app/#/share`);
    await expect(page.getByText("Nothing waiting to be shared", { exact: false })).toBeVisible({ timeout: 15000 });
});

test("messages from blocked users are folded until shown", async () => {
    await openServerToMembers(desktop);
    const rowan = await desktopSession(server.controlAddr, server.accounts.rowan, { version: info.version });
    const rowanChat = new ChatSession(rowan.conn, rowan.identity, rowan.auth);
    await page.evaluate(uid => localStorage.setItem("noxa.settings", JSON.stringify({ blockedUsers: [uid] })), rowan.uniqueID);
    // Settings are read once per page load.
    await page.reload();
    await openServerChat();
    const secret = `from rowan ${Date.now().toString(36)}`;
    await rowanChat.send(0, secret);
    const log = page.getByRole("log", { name: "Server chat" });
    await expect(log.getByText("Blocked message").last()).toBeVisible();
    await expect(log).not.toContainText(secret);
    await log.getByRole("button", { name: "Show" }).last().click();
    await expect(log).toContainText(secret);
    rowanChat.close();
    await page.evaluate(() => localStorage.removeItem("noxa.settings"));
});

test("chat history exports as text and as an encrypted file", async () => {
    await openServerChat();
    const marker = `export-me-${Date.now().toString(36)}`;
    await send(marker);
    await expect(page.getByRole("log", { name: "Server chat" })).toContainText(marker);

    await page.getByRole("button", { name: "More options" }).click();
    await page.getByRole("button", { name: "Export history" }).click();
    let dialog = page.getByRole("dialog", { name: "Export chat" });
    let [download] = await Promise.all([page.waitForEvent("download"), dialog.getByRole("button", { name: "Export", exact: true }).click()]);
    expect(download.suggestedFilename()).toMatch(/^noxa-server-chat-\d{8}\.txt$/);
    const text = await readFile(await download.path(), "utf8");
    expect(text).toMatch(new RegExp(`\\] \\[server encrypted\\] ${server.accounts.owner.nickname}(#M\d+)?: ${marker}`));

    await page.getByRole("button", { name: "More options" }).click();
    await page.getByRole("button", { name: "Export history" }).click();
    dialog = page.getByRole("dialog", { name: "Export chat" });
    await dialog.getByRole("radio", { name: /Encrypted with a passphrase/ }).check();
    await dialog.getByLabel("Passphrase (at least 8 characters)").fill("correct horse");
    await dialog.getByLabel("Repeat passphrase").fill("correct horse");
    [download] = await Promise.all([page.waitForEvent("download", { timeout: 30000 }), dialog.getByRole("button", { name: "Export", exact: true }).click()]);
    const sealed = await readFile(await download.path(), "utf8");
    expect(sealed).not.toContain(marker);
    const opened = JSON.parse(await openExport(sealed, "correct horse"));
    expect(opened.messages.some(m => m.text === marker && m.encryption === "server")).toBe(true);
});
