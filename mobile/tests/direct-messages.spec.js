import { expect, test } from "@playwright/test";

import { fromBase64, openDM, sealDM } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, publishKeyRecord, registerDesktop } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

function directory(desktop, uid) {
    return desktop.conn.request(Msg.KeyRequest, { unique_id: uid, devices: true }, Msg.KeyResponse, { accept: r => r.unique_id === uid });
}

function nextDirect(desktop, match) {
    return new Promise(resolve => {
        const stop = desktop.conn.on(Msg.Event, event => {
            if (event.type === "chat" && event.data?.direct && match(event.data)) {
                stop();
                resolve(event.data);
            }
        });
    });
}

test("a direct message reaches the desktop and the phone, and the phone replies", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    // Rowan: desktop (the account's first device) plus a linked phone.
    const rowan = await desktopSession(server.controlAddr, server.accounts.rowan, { version: info.version });
    const rowanDevice = await registerDesktop(rowan);
    await publishKeyRecord(rowan);
    const linked = await linkPhone(page, rowan, server.webURL, expect);

    // Rowan's desktop copies an older conversation to the new phone.
    const list = await rowan.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const phoneDevice = list.devices.find(d => d.kind === "web" && d.fingerprint && d.x25519_public_key === linked.x25519_public_key);
    const history = [{ peer: "uid-sam", peer_nickname: "Sam", from: "uid-sam", from_nickname: "Sam", body: "an old message from Sam", sent_at: 1700000000, client_msg_id: "old-1" }];
    const sealed = fromBase64(sealDM(JSON.stringify(history), fromBase64(phoneDevice.x25519_public_key), rowan.identity.xSecret));
    await rowan.conn.request(Msg.DeviceRequest, { action: "history_put", device_id: phoneDevice.device_id, history: { seq: 0, total: 1, data: btoa(String.fromCharCode(...sealed)) } },
        Msg.DeviceResult, { accept: r => r.action === "history_put" });
    await expect(page.getByRole("link", { name: /Sam/ })).toContainText("an old message from Sam", { timeout: 15000 });

    // Ivy, on her desktop, writes to Rowan: one copy per device.
    const ivy = await desktopSession(server.controlAddr, server.accounts.ivy, { version: info.version });
    const ivyDevice = await registerDesktop(ivy);
    await publishKeyRecord(ivy);
    const rowanDevices = (await directory(ivy, rowan.uniqueID)).devices;
    expect(rowanDevices.map(d => d.kind).sort()).toEqual(["desktop", "web"]);
    const copies = [...rowanDevices, { device_id: ivyDevice, x25519_public_key: btoa(String.fromCharCode(...ivy.identity.xPublic)) }]
        .map(d => ({ device_id: d.device_id, text: sealDM("hi Rowan, it's Ivy", fromBase64(d.x25519_public_key), ivy.identity.xSecret) }));
    const onDesktop = nextDirect(rowan, data => data.from_unique_id === ivy.uniqueID);
    await ivy.conn.request(Msg.ChatSend, { to_unique_id: rowan.uniqueID, enc: true, ack_requested: true, client_msg_id: "ivy-1", copies },
        154, { accept: r => r.client_msg_id === "ivy-1" });
    const desktopCopy = await onDesktop;
    expect(desktopCopy.recipient_device_id).toBe(rowanDevice);
    expect(openDM(desktopCopy.text, ivy.identity.xPublic, rowan.identity.xSecret)).toBe("hi Rowan, it's Ivy");

    // The phone lists the conversation as unread and opens it.
    const row = page.getByRole("link", { name: new RegExp(server.accounts.ivy.nickname) });
    await expect(row).toContainText("hi Rowan, it's Ivy");
    await expect(row.getByLabel("1 unread")).toBeVisible();
    await row.click();
    await expect(page.getByRole("log")).toContainText("hi Rowan, it's Ivy");
    await expect(page.getByRole("note")).toContainText("End-to-end encrypted");

    // The phone's reply reaches Ivy and Rowan's own desktop.
    const toIvy = nextDirect(ivy, data => data.from_unique_id === rowan.uniqueID);
    const toOwnDesktop = nextDirect(rowan, data => data.from_unique_id === rowan.uniqueID);
    const composer = page.getByRole("textbox", { name: /Message/ });
    await composer.fill("hey Ivy, from my phone");
    await composer.press("Enter");
    const reply = await toIvy;
    const phoneKey = fromBase64(reply.sender_device_key);
    expect(reply.sender_device_id).not.toBe(rowanDevice);
    expect(openDM(reply.text, phoneKey, ivy.identity.xSecret)).toBe("hey Ivy, from my phone");
    const synced = await toOwnDesktop;
    expect(synced.to_unique_id).toBe(ivy.uniqueID);
    expect(openDM(synced.text, phoneKey, rowan.identity.xSecret)).toBe("hey Ivy, from my phone");
    await expect(page.locator(".msg.own").last()).not.toHaveClass(/pending|failed/);

    // History survives a reload (encrypted in IndexedDB).
    await page.waitForTimeout(500);
    await page.reload();
    await expect(page.getByRole("log")).toContainText("hey Ivy, from my phone", { timeout: 15000 });

    rowan.conn.close();
    ivy.conn.close();
});
