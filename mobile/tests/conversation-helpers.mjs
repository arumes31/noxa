// conversation-helpers.mjs — desktop-side steps of the DM and group specs:
// a contact desktop that writes to a linked phone's account, listens for
// events, and seals group messages like client/conversation.go.
import { fromBase64, sealDM } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers, publishKeyRecord, registerDesktop } from "./helpers.mjs";

/** Lets members see each other (typing and receipts need a visible sender). */
export async function openServer(server, version) {
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version });
    await openServerToMembers(owner);
    // Sign the helper session out again so it does not use up one of the owner's device slots.
    await owner.conn.request(Msg.DeviceRequest, { action: "sign_out" }, Msg.DeviceResult, { accept: r => r.action === "sign_out" }).catch(() => {});
    owner.conn.close();
}

/** A registered desktop with a published key record. */
export async function contactDesktop(server, account, version) {
    const desktop = await desktopSession(server.controlAddr, account, { version });
    await registerDesktop(desktop);
    await publishKeyRecord(desktop);
    return desktop;
}

/** The account's desktop plus the page linked as its phone. */
export async function phoneAccount(page, server, account, version, expect) {
    const desktop = await contactDesktop(server, account, version);
    const linked = await linkPhone(page, desktop, server.webURL, expect);
    return { desktop, linked };
}

export function directory(desktop, uid) {
    return desktop.conn.request(Msg.KeyRequest, { unique_id: uid, devices: true }, Msg.KeyResponse, { accept: r => r.unique_id === uid });
}

/** Resolves with the next server event of type whose data matches. */
export function nextEvent(desktop, type, match = () => true) {
    return new Promise(resolve => {
        const stop = desktop.conn.on(Msg.Event, event => {
            if (event.type === type && match(event.data || {})) {
                stop();
                resolve(event.data);
            }
        });
    });
}

/** Whether an event of type arrives within ms (for "nothing was sent" checks). */
export function eventWithin(desktop, type, match, ms) {
    return Promise.race([nextEvent(desktop, type, match).then(() => true), new Promise(resolve => setTimeout(() => resolve(false), ms))]);
}

export const directFrom = (desktop, uid) => nextEvent(desktop, "chat", data => data.direct && data.from_unique_id === uid);

/** Sends a DM from a contact desktop: one copy per device of both accounts. */
export async function sendDM(desktop, toUID, text, id = crypto.randomUUID(), extra = {}) {
    const devices = [...(await directory(desktop, toUID)).devices, ...(await directory(desktop, desktop.uniqueID)).devices];
    const copies = devices.map(d => ({ device_id: d.device_id, text: sealDM(text, fromBase64(d.x25519_public_key), desktop.identity.xSecret) }));
    await desktop.conn.request(Msg.ChatSend, { to_unique_id: toUID, enc: true, ack_requested: true, client_msg_id: id, copies, ...extra },
        Msg.ChatAccepted, { accept: r => r.client_msg_id === id });
    return id;
}

export const group = (desktop, request) => desktop.conn.request(Msg.ConversationRequest, request, Msg.ConversationResult, { accept: r => r.action === request.action });

/** Sends a group message from a desktop: account envelopes plus device envelopes for every web device. */
export async function sendGroup(desktop, groupID, text, reference = crypto.randomUUID()) {
    const conversation = (await group(desktop, { action: "get", id: groupID })).conversations[0];
    const envelopes = {};
    const deviceEnvelopes = {};
    for (const member of conversation.members.filter(m => !m.pending)) {
        const devices = (await directory(desktop, member.unique_id)).devices;
        const first = devices.find(d => d.first_device) || devices[0];
        const plain = JSON.stringify({ conversation_id: groupID, epoch: conversation.epoch, reference, from: desktop.uniqueID, to: member.unique_id, text });
        envelopes[member.unique_id] = sealDM(plain, fromBase64(first.x25519_public_key), desktop.identity.xSecret);
        for (const device of devices) {
            if (device === first) continue;
            deviceEnvelopes[`${member.unique_id}/${device.device_id}`] = sealDM(plain, fromBase64(device.x25519_public_key), desktop.identity.xSecret);
        }
    }
    const result = await group(desktop, { action: "send", id: groupID, revision: conversation.revision,
        message: { epoch: conversation.epoch, reference, envelopes, device_envelopes: deviceEnvelopes } });
    return result.message_id;
}

/** A nickname as the phone shows it, without the mobile session suffix. */
export const plainName = name => String(name || "").replace(/#M\d+$/, "");
