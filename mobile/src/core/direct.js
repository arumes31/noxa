// direct.js — end-to-end encrypted direct messages on this device.
//
// Each message is sealed once per trusted device of the recipient and of
// this account (dm_devices_v1, internal/netproto/dm_devices.go). Copies for
// this device arrive live or from the server's per-device spool. History
// is kept in IndexedDB, encrypted with the database key.
import { Stores, currentFence, deleteKeyIn, getSealed, keys, putSealedIn } from "./db.js";
import { keyFingerprint, openDM, sealDM, toBase64 } from "./crypto.js";
import { ed25519FromPEM, parseKeyRecord, trustedDevices } from "./dm-trust.js";
import { Msg } from "./frame.js";
import { ProtocolError } from "./connection.js";
import { RATCHET_PROTOCOL } from "./ratchet-consumer.js";
import { decodeHistoryEnvelope, isHistoryEnvelope } from "./history-envelope.js";
import { verifyLinkBinding } from "./link-binding.js";

const MsgChatAccepted = 154;
/** Durable record of imported history chunks (exact dedup), in Stores.device. */
const HISTORY_IMPORTS = "history-imports";
const MAX_HISTORY_DIGESTS = 4096;
/** Completed transfer IDs remembered (a replay of a completed transfer imports nothing). */
const MAX_HISTORY_TRANSFERS = 1024;
const HISTORY_PROVENANCE = new Set(["authenticated", "transferred"]);

async function chunkDigest(data) {
    const bytes = new TextEncoder().encode(data);
    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    return Array.from(hash, b => b.toString(16).padStart(2, "0")).join("");
}

/**
 * One imported history message as a view record, or null. A new-protocol
 * record must carry its sender's logical ID and a known provenance; it is
 * marked transferred, with origin (its provenance on the source) kept apart
 * from source (the device that sent the copy).
 */
export function historyRecord(m, ownUID, sourceDevice, n = 0) {
    if (!m || typeof m !== "object" || typeof m.peer !== "string" || !m.peer || typeof m.body !== "string" || typeof m.from !== "string") return null;
    // Older exporters left a legacy record's sender empty: an own record is
    // from this account, any other is from the conversation's peer.
    const from = m.from === "" && !m.protocol ? (m.self ? ownUID : m.peer) : m.from;
    // A DM record is from its conversation's peer or from this account (and
    // "self" says which), and never future-dated (a burst could otherwise
    // evict real records under the per-conversation cap).
    if (from !== m.peer && from !== ownUID) return null;
    if (m.peer !== ownUID && !!m.self !== (from === ownUID)) return null;
    const sentAt = Number(m.sent_at);
    if (!Number.isFinite(sentAt) || sentAt <= 0 || sentAt > Date.now() / 1000 + 300) return null;
    const base = { from, fromNickname: typeof m.from_nickname === "string" ? m.from_nickname : "", self: from === ownUID, body: m.body, sentAt, copied: true };
    if (!m.protocol) return { ...base, id: m.client_msg_id || `history-${m.peer}-${m.sent_at}-${n}`, verified: true };
    if (m.protocol !== RATCHET_PROTOCOL || !HISTORY_PROVENANCE.has(m.provenance) || typeof m.client_msg_id !== "string" || !m.client_msg_id ||
        m.client_msg_id.length > 512 || !m.from || !m.body || (m.self && m.from !== ownUID)) return null;
    return { ...base, id: m.client_msg_id, protocol: RATCHET_PROTOCOL, self: m.from === ownUID, verified: true,
        provenance: "transferred", origin: { provenance: m.provenance }, source: { device: String(sourceDevice || "") } };
}
/** Ratchet conditions an unchanged retry (outbox) can still resolve. */
export const RETRYABLE_RATCHET = new Set(["uncertain", "rate-limit", "unavailable", "local_conflict", "fenced", "aborted"]);
/** Failures that stay failed even for a committed attempt (no reseal loop). */
export const TERMINAL_RATCHET = new Set(["clock_skew"]);

/**
 * Whether a failed ratchet send should stay queued. A committed attempt without
 * a terminal receipt is uncertain and must be resolved exactly; an aborted
 * attempt was never delivered and may be resealed with its original content.
 * Every other coded refusal before commit (authority, limit, exhausted,
 * account change, conflict, invalid...) fails only this message.
 */
export function ratchetRetryable(error) {
    const committed = error?.committed;
    if (TERMINAL_RATCHET.has(error?.code)) return false;
    if (committed && !committed.receipt) return true;
    if (committed?.receipt) return committed.receipt.state === "aborted";
    if (!error?.code) return !(error instanceof ProtocolError);
    return RETRYABLE_RATCHET.has(error.code);
}
const MAX_MESSAGES = 5000;
const DIRECTORY_TTL = 5 * 60 * 1000;
const TYPING_PING_MS = 3000;
const TYPING_TTL_MS = 5000;

/** Receipt states of own messages, in order: sent, delivered, read. */
const RECEIPT_RANK = { sent: 1, delivered: 2, read: 3 };

/**
 * Applies a delivery or read receipt for message id to a conversation's
 * messages. A read receipt names the newest message the contact read, so it
 * covers every own message up to it. States only move forward. Returns
 * whether anything changed.
 */
export function applyReceipt(messages, kind, id) {
    const target = messages.find(m => m.id === id && m.self);
    if (!target || !RECEIPT_RANK[kind]) return false;
    let changed = false;
    for (const message of messages) {
        if (!message.self || message.pending || message.queued || message.failed) continue;
        const covered = message === target || (kind === "read" && message.sentAt <= target.sentAt);
        if (!covered || (RECEIPT_RANK[message.receipt] || 0) >= RECEIPT_RANK[kind]) continue;
        message.receipt = kind;
        changed = true;
    }
    return changed;
}

/** A conversation as plain text for export: one "[time] name: text" line per message. */
export function exportConversation(conversation, { selfName = "", peerName = "", format = date => date.toISOString() } = {}) {
    const lines = [];
    for (const message of conversation?.messages || []) {
        if (message.queued || message.failed) continue;
        const name = message.self ? selfName : (message.fromNickname || peerName);
        const body = message.deleted ? "(deleted)" : message.body ?? "(locked)";
        lines.push(`[${format(new Date(message.sentAt * 1000))}] ${name}: ${body}${message.edited ? " (edited)" : ""}`);
    }
    return lines.join("\n") + (lines.length ? "\n" : "");
}

