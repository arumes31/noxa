// devices.js — managing this account's devices from the phone: listing,
// renaming, confirming a passkey-linked device for direct messages (endorse)
// and copying this phone's direct-message history to another device.
//
// Confirming signs DeviceApprovalTranscript with this device's Ed25519 key;
// the server accepts it only from a device that is itself confirmed
// (internal/server/dm_devices.go handleEndorse). The history copy mirrors
// client/dm_history_copy.go: messages newest first, packed into chunks of at
// most MaxHistoryChunk sealed bytes, each chunk a JSON array (oldest first)
// sealed as nacl/box nonce||box from this device to the target device.
import { keyFingerprint, sealDM, sign, utf8 } from "./crypto.js";
import { Msg } from "./frame.js";

// internal/netproto/dm_devices.go
export const MAX_HISTORY_CHUNK = 256 << 10;
export const MAX_HISTORY_CHUNKS = 256;
export const MAX_HISTORY_BYTES = 50 << 20;
export const MAX_HISTORY_MESSAGES = 10000;
const SEAL_OVERHEAD = 24 + 16;

function deviceRequest(conn, request, timeout = 10000) {
    return conn.request(Msg.DeviceRequest, request, Msg.DeviceResult, { accept: r => r.action === request.action, timeout });
}

export async function listDevices(conn) {
    const result = await deviceRequest(conn, { action: "list" });
    return { devices: result.devices || [], passkeys: result.passkeys || [] };
}

/** Renames any device of the account (rename: device_id, name). */
export async function renameDevice(conn, deviceID, name) {
    const trimmed = String(name || "").trim();
    if (!trimmed) throw new Error("name required");
    const result = await deviceRequest(conn, { action: "rename", device_id: deviceID, name: trimmed.slice(0, 64) });
    return result.device;
}

/** Whether a listed device waits for a confirmation this device could give. */
export function needsConfirmation(device) {
    return !!device && !device.current && !device.first_device && !device.confirmed && !device.revoked_at;
}

/** DeviceApprovalTranscript (internal/netproto/devices.go). */
export function approvalTranscript(principal, approverDeviceID, ed25519PEM, x25519B64) {
    return utf8(`noxa-device-approval-v1\n${principal}\n${approverDeviceID}\n${ed25519PEM}\n${x25519B64}`);
}

/**
 * Confirms another device of the account for direct messages. The target's
 * keys come from the server's list and must match its fingerprint, which
 * the user compares with the one the other device shows.
 */
export async function endorseDevice(session, target) {
    if (!session.deviceID) throw new Error("this device is not registered yet");
    if (!target || target.device_id === session.deviceID || target.first_device || target.revoked_at) throw new Error("this device can no longer be confirmed");
    if (!target.ed25519_public_key || !target.x25519_public_key) throw new Error("the server does not share this device's keys");
    if (keyFingerprint(target.ed25519_public_key) !== target.fingerprint) throw new Error("the device's key does not match its fingerprint");
    const signature = sign(approvalTranscript(session.uniqueID, session.deviceID, target.ed25519_public_key, target.x25519_public_key), session.identity.edSecret);
    let binary = "";
    for (const byte of signature) binary += String.fromCharCode(byte);
    const result = await deviceRequest(session.conn, { action: "endorse", device_id: target.device_id, signature: btoa(binary) });
    return result.device;
}

/**
 * Collects stored direct messages sent at or after since (seconds; 0 for
 * everything), newest first, capped at MAX_HISTORY_MESSAGES.
 * conversations: iterable of {peer, nickname, messages[]} (core/direct.js).
 */
export function historyMessagesSince(conversations, since = 0) {
    const out = [];
    for (const conversation of conversations) {
        for (const m of conversation.messages || []) {
            if (typeof m.body !== "string" || !m.body || m.deleted || m.failed || m.queued || m.pending || m.verified === false) continue;
            const sentAt = Math.floor(Number(m.sentAt) || 0);
            if (sentAt < since) continue;
            out.push({
                peer: conversation.peer, peer_nickname: conversation.nickname || undefined, from: m.from || "", from_nickname: m.fromNickname || undefined,
                body: m.body, sent_at: sentAt, self: !!m.self || undefined, client_msg_id: m.id || undefined,
            });
        }
    }
    out.sort((a, b) => b.sent_at - a.sent_at);
    return out.slice(0, MAX_HISTORY_MESSAGES);
}

/**
 * Packs messages (newest first) into sealed chunks, oldest chunk first, each
 * holding its messages oldest first. Returns {chunks: [base64 nonce||box],
 * kept}. Limits as in client/dm_history_copy.go buildHistoryChunks.
 */
export function buildHistoryChunks(messages, recipientX25519, senderXSecret, { maxChunk = MAX_HISTORY_CHUNK } = {}) {
    const maxPlain = maxChunk - SEAL_OVERHEAD;
    const encoder = new TextEncoder();
    const groups = [];
    let current = [];
    let size = 2;
    let total = 0;
    let kept = 0;
    const flush = () => {
        if (current.length) {
            groups.push(current);
            total += size + SEAL_OVERHEAD;
        }
        current = [];
        size = 2;
    };
    for (const message of messages) {
        const raw = JSON.stringify(message);
        const length = encoder.encode(raw).length;
        if (length + 2 > maxPlain) continue;
        let add = length + (current.length ? 1 : 0);
        if (size + add > maxPlain) {
            flush();
            add = length;
        }
        if (!current.length && groups.length >= MAX_HISTORY_CHUNKS) break;
        if (total + size + add + SEAL_OVERHEAD > MAX_HISTORY_BYTES) break;
        current.push(raw);
        size += add;
        kept++;
    }
    flush();
    const chunks = [];
    for (let i = groups.length - 1; i >= 0; i--) {
        const plain = `[${[...groups[i]].reverse().join(",")}]`;
        chunks.push(sealDM(plain, recipientX25519, senderXSecret));
    }
    return { chunks, kept };
}

/**
 * Copies this phone's direct-message history of the last days days (-1:
 * everything) to another confirmed device of the account. The target's key
 * comes from the verified device directory, never from the caller.
 * onProgress({sent, total, messages}).
 */
export async function copyHistory(session, dm, deviceID, days, onProgress = () => {}) {
    if (!deviceID || deviceID === session.deviceID) throw new Error("choose another device");
    await dm.ready;
    const own = await dm.devicesOf(session.uniqueID, { refresh: true });
    if (!own.devices.some(d => d.deviceID === session.deviceID)) throw new Error("confirm this device before copying history");
    const target = own.devices.find(d => d.deviceID === deviceID);
    if (!target) throw new Error("this device is not confirmed for direct messages");
    const since = days > 0 ? Math.floor(Date.now() / 1000) - days * 86400 : 0;
    const { chunks, kept } = buildHistoryChunks(historyMessagesSince(dm.conversations.values(), since), target.x25519, session.identity.xSecret);
    for (let seq = 0; seq < chunks.length; seq++) {
        await deviceRequest(session.conn, { action: "history_put", device_id: deviceID, history: { seq, total: chunks.length, data: chunks[seq] } }, 30000);
        onProgress({ sent: seq + 1, total: chunks.length, messages: kept });
    }
    return { total: chunks.length, messages: kept };
}
