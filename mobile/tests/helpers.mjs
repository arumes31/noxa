// helpers.mjs — shared steps of the end-to-end specs: linking a phone to a
// desktop session and opening the server's chat to members.
import { linkClaimTranscript, linkSAS, toBase64, toBase64URL, utf8 } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { sign } from "./harness.mjs";

const MsgRoleQuery = 133;
const MsgRoleState = 134;
const MsgRoleChange = 135;
const MsgRoleChangeResult = 136;

export function nextDeviceEvent(conn, kind, match = () => true) {
    return new Promise(resolve => {
        const stop = conn.on(Msg.DeviceEvent, event => {
            if (event.event === kind && match(event)) {
                stop();
                resolve(event);
            }
        });
    });
}

export async function approve(desktop, event) {
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const self = list.devices.find(d => d.current);
    const transcript = utf8(`noxa-device-approval-v1\n${desktop.uniqueID}\n${self.device_id}\n${event.ed25519_public_key}\n${event.x25519_public_key}`);
    return desktop.conn.request(Msg.DeviceRequest, {
        action: "link_decide", link_id: event.link_id, approve: true, signature: toBase64(sign(transcript, desktop.identity.edSecret)),
    }, Msg.DeviceResult, { accept: r => r.action === "link_decide" });
}

/** Links the page as a phone of the desktop's account and waits for Home. */
export async function linkPhone(page, desktop, webURL, expect) {
    await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const link = await desktop.conn.request(Msg.DeviceRequest, { action: "link_create" }, Msg.DeviceResult, { accept: r => r.action === "link_create" });
    const secret = Uint8Array.from(Buffer.from(link.secret, "base64"));
    const claimed = nextDeviceEvent(desktop.conn, "link_claimed");
    await page.goto(`${webURL}/link#id=${link.link_id}&k=${toBase64URL(secret)}`);
    const event = await claimed;
    const sas = linkSAS(secret, linkClaimTranscript(event.link_id, event.ed25519_public_key, event.x25519_public_key));
    await expect(page.locator(".sas")).toHaveText(sas);
    await approve(desktop, event);
    // Two "Not now" buttons can show: the passkey offer (a dialog) and the install banner.
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 15000 }).catch(() => {});
    await page.locator("#install-banner").getByRole("button", { name: "Not now" }).click({ timeout: 2000 }).catch(() => {});
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });
    return event;
}

/** A fresh roles-v1 server is closed; the owner lets @everyone chat and talk. */
export async function openServerToMembers(owner) {
    const state = await owner.conn.request(MsgRoleQuery, { channel_id: 0 }, MsgRoleState);
    const policy = state.policy;
    const everyone = policy.roles.find(r => r.id === policy.everyone_id);
    const wanted = ["view_channel", "read_history", "send_messages", "connect", "speak", "poke_members", "share_camera", "upload_files", "download_files"];
    const permissions = [...new Set([...(everyone.permissions || []), ...wanted])];
    if (permissions.length === (everyone.permissions || []).length) return;
    await owner.conn.request(MsgRoleChange, {
        kind: "role_update", expected_revision: policy.revision, role: { ...everyone, permissions },
    }, MsgRoleChangeResult);
}

/** Signs and publishes a noxa-dm-key-v1 record like the desktop client. */
export async function publishKeyRecord(desktop) {
    const { toBase64: b64, utf8: bytes, sign: signBytes } = await import("../src/core/crypto.js");
    const payload = `{"UID":${JSON.stringify(desktop.uniqueID)},"SigningKey":"${b64(desktop.identity.edPublic)}","EncryptionKey":"${b64(desktop.identity.xPublic)}"}`;
    const signature = signBytes(new Uint8Array([...bytes("noxa/dm-identity/v1\0"), ...bytes(payload)]), desktop.identity.edSecret);
    const record = JSON.stringify({ uid: desktop.uniqueID, signing_key: b64(desktop.identity.edPublic), encryption_key: b64(desktop.identity.xPublic), signature: b64(signature) });
    desktop.conn.send(Msg.KeyPublish, { public_key: "noxa-dm-key-v1:" + b64(bytes(record)) });
}

/** Registers the desktop session as its account's first device. */
export async function registerDesktop(desktop) {
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    desktop.deviceID = list.devices.find(d => d.current)?.device_id;
    return desktop.deviceID;
}