function equalBytes(a, b) {
    return a instanceof Uint8Array && b instanceof Uint8Array && a.length === b.length && a.every((v, i) => v === b[i]);
}

/** The server's key for this account does not chain to the desktop that linked this device. */
function ownRootMismatch() {
    return Object.assign(new Error("the server's key for your account does not match the desktop that linked this device; secure messaging is blocked until this device is linked again"), { code: "server_binding_mismatch" });
}

/**
 * The approving desktop's key from this device's own verified link statement
 * ({key, deviceID}), or null. The anchor is the fingerprint from the scanned
 * QR (or the encrypted backup that carried it), never a server-relayed value.
 */
export function ownAnchor(meta, identity, principal) {
    if (!meta?.bindingAnchor || !meta.linkBinding || !identity?.edPublic || !identity?.xPublic) return null;
    try { verifyLinkBinding(meta.linkBinding, { identity, principal, approverFingerprint: meta.bindingAnchor }); } catch { return null; }
    const key = ed25519FromPEM(meta.linkBinding.approverKey);
    return key ? { key, deviceID: meta.linkBinding.approverDeviceID } : null;
}

/** Trust states of a contact's device list. */
export const Trust = Object.freeze({
    ok: "ok",
    // The contact's identity differs from the one pinned on first contact.
    changed: "changed",
    // The contact's client has not published an identity this app can use.
    unavailable: "unavailable",
});

export class DirectMessages extends EventTarget {
    /**
     * session: the ChatSession; meta: link metadata (approverFingerprint).
     */
    constructor(session, meta = {}, { fence } = {}) {
        super();
        // Ownership belongs to the session that created this object, never to
        // whichever account happens to be current when an async write resumes.
        this.#fence = fence ? Promise.resolve({ ...fence }) : currentFence().catch(() => null);
        this.session = session;
        this.conn = session.conn;
        this.identity = session.identity;
        this.ownUID = session.uniqueID;
        this.deviceID = session.deviceID;
        this.meta = meta;
        /** peer -> {peer, nickname, messages[]} */
        this.conversations = new Map();
        /** uid -> {at, trust, devices, signingKey} */
        this.directory = new Map();
        this.ready = this.#load();
        this.#saveTimers = new Map();
        /** peer -> {nickname, until} while the contact is typing */
        this.typing = new Map();
        /** Whether to send typing and receipts; the app sets it from the settings. */
        this.shareActivity = () => false;
        this.#onDirect = event => { this.#receive(event.detail).catch(() => {}); };
        session.addEventListener("direct", this.#onDirect);
        this.#onReceipt = event => { this.ready.then(() => this.#assertActive()).then(() => this.#receipt(event.detail)).catch(() => {}); };
        session.addEventListener("receipt", this.#onReceipt);
        this.#onTyping = event => { this.#assertActive().then(() => {
            const { uid, nickname } = event.detail || {};
            if (!uid) return;
            this.typing.set(uid, { nickname, until: Date.now() + TYPING_TTL_MS });
            this.#emit("typing", { peer: uid });
        }).catch(() => {}); };
        session.addEventListener("typing-direct", this.#onTyping);
        // Write pending history before the page is frozen or unloaded.
        this.#onHide = () => { if (document.visibilityState === "hidden") this.flush(); };
        globalThis.addEventListener?.("pagehide", this.#onHide);
        globalThis.document?.addEventListener("visibilitychange", this.#onHide);
    }

    #onHide;
    #fence;
    #closed = false;
    #writes = new Map();

    #enqueue(store, key, work) {
        const id = JSON.stringify([store, key]);
        const pending = (this.#writes.get(id) || Promise.resolve()).catch(() => {}).then(work);
        this.#writes.set(id, pending);
        const clean = () => { if (this.#writes.get(id) === pending) this.#writes.delete(id); };
        pending.then(clean, clean);
        return pending;
    }

    #assertOpen() {
        if (this.#closed) throw Object.assign(new Error("direct messages session retired"), { code: "fenced" });
    }

    async #assertActive() {
        this.#assertOpen();
        const expected = await this.#fence;
        if (expected) {
            const actual = await currentFence();
            if (!actual.active || actual.generation !== expected.generation) {
                this.close();
                throw Object.assign(new Error("direct messages identity changed"), { code: "fenced" });
            }
        }
        this.#assertOpen();
    }

    async #put(store, key, value) {
        this.#assertOpen();
        // Snapshot now: close() may flush immediately before retiring us.
        const snapshot = structuredClone(value);
        await this.#enqueue(store, key, async () => {
            const expected = await this.#fence;
            if (!expected) throw new Error("identity storage unavailable");
            await putSealedIn(expected, store, key, snapshot);
        });
    }

