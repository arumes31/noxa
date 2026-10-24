import { expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { linkClaimTranscript, linkSAS, toBase64, toBase64URL, utf8 } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession, sign } from "./harness.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

function nextDeviceEvent(conn, kind) {
    return new Promise(resolve => {
        const stop = conn.on(Msg.DeviceEvent, event => {
            if (event.event === kind) {
                stop();
                resolve(event);
            }
        });
    });
}

async function approve(desktop, event) {
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const self = list.devices.find(d => d.current);
    const transcript = utf8(`noxa-device-approval-v1\n${desktop.uniqueID}\n${self.device_id}\n${event.ed25519_public_key}\n${event.x25519_public_key}`);
    return desktop.conn.request(Msg.DeviceRequest, {
        action: "link_decide", link_id: event.link_id, approve: true, signature: toBase64(sign(transcript, desktop.identity.edSecret)),
    }, Msg.DeviceResult, { accept: r => r.action === "link_decide" });
}

test("a phone links by QR, compares the code, signs in and chats with the desktop", async ({ page }) => {
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    const deskChat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const link = await desktop.conn.request(Msg.DeviceRequest, { action: "link_create" }, Msg.DeviceResult, { accept: r => r.action === "link_create" });
    const secret = Uint8Array.from(Buffer.from(link.secret, "base64"));
    const claimed = nextDeviceEvent(desktop.conn, "link_claimed");

    page.on("pageerror", error => console.log("pageerror:", error.message));
    await page.goto(`${server.webURL}/link#id=${link.link_id}&k=${toBase64URL(secret)}`);
    const event = await claimed;
    expect(event.platform).toContain("Android");
    const desktopSAS = linkSAS(secret, linkClaimTranscript(event.link_id, event.ed25519_public_key, event.x25519_public_key));
    await expect(page.locator(".sas")).toHaveText(desktopSAS);
    await expect(page.getByText(/Desktop fingerprint: [0-9a-f]{4} /)).toBeVisible();
    expect(page.url()).not.toContain(link.link_id);

    const decided = await approve(desktop, event);
    expect(decided.state).toBe("approved");

    // Signed in: the passkey offer appears first.
    // Two "Not now" buttons can show: the passkey offer (a dialog) and the install banner.
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 15000 }).catch(() => {});
    await page.locator("#install-banner").getByRole("button", { name: "Not now" }).click({ timeout: 2000 }).catch(() => {});
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });

    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("link", { name: "Server chat" }).click();
    const composer = page.getByRole("textbox", { name: /Message/ });
    await composer.fill("hello from the phone");
    const fromPhone = new Promise(resolve => deskChat.addEventListener("messages", () => {
        const found = (deskChat.messages.get(0) || []).find(m => m.text === "hello from the phone");
        if (found) resolve(found);
    }));
    await composer.press("Enter");
    const received = await fromPhone;
    expect(received.from.replace(/#M\d+$/, "")).toBe(server.accounts.owner.nickname);

    await deskChat.send(0, "hi phone, desktop here");
    await expect(page.getByRole("log")).toContainText("hi phone, desktop here");

    // Channel chat works through subscription, without joining voice.
    await page.getByRole("link", { name: "Back" }).click();
    await page.getByRole("link", { name: "Public1" }).click();
    await deskChat.subscribe([2]);
    await deskChat.send(2, "channel hello");
    await expect(page.getByRole("log", { name: "# Public1" })).toContainText("channel hello");

    // The phone's device is listed, and revoking it signs the phone out.
    await page.getByRole("link", { name: "Back" }).click();
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByText("This device", { exact: false }).first()).toBeVisible();
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    // Other tests link phones to the same account; this one is the newest.
    const phone = list.devices.filter(d => d.kind === "web").sort((a, b) => b.created_at - a.created_at)[0];
    await desktop.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: phone.device_id }, Msg.DeviceResult, { accept: r => r.action === "revoke" });
    await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeVisible({ timeout: 15000 });
    deskChat.close();
});

test("a typed code works and a denial returns to the connect screen", async ({ page }) => {
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const link = await desktop.conn.request(Msg.DeviceRequest, { action: "link_create" }, Msg.DeviceResult, { accept: r => r.action === "link_create" });
    const claimed = nextDeviceEvent(desktop.conn, "link_claimed");

    await page.goto(`${server.webURL}/app/`);
    await page.getByRole("textbox", { name: "Code from your desktop" }).fill(link.code.toLowerCase());
    await page.getByRole("button", { name: "Continue" }).click();
    const event = await claimed;
    expect(event.via).toBe("code");
    const sas = linkSAS(utf8(link.code.replace("-", "")), linkClaimTranscript(event.link_id, event.ed25519_public_key, event.x25519_public_key));
    await expect(page.locator(".sas")).toHaveText(sas);
    await desktop.conn.request(Msg.DeviceRequest, { action: "link_decide", link_id: event.link_id }, Msg.DeviceResult, { accept: r => r.action === "link_decide" });
    await expect(page.getByText("Your desktop denied this sign-in.")).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "Continue" })).toBeVisible();
    desktop.conn.close();
});
