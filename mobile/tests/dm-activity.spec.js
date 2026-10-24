import { expect, test } from "@playwright/test";

import { fromBase64, keyFingerprint, openDM, toBase64 } from "../src/core/crypto.js";
import { downloadBlob, fileToken, openAttachment, parseFileTokens, sealAttachment, storageName, uploadBlob } from "../src/core/files.js";
import { Msg } from "../src/core/frame.js";
import { contactDesktop, directFrom, eventWithin, nextEvent, openServer, phoneAccount, sendDM } from "./conversation-helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

// Node's WebSocket sends no Origin; the web listener's /ft requires one.
const NativeWebSocket = globalThis.WebSocket;
globalThis.WebSocket = class extends NativeWebSocket {
    constructor(url) { super(url, { headers: { origin: server.webURL } }); }
};

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    await openServer(server, info.version);
});

// A 1×1 PNG.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
const ftURL = () => `${server.webURL.replace(/^http/, "ws")}/ft`;

async function openDMWith(page, uid) {
    await page.evaluate(peer => { location.hash = `#/dm/${encodeURIComponent(peer)}`; }, uid);
    await expect(page.getByRole("note")).toContainText("End-to-end encrypted");
    const composer = page.getByRole("textbox", { name: /Message/ });
    await expect(composer).toBeEnabled({ timeout: 15000 });
    return composer;
}

test("DM attachments, typing and receipts work between the phone and a desktop", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const { desktop: pia } = await phoneAccount(page, server, server.accounts.pia, info.version, expect);
    const quin = await contactDesktop(server, server.accounts.quin, info.version);
    const composer = await openDMWith(page, quin.uniqueID);

    // Typing, both ways.
    const typed = nextEvent(quin, "typing", data => data.direct && data.unique_id === pia.uniqueID);
    await composer.pressSequentially("hel");
    await typed;
    await composer.fill("");
    quin.conn.send(Msg.Typing, { to_unique_id: pia.uniqueID });
    await expect(page.locator(".typing")).toContainText("is typing");

    // The phone's message gets ✓, then ✓✓ delivered, then ✓✓ read.
    const toQuin = directFrom(quin, pia.uniqueID);
    await composer.fill("hello Quin");
    await composer.press("Enter");
    const sent = await toQuin;
    const own = page.locator(".msg.own").last();
    await expect(own.getByRole("img", { name: "Sent" })).toBeVisible();
    await expect(page.locator(".typing")).toBeEmpty({ timeout: 8000 });
    quin.conn.send(Msg.ChatDelivered, { to_unique_id: pia.uniqueID, client_msg_id: sent.client_msg_id });
    await expect(own.getByRole("img", { name: "Delivered" })).toBeVisible();
    quin.conn.send(Msg.ChatRead, { to_unique_id: pia.uniqueID, client_msg_id: sent.client_msg_id });
    await expect(own.getByRole("img", { name: "Read" })).toBeVisible();
    await expect(own.locator(".msg-receipt.read")).toHaveText("✓✓");

    // Quin's message is acknowledged as delivered and, being on screen, read.
    const delivered = nextEvent(quin, "dm_delivered", data => data.client_msg_id === "quin-1");
    const read = nextEvent(quin, "dm_read", data => data.client_msg_id === "quin-1");
    await sendDM(quin, pia.uniqueID, "hi Pia", "quin-1");
    await expect(page.getByRole("log")).toContainText("hi Pia");
    expect((await delivered).from_unique_id).toBe(pia.uniqueID);
    await read;

    // The phone attaches a file with a caption; the desktop opens it from the server scope.
    const withFile = directFrom(quin, pia.uniqueID);
    await composer.fill("our logo");
    await page.locator("input[type=file]").setInputFiles({ name: "logo.png", mimeType: "image/png", buffer: PNG });
    const attachment = await withFile;
    const text = openDM(attachment.text, fromBase64(attachment.sender_device_key), quin.identity.xSecret);
    expect(text.startsWith("our logo [file:")).toBe(true);
    const [ref] = parseFileTokens(text);
    expect(ref.name).toBe("logo.png");
    const blob = await downloadBlob(quin.conn, 0, ref.storage, 1 << 20, ftURL());
    expect(Buffer.from(await openAttachment(blob, ref.master)).equals(PNG)).toBe(true);

    // The desktop sends a file the same way; the phone shows it.
    const sealed = await sealAttachment(new Uint8Array(PNG));
    const storage = storageName(sealed.blob);
    await uploadBlob(quin.conn, 0, storage, sealed.blob, ftURL());
    await sendDM(quin, pia.uniqueID, `from my desktop ${fileToken(storage, sealed.master, PNG.length, "quin.png")}`);
    await page.getByRole("button", { name: "Show image quin.png" }).click();
    const image = page.getByRole("img", { name: "quin.png" });
    await expect(image).toBeVisible();
    expect(await image.evaluate(img => img.naturalWidth)).toBe(1);
    pia.conn.close();
    quin.conn.close();
});