    async #read(store, key) {
        await this.#assertActive();
        const value = await getSealed(store, key);
        await this.#assertActive();
        return value;
    }

    #saveTimers;
    #onDirect;
    #onReceipt;
    #onTyping;
    #typingSent = new Map();

    #emit(type, detail) {
        if (this.#closed) return;
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    async #load() {
        try {
            await this.#assertActive();
            for (const peer of await keys(Stores.dm)) {
                const stored = await this.#read(Stores.dm, peer);
                if (stored?.peer) this.conversations.set(stored.peer, stored);
            }
        } catch {
            // Storage unavailable (private window): history lives in memory.
        }
        this.#emit("conversations");
    }

    #save(peer) {
        if (this.#closed) return;
        clearTimeout(this.#saveTimers.get(peer));
        this.#saveTimers.set(peer, setTimeout(() => this.#write(peer), 50));
    }

    #write(peer) {
        if (this.#closed) return;
        clearTimeout(this.#saveTimers.get(peer));
        this.#saveTimers.delete(peer);
        const conversation = this.conversations.get(peer);
        if (conversation) this.#put(Stores.dm, peer, conversation).catch(() => {});
    }

    /** Writes every conversation with unsaved changes now. */
    flush() {
        for (const peer of [...this.#saveTimers.keys()]) this.#write(peer);
    }

    conversation(peer, nickname = "") {
        let conversation = this.conversations.get(peer);
        if (!conversation) {
            conversation = { peer, nickname, messages: [] };
            this.conversations.set(peer, conversation);
        }
        if (nickname && conversation.nickname !== nickname) conversation.nickname = nickname;
        return conversation;
    }

    /** Conversations, most recent first. */
    list() {
        return [...this.conversations.values()]
            .filter(c => c.messages.length)
            .sort((a, b) => (b.messages.at(-1)?.sentAt || 0) - (a.messages.at(-1)?.sentAt || 0));
    }

    /** Same message: equal sender-chosen ID from the same sender and protocol. */
    static same(a, b) {
        return !!a.id && a.id === b.id && (a.from || "") === (b.from || "") && (a.protocol || "") === (b.protocol || "");
    }

    #add(peer, nickname, message, { notify = null } = {}) {
        if (this.#closed) return false;
        const conversation = this.conversation(peer, nickname);
        const existing = conversation.messages.findIndex(m => DirectMessages.same(m, message));
        if (existing >= 0) {
            conversation.messages[existing] = { ...conversation.messages[existing], ...message };
        } else {
            conversation.messages.push(message);
            conversation.messages.sort((a, b) => a.sentAt - b.sentAt);
            const waits = change => change.ref === message.id && change.from === message.from && (change.protocol || "") === (message.protocol || "");
            const waiting = (conversation.pendingChanges || []).filter(waits);
            if (waiting.length) {
                conversation.pendingChanges = conversation.pendingChanges.filter(change => !waits(change));
                queueMicrotask(() => waiting.forEach(change => this.#applyChange(peer, change)));
            }
            if (conversation.messages.length > MAX_MESSAGES) conversation.messages.splice(0, conversation.messages.length - MAX_MESSAGES);
        }
        // Own messages are written at once; incoming bursts are batched.
        if (message.self) this.#write(peer);
        else this.#save(peer);
        // New-protocol unread/notification effects follow the durable receive
        // outcome (notify), never whether a debounced display cache had it.
        const isNew = notify === null ? existing < 0 : existing < 0 && notify;
        this.#emit("messages", { peer, message, isNew });
        return existing < 0;
    }

    // --- Directory and trust ------------------------------------------------

    async #pin(uid) {
        try {
            return await this.#read(Stores.pins, uid);
        } catch (error) {
            if (error?.code === "fenced") throw error;
            return undefined;
        }
    }

    /**
     * Returns {trust, devices, fingerprint} for uid. devices are the trusted
     * devices ({deviceID, x25519, x25519B64}).
     */
    async devicesOf(uid, { refresh = false } = {}) {
        await this.#assertActive();
        const cached = this.directory.get(uid);
        if (cached && !refresh && Date.now() - cached.at < DIRECTORY_TTL) return cached;
        // Concurrent lookups share one request; the server rate-limits them.
        const inflight = this.#lookups.get(uid);
        if (inflight) return inflight;
        const lookup = this.#lookup(uid).finally(() => this.#lookups.delete(uid));
        this.#lookups.set(uid, lookup);
        return lookup;
    }

    #lookups = new Map();

    async #lookup(uid) {
        const response = await this.conn.request(Msg.KeyRequest, { unique_id: uid, devices: true }, Msg.KeyResponse,
            { accept: r => r.unique_id === uid });
        await this.#assertActive();
        const record = parseKeyRecord(response.public_key, uid);
        const devices = response.devices || [];
        let signingKey = record?.signingKey || null;
        let encryptionKey = record?.encryptionKey || null;
        // This account's own root comes from local evidence only (#ownRoot),
        // never from the server's self-signed record.
        let own = null;
        if (uid === this.ownUID) {
            own = this.#ownRoot(record);
            ({ signingKey, encryptionKey } = own);
        }
        const entry = { at: Date.now(), trust: Trust.unavailable, devices: [], fingerprint: "", verified: false, newDevices: [] };
        // block: this account's own root is not trusted; secure messaging is
        // blocked with the re-link explanation (app.js).
        if (own?.block) entry.block = own.block;
        if (signingKey) {
            let pinned = await this.#pin(uid);
            const signingB64 = toBase64(signingKey);
            const fingerprint = keyFingerprint(`-----BEGIN PUBLIC KEY-----\n${signingB64}\n-----END PUBLIC KEY-----\n`);
            if (own?.anchored && pinned?.signingKey && pinned.signingKey !== signingB64) {
                // A stale own pin (an earlier root, or one a server planted
                // before the anchor was enforced) never outranks the root this
                // device holds from its own link evidence.
                pinned = { ...pinned, signingKey: signingB64, devices: undefined, verified: false };
                await this.#put(Stores.pins, uid, pinned).catch(() => {});
            }
            // After an explicit confirmation only the confirmed identity may be pinned.
            const unconfirmed = !pinned?.signingKey && pinned?.confirmed && pinned.confirmed !== fingerprint;
            if ((pinned?.signingKey && pinned.signingKey !== signingB64) || unconfirmed) {
                entry.trust = Trust.changed;
                if (own) entry.block = ownRootMismatch();
                // A changed identity is no longer the one the user verified.
                if (pinned.verified) await this.#put(Stores.pins, uid, { ...pinned, verified: false }).catch(() => {});
            } else {
                entry.trust = Trust.ok;
                entry.signingKey = signingKey;
                entry.encryptionKey = encryptionKey;
                entry.devices = trustedDevices(uid, devices, signingKey, entry.encryptionKey);
                const ids = entry.devices.map(d => d.deviceID);
                if (!pinned || !Array.isArray(pinned.devices)) {
                    // First contact (or a pin from before device tracking): remember the list.
                    await this.#put(Stores.pins, uid, { ...pinned, signingKey: signingB64, at: pinned?.at || Date.now(), devices: ids }).catch(() => {});
                    entry.verified = !!pinned?.verified;
                } else {
                    entry.verified = !!pinned.verified;
                    entry.newDevices = entry.devices.filter(d => !pinned.devices.includes(d.deviceID));
                }
            }
            entry.fingerprint = fingerprint;
        }
        await this.#assertActive();
        this.directory.set(uid, entry);
        return entry;
    }

    /**
     * This account's own root key, from local evidence only, never from the
     * server's (merely self-signed) key record alone. Returns {signingKey,
     * encryptionKey, anchored, block}; a null signingKey is untrusted.
     *
     * - A guest is self-rooted: its root is its own local identity.
     * - A linked member's root is the approver key of its verified desktop
     *   link statement (meta.linkBinding, anchored by meta.bindingAnchor from
     *   the scanned QR or the encrypted backup). The server's record adds only
     *   the root's encryption key, and only when that key signed it; a record
     *   signed by any other key blocks secure messaging (link again).
     * - Without a verified statement (typed-code or passkey link, a restore
     *   of an older backup) nothing is trusted: such a device must be linked
     *   again by QR for secure messaging anyway.
     */
    #ownRoot(record) {
        if (this.meta.guest) {
            const { edPublic, xPublic } = this.identity || {};
            if (!(edPublic instanceof Uint8Array) || !(xPublic instanceof Uint8Array)) return { signingKey: null, encryptionKey: null };
            return { signingKey: edPublic, encryptionKey: xPublic, anchored: true };
        }
        const anchor = ownAnchor(this.meta, this.identity, this.ownUID);
        if (!anchor) {
            return { signingKey: null, encryptionKey: null,
                block: Object.assign(new Error("this device has no confirmed account key; link it again by scanning the QR code on your desktop"), { code: "server_binding_unavailable" }) };
        }
        if (!record || equalBytes(record.signingKey, anchor.key)) {
            return { signingKey: anchor.key, encryptionKey: record ? record.encryptionKey : null, anchored: true };
        }
        // Any other root is refused, even one the approving desktop "chains"
        // to: the server can always list the real approver as a device its
        // own root approved (it holds that root's private key), so such a
        // chain proves nothing. A desktop signs in with the account's identity
        // key (the principal is derived from it), so every desktop of the
        // account, first or secondary, approves with the root key itself.
        return { signingKey: null, encryptionKey: null, block: ownRootMismatch() };
    }

    /** Verifies a historical group sender without trusting it for new traffic. */
    async historicalSenderKey(uid, deviceID, encoded, chain) {
        if (!Array.isArray(chain) || !chain.length || chain.length > 64) return null;
        const entry = await this.devicesOf(uid);
        if (entry.trust !== Trust.ok || !entry.signingKey) return null;
        const device = trustedDevices(uid, chain, entry.signingKey, entry.encryptionKey)
            .find(d => d.deviceID === deviceID && d.x25519B64 === encoded);
        return device?.x25519 || null;
    }

    /**
     * The X25519 key a contact's call participant may use: a key its trusted
     * device bound (deviceID), or its signed key record under the pinned
     * identity. Returns raw bytes or null.
     */
    async callKey(uid, encoded, deviceID) {
        for (const refresh of [false, true]) {
            const entry = await this.devicesOf(uid, { refresh }).catch(() => null);
            if (!entry || entry.trust !== Trust.ok) return null;
            if (deviceID) {
                const device = entry.devices.find(d => d.deviceID === deviceID && d.x25519B64 === encoded);
                if (device) return device.x25519;
                continue;
            }
            const record = parseKeyRecord(encoded, uid);
            const pinned = await this.#pin(uid);
            if (record && pinned?.signingKey === toBase64(record.signingKey)) return record.encryptionKey;
            if (record && uid === this.ownUID && entry.devices.some(d => d.x25519B64 === toBase64(record.encryptionKey))) return record.encryptionKey;
        }
        return null;
    }

    /** Marks a contact's identity as verified (compared in person) or not. */
    async setVerified(uid, verified) {
        const pinned = await this.#pin(uid);
        if (!pinned?.signingKey) throw new Error("no identity to verify");
        await this.#put(Stores.pins, uid, { ...pinned, verified: !!verified });
        const cached = this.directory.get(uid);
        if (cached && cached.trust === Trust.ok) cached.verified = !!verified;
    }

    /** Remembers a contact's current devices, so the new-device notice goes away. */
    async acknowledgeDevices(uid) {
        const cached = this.directory.get(uid);
        const pinned = await this.#pin(uid);
        if (!cached || cached.trust !== Trust.ok || !pinned?.signingKey) return;
        await this.#put(Stores.pins, uid, { ...pinned, devices: cached.devices.map(d => d.deviceID) });
        cached.newDevices = [];
    }

    /** Accepts a contact's new identity after the user compared it. */
    async trustNewIdentity(uid, fingerprint) {
        // Pin exactly the identity the user compared and confirmed, never
        // whatever the server returns on the refresh below.
        if (typeof fingerprint !== "string" || !fingerprint) throw new Error("the confirmed fingerprint is required");
        // The explicit confirmation is stored durably with the (now unpinned)
        // contact: the ratchet consumer applies the reset on its next trust
        // check even if no session is live now or the immediate reset fails.
        // A fresh token per confirmation: clearing is compare-and-clear.
        await this.#put(Stores.pins, uid, { ratchetReset: crypto.randomUUID(), confirmed: fingerprint });
        this.directory.delete(uid);
        const entry = await this.devicesOf(uid, { refresh: true });
        // A live consumer drops the old pins now; the intent itself is cleared
        // by its next trust check once the new key is pinned.
        await this.ratchet?.resetTrust(uid);
        return entry;
    }

    /**
     * Token of a confirmed identity change whose ratchet reset is not applied
     * yet, once the contact is re-pinned to the new key; otherwise "".
     */
    async ratchetResetPending(uid) {
        const pinned = await this.#pin(uid);
        return pinned?.signingKey && typeof pinned.ratchetReset === "string" ? pinned.ratchetReset : "";
    }

    /** Clears exactly this token (a newer confirmation keeps its own). */
    async clearRatchetReset(uid, token) {
        const pinned = await this.#pin(uid);
        if (token && pinned?.ratchetReset === token) await this.#put(Stores.pins, uid, { ...pinned, ratchetReset: "" });
    }

    /** Whether this device may send: the account's desktop confirmed it. */
    async canSend() {
        const own = await this.devicesOf(this.ownUID);
        return own.devices.some(d => d.deviceID === this.deviceID);
    }

    // --- Receiving ------------------------------------------------------------

    async #receive(data) {
        if (!data?.recipient_device_id || data.recipient_device_id !== this.deviceID) return;
        await this.ready;
        await this.#assertActive();
        const from = data.from_unique_id;
        const peer = from === this.ownUID ? data.to_unique_id : from;
        if (!peer) return;
        let sender = await this.#senderDevice(from, data);
        if (!sender) sender = await this.#senderDevice(from, data, true);
        await this.#assertActive();
        let text = sender ? openDM(data.text, sender.x25519, this.identity.xSecret) : null;
        // A history chunk replayed as a DM copy is never shown.
        if (isHistoryEnvelope(text)) text = null;
        const own = from === this.ownUID;
        const nickname = own ? "" : data.from;
        if (data.dm_action) {
            if (text !== null) this.#applyChange(peer, { action: data.dm_action, ref: data.ref_client_msg_id, from, text });
            return;
        }
        // A contact whose message arrived is done typing it.
        if (!own && this.typing.delete(peer)) this.#emit("typing", { peer });
        const isNew = this.#add(peer, nickname, {
            id: data.client_msg_id || crypto.randomUUID(), from, fromNickname: data.from, self: own,
            body: text, verified: !!text, sentAt: Date.now() / 1000, offline: !!data.offline,
        });
        if (isNew && !own && data.client_msg_id && this.shareActivity() && this.conn.open) {
            this.conn.send(Msg.ChatDelivered, { to_unique_id: from, client_msg_id: data.client_msg_id });
        }
    }

    /** A contact's delivery or read receipt for one of this account's messages. */
    #receipt({ kind, from, clientMsgID }) {
        if (this.#closed) return;
        const conversation = from && this.conversations.get(from);
        if (!conversation || !clientMsgID) return;
        if (applyReceipt(conversation.messages, kind, clientMsgID)) {
            this.#save(from);
            this.#emit("receipt", { peer: from });
        }
    }

    /**
     * Sends a read receipt for the newest message from peer once it was
     * shown, unless activity is private. Called by the open conversation.
     */
    markRead(peer) {
        if (this.#closed) return false;
        const conversation = this.conversations.get(peer);
        if (!conversation || !this.shareActivity() || !this.conn.open) return false;
        const newest = conversation.messages.filter(m => !m.self && !m.copied && m.id && !m.deleted).at(-1);
        if (!newest || conversation.readReceipt === newest.id) return false;
        conversation.readReceipt = newest.id;
        this.#save(peer);
        this.conn.send(Msg.ChatRead, { to_unique_id: peer, client_msg_id: newest.id });
        return true;
    }

    /** Announces typing to peer, at most every 3 s, unless activity is private. */
    sendTyping(peer, now = Date.now()) {
        if (this.#closed) return false;
        if (!this.shareActivity() || (this.#typingSent.has(peer) && now - this.#typingSent.get(peer) < TYPING_PING_MS)) return false;
        this.#typingSent.set(peer, now);
        this.session.sendTypingDirect(peer);
        return true;
    }

    /** The contact's nickname while peer is typing ("" when unknown), else null. */
    typingName(peer, now = Date.now()) {
        const entry = this.typing.get(peer);
        return entry && entry.until > now ? entry.nickname || "" : null;
    }

    /**
     * Applies an edit or deletion from the original's sender. A change that
     * arrives before its message (spool order) waits on the conversation.
     */
    #applyChange(peer, change) {
        if (this.#closed) return;
        const conversation = this.conversation(peer);
        // Authenticated changes address the original sender's logical ID within
        // the same protocol; a legacy change never edits a ratcheted record.
        const message = conversation.messages.find(m => m.id === change.ref && m.from === change.from && (m.protocol || "") === (change.protocol || ""));
        if (!message) {
            conversation.pendingChanges = [...(conversation.pendingChanges || []), change].slice(-50);
            this.#save(peer);
            return;
        }
        if (message.from !== change.from) return;
        if (change.action === "delete") this.#add(peer, "", { ...message, body: "", deleted: true, edited: false });
        else if (change.action === "edit" && !message.deleted) this.#add(peer, "", { ...message, body: change.text, edited: true });
    }

    async #senderDevice(uid, data, refresh = false) {
        const entry = await this.devicesOf(uid, { refresh }).catch(() => null);
        return entry?.devices.find(d => d.deviceID === data.sender_device_id && d.x25519B64 === data.sender_device_key) || null;
    }

    // --- Sending ----------------------------------------------------------------

    /** Sends text to peer, sealed for every trusted device. */
    async send(peer, text, nickname = "", { id = crypto.randomUUID(), sentAt = Date.now() / 1000 } = {}) {
        await this.ready;
        await this.#assertActive();
        if (this.ratchet) return this.#sendRatchet(peer, text, nickname, id, sentAt);
        this.#refuseLegacySend();
        const message = { id, from: this.ownUID, fromNickname: this.session.nickname, self: true, body: text, verified: true, sentAt, pending: true, queued: false };
        this.#add(peer, nickname, message);
        try {
            await this.#sendCopies(peer, text, id, false);
            await this.#assertActive();
            const current = this.conversations.get(peer)?.messages.find(m => m.id === id);
            // A receipt may already have overtaken the acceptance.
            this.#add(peer, nickname, { ...message, pending: false, receipt: current?.receipt || "sent" });
        } catch (error) {
            // Lost connections leave the message for the outbox; refusals fail it.
            const retryable = !(error instanceof ProtocolError) && !error.code;
            this.#add(peer, nickname, { ...message, pending: false, failed: !retryable, queued: retryable });
            throw Object.assign(error, { retryable });
        }
    }

    /** Shows an own message waiting in the outbox. */
    showQueued(peer, { id, text, sentAt, expired = false }) {
        // With a consumer attached, outbox entries are ratchet operations: match
        // and update that record (same sender + protocol) instead of a ghost.
        // A signed-in session sends only noxa-olm-v1: a queued entry is an Olm
        // operation even while the consumer is not attached yet.
        const protocol = this.ratchet || this.secureRequired ? RATCHET_PROTOCOL : "";
        const existing = this.conversations.get(peer)?.messages.find(m => m.id === id && m.from === this.ownUID && (m.protocol || "") === protocol);
        if (existing && !existing.queued && !existing.failed) return;
        // A committed original keeps its text; the draft never replaces it.
        const body = existing && protocol ? existing.body : text;
        this.#add(peer, "", { id, ...(protocol ? { protocol } : {}), from: this.ownUID, fromNickname: this.session.nickname, self: true, body, verified: true, sentAt, queued: !expired, failed: expired });
    }

    /** Removes a message that never reached the server from this device. */
    discardLocal(peer, id) {
        if (this.#closed) return;
        const conversation = this.conversations.get(peer);
        if (!conversation) return;
        conversation.messages = conversation.messages.filter(m => m.id !== id || !(m.queued || m.failed));
        this.#write(peer);
        this.#emit("messages", { peer, message: null, isNew: false });
    }

    /** Deletes the history with peer on this device only. */
    async deleteHistory(peer) {
        this.#assertOpen();
        clearTimeout(this.#saveTimers.get(peer));
        this.#saveTimers.delete(peer);
        this.conversations.delete(peer);
        // Serialize after already-started cache writes; later messages may
        // create a new conversation, ordered after this deletion.
        await this.#enqueue(Stores.dm, peer, async () => {
            const expected = await this.#fence;
            if (!expected) throw new Error("identity storage unavailable");
            await deleteKeyIn(expected, Stores.dm, peer);
        });
        await this.#assertActive();
        this.#emit("messages", { peer, message: null, isNew: false });
        this.#emit("conversations");
    }

    /** Edits one of this account's messages on every device. */
    async editMessage(peer, ref, text) {
        await this.#assertActive();
        const message = this.#ownMessage(peer, ref);
        if (message.protocol === RATCHET_PROTOCOL) return this.#changeRatchet(peer, message, "edit", text);
        if (this.secureRequired) throw Object.assign(new Error("messages from before secure messaging was upgraded stay readable but can no longer be edited or deleted"), { code: "legacy_read_only", retryable: false });
        await this.#sendCopies(peer, text, crypto.randomUUID(), false, { dm_action: "edit", ref_client_msg_id: ref });
        this.#add(peer, "", { ...message, body: text, edited: true });
    }

    /** Deletes one of this account's messages on every device. */
    async deleteMessage(peer, ref) {
        await this.#assertActive();
        const message = this.#ownMessage(peer, ref);
        if (message.protocol === RATCHET_PROTOCOL) return this.#changeRatchet(peer, message, "delete", "");
        if (this.secureRequired) throw Object.assign(new Error("messages from before secure messaging was upgraded stay readable but can no longer be edited or deleted"), { code: "legacy_read_only", retryable: false });
        await this.#sendCopies(peer, "", crypto.randomUUID(), false, { dm_action: "delete", ref_client_msg_id: ref });
        this.#add(peer, "", { ...message, body: "", deleted: true, edited: false });
    }

    #ownMessage(peer, ref) {
        const message = this.conversations.get(peer)?.messages.find(m => m.id === ref && m.from === this.ownUID);
        if (!message || !message.self || message.deleted || message.queued || message.pending) throw new Error("only your sent messages can be changed");
        return message;
    }

    async #sendCopies(peer, text, id, retried, change = {}) {
        const [recipient, own] = await Promise.all([this.devicesOf(peer, { refresh: retried }), this.devicesOf(this.ownUID, { refresh: retried })]);
        if (recipient.trust === Trust.changed) throw Object.assign(new Error("identity changed"), { code: "identity_changed" });
        if (!recipient.devices.length) throw Object.assign(new Error("no devices"), { code: "unavailable" });
        if (!own.devices.some(d => d.deviceID === this.deviceID)) throw Object.assign(new Error("not confirmed"), { code: "unconfirmed" });
        const targets = new Map();
        for (const device of [...own.devices, ...recipient.devices]) targets.set(device.deviceID, device);
        const copies = [...targets.values()].map(device => ({ device_id: device.deviceID, text: sealDM(text, device.x25519, this.identity.xSecret) }));
        await this.#assertActive();
        try {
            await this.conn.request(Msg.ChatSend, { to_unique_id: peer, enc: true, ack_requested: true, client_msg_id: id, copies, ...change },
                MsgChatAccepted, { accept: r => r.client_msg_id === id });
        } catch (error) {
            if (!retried && error instanceof ProtocolError && error.devicesStale) return this.#sendCopies(peer, text, id, true, change);
            throw error;
        }
    }

    // --- noxa-olm-v1 (explicitly enabled sessions only) ---------------------

    /**
     * Routes this session's direct sends/edits/deletes through one durable
     * RatchetConsumer and projects its authenticated archive. Once attached,
     * new-protocol failures never fall back to legacy sending or decoding.
     */
    async attachRatchet(consumer) {
        this.#assertOpen();
        this.ratchet = consumer;
        const owned = () => !this.#closed && this.ratchet === consumer && !consumer.closed;
        consumer.project((record, { isNew }) => this.#project(record, isNew, owned));
        await this.ready;
        if (!owned()) return;
        // The protected archive is authoritative: restore views it holds.
        const history = await consumer.history();
        for (const record of history) {
            if (!owned()) return;
            if (record.scope === "direct") await this.#project(record, false, owned);
        }
    }

    async #project(record, isNew, owned = () => true) {
        if (record.scope !== "direct" || record.protocol !== RATCHET_PROTOCOL || !record.peer) return;
        // Never project into (and persist over) a conversation before cached
        // history is loaded, nor after this consumer lost ownership.
        await this.ready;
        if (!owned()) return;
        try { await this.#assertActive(); } catch (error) { if (error?.code === "fenced") return; throw error; }
        if (!owned()) return;
        const body = record.body && typeof record.body === "object" ? record.body : {};
        const text = typeof body.text === "string" ? body.text : "";
        if (record.purpose === "edit" || record.purpose === "delete") {
            // An own change shows only once accepted (linked copies carry no status).
            if (record.sender === this.ownUID && record.status && record.status !== "accepted") return;
            this.#applyChange(record.peer, { action: record.purpose, ref: record.reference, from: record.sender, text, protocol: RATCHET_PROTOCOL });
            return;
        }
        const own = record.sender === this.ownUID;
        const prior = this.conversations.get(record.peer)?.messages.find(m => m.id === record.logicalID && m.from === record.sender && m.protocol === RATCHET_PROTOCOL);
        const status = record.status || (own ? "" : "received");
        const message = {
            id: record.logicalID, key: record.key, protocol: RATCHET_PROTOCOL, from: record.sender, fromNickname: prior?.fromNickname || "",
            self: own, copied: own && !!record.copied, body: prior?.deleted ? "" : prior?.edited ? prior.body : text,
            edited: !!prior?.edited, deleted: !!prior?.deleted, verified: true, sentAt: record.sentAt, transport: record.transport || {},
            // Received authenticated on this device: no longer only a
            // transferred copy (the merge in #add would keep that label).
            provenance: "authenticated", origin: undefined, source: undefined,
            pending: own && status === "pending", queued: false, failed: own && (status === "rejected" || status === "aborted"),
            ...(own && status === "accepted" ? { receipt: prior?.receipt || "sent" } : {}),
        };
        if (!own && this.typing.delete(record.peer)) this.#emit("typing", { peer: record.peer });
        this.#add(record.peer, "", message, { notify: isNew && !own });
        this.#unquarantine(record.peer, record.transport?.delivery);
        if (!prior && owned()) await this.#put(Stores.dm, record.peer, this.conversations.get(record.peer)).catch(() => {});
    }

    /**
     * Truthful placeholders for authenticated-protocol copies that could not
     * be opened (lost key, failed authentication, untrusted sender). They are
     * never ACKed or decoded as legacy and never count as unread.
     *
     * Their sender, peer and conversation come only from the server's
     * unauthenticated manifest: a placeholder is never attributed to a sender
     * (neutral label) and is shown only in a conversation this device already
     * has, never creating one.
     */
    async showQuarantined(list = []) {
        await this.ready;
        await this.#assertActive();
        const current = new Set();
        for (const q of list) {
            if (q.scope !== "direct" || !q.peer) continue;
            const conversation = this.conversations.get(q.peer);
            if (!conversation?.messages.some(m => !m.quarantined)) continue;
            const id = "quarantine:" + q.id;
            current.add(id);
            if (conversation.messages.some(m => m.id === id && m.unattributed)) continue;
            this.#add(q.peer, "", { id, protocol: RATCHET_PROTOCOL, from: "", self: false, unattributed: true,
                body: null, verified: false, quarantined: q.code, sentAt: q.seenAt || Date.now() / 1000 }, { notify: false });
        }
        for (const conversation of this.conversations.values()) {
            // An older, attributed placeholder is replaced by the neutral one.
            const keep = conversation.messages.filter(m => !m.quarantined || (current.has(m.id) && m.unattributed));
            if (keep.length !== conversation.messages.length) {
                conversation.messages = keep;
                this.#write(conversation.peer);
                this.#emit("messages", { peer: conversation.peer, message: null, isNew: false });
            }
        }
    }

    #unquarantine(peer, delivery) {
        const conversation = delivery && this.conversations.get(peer);
        if (!conversation) return;
        const id = "quarantine:" + delivery;
        if (conversation.messages.some(m => m.id === id)) conversation.messages = conversation.messages.filter(m => m.id !== id);
    }

    async #sendRatchet(peer, text, nickname, id, sentAt) {
        const existing = this.conversations.get(peer)?.messages.find(m => m.id === id && m.from === this.ownUID && m.protocol === RATCHET_PROTOCOL);
        // Never paint a changed draft over an already committed operation.
        if (!existing) {
            this.#add(peer, nickname, { id, protocol: RATCHET_PROTOCOL, from: this.ownUID, fromNickname: this.session.nickname, self: true, body: text, verified: true, sentAt, pending: true, queued: false });
        }
        const consumer = this.ratchet;
        try {
            const outcome = await consumer.send({ logicalID: id, scope: "direct", peer, body: { text, sentAt } });
            // A reused ID whose committed text differs: the ORIGINAL was sent,
            // this text was not (the view shows the original).
            if (outcome.reused && (outcome.record?.body?.text ?? "") !== text) outcome.mismatch = true;
            return outcome;
        } catch (error) {
            const committed = error.committed;
            // Keep the truthful transport outcome, but retire stale UI/cache
            // work before either success or failure can project a message.
            try { await this.#assertActive(); } catch { /* persistence still uses the original fence */ }
            // An accepted receipt is success even if later housekeeping failed.
            if (committed?.receipt?.state === "accepted") {
                const committedText = committed.record?.body?.text ?? "";
                // The projection never ran: paint the ORIGINAL committed text
                // as sent, never this (possibly changed) draft as pending.
                if (this.ratchet === consumer && !consumer.closed) this.#add(peer, nickname, { id, protocol: RATCHET_PROTOCOL, from: this.ownUID, fromNickname: this.session.nickname, self: true,
                    body: committedText, verified: true, sentAt: committed.record?.sentAt || sentAt, pending: false, queued: false, failed: false, receipt: existing?.receipt || "sent" });
                const outcome = { record: committed.record, receipt: committed.receipt, reused: committed.reused };
                if (committed.reused && committedText !== text) outcome.mismatch = true;
                return outcome;
            }
            // Without a terminal receipt a committed attempt is uncertain: keep
            // its ORIGINAL committed text queued for exact resolution.
            const retryable = ratchetRetryable(error);
            this.#add(peer, nickname, { id, protocol: RATCHET_PROTOCOL, from: this.ownUID, fromNickname: this.session.nickname, self: true,
                body: committed ? committed.record.body?.text ?? "" : existing?.body ?? text, verified: true, sentAt: committed?.record.sentAt || sentAt,
                pending: false, failed: !retryable, queued: retryable });
            throw Object.assign(error, { retryable });
        }
    }

    /**
     * A signed-in session never sends legacy encryption (secureRequired is set
     * by the app): without a consumer the send fails with the blocking reason.
     */
    #refuseLegacySend() {
        if (!this.secureRequired) return;
        const reason = this.secureRequired();
        // blocked: the outbox keeps the entry queued (never failed) until a
        // consumer is attached; an immediate send shows the reason.
        throw Object.assign(new Error(reason?.message || "secure messaging is not available"), { code: reason?.code || "secure_unavailable", retryable: false, blocked: true });
    }

    async #changeRatchet(peer, message, action, text) {
        // reference = the original sender's logical ID, never a transport UUID.
        await this.ratchet.send({ logicalID: crypto.randomUUID(), scope: "direct", peer, purpose: action, reference: message.id, body: action === "edit" ? { text } : {} });
    }

    // --- History copy ---------------------------------------------------------

    /**
     * Imports a history copy uploaded by another device of this account (a
     * desktop or phone the account trusts). Emits "history" {done, total}.
     *
     * - Only readable archive records are imported, into this device's view
     *   history: never send ratchets, keys, sessions or peer trust.
     * - A new-protocol record keeps its original provenance (origin) apart
     *   from the source device that authorized the copy (source); it is shown
     *   as transferred, never as authenticated on this device, and never
     *   replaces a record this device already holds.
     * - Exact chunk dedup: a chunk whose sealed bytes were already imported
     *   is skipped (a re-sent or replayed copy imports nothing twice).
     * - Durable import ACK: a page is acknowledged to the server only after
     *   its records and the imported-chunk digests are persisted.
     */
    async importHistory() {
        await this.ready;
        await this.#assertActive();
        const own = await this.devicesOf(this.ownUID, { refresh: true });
        const state = (await this.#read(Stores.device, HISTORY_IMPORTS)) || {};
        const done = new Set(state.digests || []);
        // Transfer binding: the chunks of one transfer import in order from
        // seq 0 under one signed transfer ID; a chunk spliced from another
        // transfer, or a replay of a completed transfer, imports nothing.
        const completed = new Set(Array.isArray(state.completed) ? state.completed : []);
        // Transfer IDs are recorded when they start: a known transfer never
        // restarts from seq 0 (a replay of an abandoned transfer imports nothing).
        const started = new Set(Array.isArray(state.started) ? state.started : []);
        let current = state.current && typeof state.current.transfer === "string" ? state.current : null;
        let seq = 0;
        let imported = 0, skipped = 0;
        for (;;) {
            const page = await this.conn.request(Msg.DeviceRequest, { action: "history_get", seq }, Msg.DeviceResult, { accept: r => r.action === "history_get" });
            await this.#assertActive();
            const chunks = page.history || [];
            if (!chunks.length) break;
            const touched = new Set(), digests = [];
            for (const chunk of chunks) {
                const digest = typeof chunk.data === "string" && chunk.data ? await chunkDigest(chunk.data) : "";
                await this.#assertActive();
                const sender = own.devices.find(d => d.deviceID === chunk.from_device_id);
                // Data is nonce||box, which JSON carries as standard base64.
                const plain = digest && !done.has(digest) && sender ? openDM(chunk.data, sender.x25519, this.identity.xSecret) : null;
                // Only a signed history envelope for exactly this transfer
                // position and this device opens; a bare array or a replayed
                // DM copy imports nothing.
                const envelope = plain ? decodeHistoryEnvelope(plain, { source: chunk.from_device_id, target: this.deviceID, seq: chunk.seq, total: chunk.total, signingKey: sender.signingKey }) : null;
                const h = envelope?.header;
                if (h && !completed.has(h.transfer) && !started.has(h.transfer) && h.seq === 0) {
                    current = { transfer: h.transfer, source: h.source, next: 0 };
                    started.add(h.transfer);
                }
                const inOrder = h && !completed.has(h.transfer) && current && current.transfer === h.transfer && current.source === h.source && current.next === h.seq;
                if (envelope && inOrder) {
                    current.next++;
                    if (h.seq === h.total - 1) {
                        completed.add(h.transfer);
                        current = null;
                    }
                    for (const m of envelope.records) {
                        const message = historyRecord(m, this.ownUID, chunk.from_device_id, imported);
                        // Never dropped silently: refused records are counted.
                        if (!message) { skipped++; continue; }
                        // A transferred record never overwrites one this device holds.
                        if (message.protocol && this.conversations.get(m.peer)?.messages.some(x => DirectMessages.same(x, message))) continue;
                        if (this.#add(m.peer, typeof m.peer_nickname === "string" ? m.peer_nickname : "", message, { notify: false })) imported++;
                        touched.add(m.peer);
                    }
                    digests.push(digest);
                }
                seq = chunk.seq + 1;
                this.#emit("history", { done: seq, total: chunk.total, imported, skipped });
            }
            // Persist the page (records, then chunk digests) BEFORE the ACK.
            for (const peer of touched) {
                clearTimeout(this.#saveTimers.get(peer));
                this.#saveTimers.delete(peer);
                await this.#put(Stores.dm, peer, this.conversations.get(peer));
            }
            for (const d of digests) done.add(d);
            await this.#put(Stores.device, HISTORY_IMPORTS, { digests: [...done].slice(-MAX_HISTORY_DIGESTS),
                completed: [...completed].slice(-MAX_HISTORY_TRANSFERS), started: [...started].slice(-MAX_HISTORY_TRANSFERS), current });
            await this.#assertActive();
            await this.conn.request(Msg.DeviceRequest, { action: "history_ack", seq: seq - 1 }, Msg.DeviceResult, { accept: r => r.action === "history_ack" });
        }
        this.lastImport = { imported, skipped };
        return imported;
    }

    close() {
        if (this.#closed) return;
        this.session.removeEventListener("direct", this.#onDirect);
        this.session.removeEventListener("receipt", this.#onReceipt);
        this.session.removeEventListener("typing-direct", this.#onTyping);
        globalThis.removeEventListener?.("pagehide", this.#onHide);
        globalThis.document?.removeEventListener("visibilitychange", this.#onHide);
        this.flush();
        this.#closed = true;
    }
}
