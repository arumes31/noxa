// ratchet-projection.js — pure view helpers for authoritative noxa-olm-v1
// records (Go: client/ratchet_session.go "ratchet_message" events and
// RatchetConversationHistoryForTab). Three identities stay distinct:
//   protocol/provenance  record.protocol
//   semantic reference   (sender, logical_id) -> record.key
//   transport            record.transport.{message_id, attempt_id, delivery_id}
// Opaque transport/logical IDs are never coerced into legacy numeric IDs.

export const RATCHET_PROTOCOL = "noxa-olm-v1";

const text = value => (typeof value === "string" ? value : "");
const bodyText = record => text(record?.body?.text);

/** Legacy numeric server message ID, or 0 for any new-protocol record. */
export function legacyID(message) {
    const id = message?.id;
    return typeof id === "number" && Number.isSafeInteger(id) && id > 0 ? id : 0;
}

/** Same DM message: protocol + sender + client/logical ID (collision-free). */
export function sameDMMessage(a, b) {
    return !!a && !!b && (a.protocol || "") === (b.protocol || "") && (a.fromUID || "") === (b.fromUID || "") && !!a.clientMsgID && a.clientMsgID === b.clientMsgID;
}

/** Status flags of an own new-protocol send. */
function sendState(record) {
    const status = text(record?.status);
    return { pending: !!record?.self && status === "pending", failed: !!record?.self && (status === "rejected" || status === "aborted") };
}

/**
 * The chat-ui DM message for one direct chat record. It is never a legacy
 * numeric message (id 0); the semantic key identifies it.
 */
export function ratchetDMMessage(record, nickname = "") {
    const sentAt = Number(record?.sent_at) || Date.now() / 1000;
    return {
        id: 0,
        localSeq: 0,
        protocol: RATCHET_PROTOCOL,
        ratchetKey: text(record?.key),
        from: nickname || text(record?.sender) || "?",
        fromUID: text(record?.sender),
        text: bodyText(record),
        ts: Math.round(sentAt * 1000),
        edited: false,
        deleted: false,
        reactions: null,
        mentions: [],
        e2e: false,
        direct: true,
        enc: true,
        encVerified: true,
        encMode: "e2e",
        offline: false,
        clientMsgID: text(record?.logical_id),
        channelID: null,
        self: !!record?.self,
        copied: !!record?.copied,
        mentioned: false,
        ...sendState(record),
    };
}

/** Whether a projection may count as unread/notify: durable new receive only. */
export function ratchetIsIncoming(record) {
    return !!record?.is_new && !record.self && !record.copied && record.purpose === "chat";
}

/**
 * Builds the group view rows of new-protocol records: one row per sender
 * logical ID (newest status wins), with edits/deletes applied only when they
 * reference the SAME sender's logical ID. Row IDs are "olm:" + semantic key.
 */
export function groupRatchetRows(records = []) {
    const rows = new Map();
    const changes = [];
    for (const record of records) {
        if (record?.protocol !== RATCHET_PROTOCOL || record.scope !== "group") continue;
        if (record.purpose === "edit" || record.purpose === "delete") { changes.push(record); continue; }
        const row = rows.get(record.key) || {};
        rows.set(record.key, {
            ...row,
            id: "olm:" + record.key, key: record.key, protocol: RATCHET_PROTOCOL,
            conversation_id: text(record.conversation), from_unique_id: text(record.sender), reference: text(record.logical_id),
            body: bodyText(record), created_at: Number(record.sent_at) || 0, self: !!record.self,
            status: text(record.status), edited: !!row.edited, deleted: !!row.deleted, ...sendState(record),
        });
    }
    for (const change of changes) {
        const row = rows.get(change.reference_key);
        if (!row || row.from_unique_id !== change.sender || row.deleted) continue;
        if (change.purpose === "delete") Object.assign(row, { deleted: true, body: "" });
        else if (bodyText(change)) Object.assign(row, { edited: true, body: bodyText(change) });
    }
    return [...rows.values()].sort((a, b) => a.created_at - b.created_at);
}

/**
 * Merges legacy numeric history with new-protocol rows. Legacy messages keep
 * their numeric order (the server's navigation order); each new row is
 * inserted before the first legacy message sent after it, new rows among
 * themselves by time. No numeric ID is ever derived for a new row.
 */
export function mergeGroupMessages(legacy = [], rows = []) {
    const out = [...legacy].sort((a, b) => legacyID(a) - legacyID(b));
    for (const row of [...rows].sort((a, b) => (Number(a.created_at) || 0) - (Number(b.created_at) || 0))) {
        const at = Number(row.created_at) || 0;
        const index = out.findIndex(m => legacyID(m) && (Number(m.created_at) || 0) > at);
        if (index < 0) out.push(row); else out.splice(index, 0, row);
    }
    return out;
}

/** Locked placeholder rows for quarantined copies of one conversation. */
export function quarantineRows(placeholders = [], { scope, peer = "", conversation = "" }) {
    return placeholders.filter(q => q?.scope === scope && (scope === "direct" ? q.peer === peer : q.conversation === conversation))
        .map(q => ({ id: "quarantine:" + text(q.id), protocol: RATCHET_PROTOCOL, fromUID: text(q.sender), code: text(q.code), ts: Math.round((Number(q.seen_at) || 0) * 1000) }));
}