test("safety number, mute, privacy and retrying a message that was not sent", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    // The next DM send can be refused by the "server" to produce a failed message.
    let refuseNext = false;
    let refusedID = "";
    await page.routeWebSocket(/\/ws$/, ws => {
        const upstream = ws.connectToServer();
        ws.onMessage(message => {
            const bytes = Buffer.isBuffer(message) ? message : Buffer.from(message);
            if (refuseNext && bytes.length > 6 && bytes.readUInt16BE(4) === Msg.ChatSend) {
                const request = JSON.parse(bytes.subarray(6).toString());
                if (request.to_unique_id && !request.dm_action) {
                    refuseNext = false;
                    refusedID = request.client_msg_id;
                    const payload = Buffer.from(JSON.stringify({ code: "rate_limited", message: "slow down", origin_type: Msg.ChatSend }));
                    const frame = Buffer.alloc(6 + payload.length);
                    frame.writeUInt32BE(payload.length + 2, 0);
                    frame.writeUInt16BE(Msg.Error, 4);
                    payload.copy(frame, 6);
                    ws.send(frame);
                    return;
                }
            }
            upstream.send(message);
        });
    });
    const { desktop: sol } = await phoneAccount(page, server, server.accounts.sol, info.version, expect);
    const tao = await contactDesktop(server, server.accounts.tao, info.version);
    const composer = await openDMWith(page, tao.uniqueID);

    // Safety number: both fingerprints, then marked verified.
    await page.getByRole("button", { name: "Conversation options" }).click();
    await page.getByRole("menuitem", { name: "Safety number" }).click();
    const dialog = page.getByRole("dialog", { name: "Safety number" });
    const pem = key => `-----BEGIN PUBLIC KEY-----\n${toBase64(key)}\n-----END PUBLIC KEY-----\n`;
    await expect(dialog).toContainText(keyFingerprint(pem(tao.identity.edPublic)));
    await expect(dialog).toContainText(keyFingerprint(pem(sol.identity.edPublic)));
    await dialog.getByRole("switch", { name: /Mark as verified/ }).check();
    await dialog.getByRole("button", { name: "Close" }).click();
    await expect(page.getByRole("note")).toContainText("Verified");
    await page.reload();
    await expect(page.getByRole("note")).toContainText("Verified", { timeout: 15000 });

    // A refused message offers Retry, which resends it with the same client message ID.
    refuseNext = true;
    await composer.fill("second try");
    await composer.press("Enter");
    const failed = page.locator(".msg.failed");
    await expect(failed).toContainText("Not sent — tap to retry");
    const resent = directFrom(tao, sol.uniqueID);
    await failed.click();
    await page.getByRole("dialog").getByRole("button", { name: "Retry", exact: true }).click();
    const retried = await resent;
    expect(retried.client_msg_id).toBe(refusedID);
    await expect(page.locator(".msg.failed")).toHaveCount(0);
    await expect(page.locator(".msg.own").last().getByRole("img", { name: "Sent" })).toBeVisible();

    // A refused message can also be deleted from this phone.
    refuseNext = true;
    await composer.fill("never mind");
    await composer.press("Enter");
    await page.locator(".msg.failed").click();
    await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
    await expect(page.getByRole("log")).not.toContainText("never mind");

    // Muted: a new message neither counts as unread nor badges the row.
    await page.getByRole("button", { name: "Conversation options" }).click();
    await page.getByRole("menuitem", { name: "Mute conversation" }).click();
    await page.evaluate(() => { location.hash = "#/home"; });
    await sendDM(tao, sol.uniqueID, "while muted");
    const row = page.getByRole("link", { name: new RegExp(server.accounts.tao.nickname) });
    await expect(row).toContainText("while muted");
    await expect(row.getByRole("img", { name: "Muted" })).toBeVisible();
    await expect(row.getByLabel(/unread/)).toHaveCount(0);

    // Privacy: with receipts and typing off, the phone sends neither.
    await page.getByRole("link", { name: "You", exact: true }).click();
    await page.getByRole("switch", { name: /Send read receipts and typing indicators/ }).uncheck();
    await openDMWith(page, tao.uniqueID);
    const receipt = eventWithin(tao, "dm_delivered", data => data.client_msg_id === "tao-private", 2000);
    const typing = eventWithin(tao, "typing", data => data.direct, 2000);
    await sendDM(tao, sol.uniqueID, "do you read this?", "tao-private");
    await expect(page.getByRole("log")).toContainText("do you read this?");
    await page.getByRole("textbox", { name: /Message/ }).pressSequentially("typing quietly");
    expect(await receipt).toBe(false);
    expect(await typing).toBe(false);
    sol.conn.close();
    tao.conn.close();
});
