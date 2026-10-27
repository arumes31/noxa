// direct.js — end-to-end encrypted direct messages on this device.
//
// Each message is sealed once per trusted device of the recipient and of
// this account (dm_devices_v1, internal/netproto/dm_devices.go). Copies for
// this device arrive live or from the server's per-device spool. History
// is kept in IndexedDB, encrypted with the database key.
import { Stores, deleteKey, getSealed, keys, putSealed } from "./db.js";
import { keyFingerprint, openDM, sealDM, toBase64 } from "./crypto.js";
import { ed25519FromPEM, parseKeyRecord, trustedDevices } from "./dm-trust.js";
import { Msg } from "./frame.js";
import { ProtocolError } from "./connection.js";
import { RATCHET_PROTOCOL } from "./ratchet-consumer.js";

const MsgChatAccepted = 154;
/** Ratchet conditions an unchanged retry (outbox) can still resolve. */
export const RETRYABLE_RATCHET = new Set(["uncertain", "rate-limit", "unavailable", "local_conflict", "fenced"]);

/**
 * Whether a failed ratchet send should stay queued. A committed attempt without
 * a terminal receipt is uncertain and must be resolved exactly; an aborted
 * attempt was never delivered and may be resealed with its original content.
 * Every other coded refusal before commit (authority, limit, exhausted,
 * account change, conflict, invalid...) fails only this message.
 */
export function ratchetRetryable(error) {
    const committed = error?.committed;
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
    constructor(session, meta = {}) {
        super();
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
        this.#onReceipt = event => { this.ready.then(() => this.#receipt(event.detail)).catch(() => {}); };
        session.addEventListener("receipt", this.#onReceipt);
        this.#onTyping = event => {
            const { uid, nickname } = event.detail || {};
            if (!uid) return;
            this.typing.set(uid, { nickname, until: Date.now() + TYPING_TTL_MS });
            this.#emit("typing", { peer: uid });
        };
        session.addEventListener("typing-direct", this.#onTyping);
        // Write pending history before the page is frozen or unloaded.
        this.#onHide = () => { if (document.visibilityState === "hidden") this.flush(); };
        globalThis.addEventListener?.("pagehide", this.#onHide);
        globalThis.document?.addEventListener("visibilitychange", this.#onHide);
    }

    #onHide;

    #saveTimers;
    #onDirect;
    #onReceipt;
    #onTyping;
    #typingSent = new Map();

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    async #load() {
        try {
            for (const peer of await keys(Stores.dm)) {
                const stored = await getSealed(Stores.dm, peer);
                if (stored?.peer) this.conversations.set(stored.peer, stored);
            }
        } catch {
            // Storage unavailable (private window): history lives in memory.
        }
        this.#emit("conversations");
    }

    #save(peer) {
        clearTimeout(this.#saveTimers.get(peer));
        this.#saveTimers.set(peer, setTimeout(() => this.#write(peer), 50));
    }

    #write(peer) {
        clearTimeout(this.#saveTimers.get(peer));
        this.#saveTimers.delete(peer);
        const conversation = this.conversations.get(peer);
        if (conversation) putSealed(Stores.dm, peer, conversation).catch(() => {});
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
            return await getSealed(Stores.pins, uid);
        } catch {
            return undefined;
        }
    }

    /**
     * Returns {trust, devices, fingerprint} for uid. devices are the trusted
     * devices ({deviceID, x25519, x25519B64}).
     */
    async devicesOf(uid, { refresh = false } = {}) {
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
        const record = parseKeyRecord(response.public_key, uid);
        const devices = response.devices || [];
        let signingKey = record?.signingKey || null;
        if (uid === this.ownUID) {
            // This account's identity is the desktop that approved the link.
            const first = devices.find(d => d.first_device);
            if (first && this.meta.approverFingerprint && keyFingerprint(first.ed25519_public_key) === this.meta.approverFingerprint) {
                signingKey = ed25519FromPEM(first.ed25519_public_key);
            }
        }
        const entry = { at: Date.now(), trust: Trust.unavailable, devices: [], fingerprint: "", verified: false, newDevices: [] };
        if (signingKey) {
            const pinned = await this.#pin(uid);
            const signingB64 = toBase64(signingKey);
            if (pinned && pinned.signingKey !== signingB64) {
                entry.trust = Trust.changed;
                // A changed identity is no longer the one the user verified.
                if (pinned.verified) await putSealed(Stores.pins, uid, { ...pinned, verified: false }).catch(() => {});
            } else {
                entry.trust = Trust.ok;
                entry.signingKey = signingKey;
                entry.encryptionKey = record && toBase64(record.signingKey) === signingB64 ? record.encryptionKey : null;
                entry.devices = trustedDevices(uid, devices, signingKey, entry.encryptionKey);
                const ids = entry.devices.map(d => d.deviceID);
                if (!pinned || !Array.isArray(pinned.devices)) {
                    // First contact (or a pin from before device tracking): remember the list.
                    await putSealed(Stores.pins, uid, { ...pinned, signingKey: signingB64, at: pinned?.at || Date.now(), devices: ids }).catch(() => {});
                    entry.verified = !!pinned?.verified;
                } else {
                    entry.verified = !!pinned.verified;
                    entry.newDevices = entry.devices.filter(d => !pinned.devices.includes(d.deviceID));
                }
            }
            entry.fingerprint = keyFingerprint(`-----BEGIN PUBLIC KEY-----\n${signingB64}\n-----END PUBLIC KEY-----\n`);
        }
        this.directory.set(uid, entry);
        return entry;
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
        await putSealed(Stores.pins, uid, { ...pinned, verified: !!verified });
        const cached = this.directory.get(uid);
        if (cached && cached.trust === Trust.ok) cached.verified = !!verified;
    }

    /** Remembers a contact's current devices, so the new-device notice goes away. */
    async acknowledgeDevices(uid) {
        const cached = this.directory.get(uid);
        const pinned = await this.#pin(uid);
        if (!cached || cached.trust !== Trust.ok || !pinned?.signingKey) return;
        await putSealed(Stores.pins, uid, { ...pinned, devices: cached.devices.map(d => d.deviceID) });
        cached.newDevices = [];
    }

    /** Accepts a contact's new identity after the user compared it. */
    async trustNewIdentity(uid) {
        await putSealed(Stores.pins, uid, null).catch(() => {});
        this.directory.delete(uid);
        // The same explicit confirmation resets ratchet pins for this contact;
        // Olm keys are then re-adopted only from bundles the new chain signs.
        await this.ratchet?.resetTrust(uid);
        return this.devicesOf(uid, { refresh: true });
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
        const from = data.from_unique_id;
        const peer = from === this.ownUID ? data.to_unique_id : from;
        if (!peer) return;
        let sender = await this.#senderDevice(from, data);
        if (!sender) sender = await this.#senderDevice(from, data, true);
        const text = sender ? openDM(data.text, sender.x25519, this.identity.xSecret) : null;
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
        if (this.ratchet) return this.#sendRatchet(peer, text, nickname, id, sentAt);
        const message = { id, from: this.ownUID, fromNickname: this.session.nickname, self: true, body: text, verified: true, sentAt, pending: true, queued: false };
        this.#add(peer, nickname, message);
        try {
            await this.#sendCopies(peer, text, id, false);
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
        const protocol = this.ratchet ? RATCHET_PROTOCOL : "";
        const existing = this.conversations.get(peer)?.messages.find(m => m.id === id && m.from === this.ownUID && (m.protocol || "") === protocol);
        if (existing && !existing.queued && !existing.failed) return;
        // A committed original keeps its text; the draft never replaces it.
        const body = existing && protocol ? existing.body : text;
        this.#add(peer, "", { id, ...(protocol ? { protocol } : {}), from: this.ownUID, fromNickname: this.session.nickname, self: true, body, verified: true, sentAt, queued: !expired, failed: expired });
    }

    /** Removes a message that never reached the server from this device. */
    discardLocal(peer, id) {
        const conversation = this.conversations.get(peer);
        if (!conversation) return;
        conversation.messages = conversation.messages.filter(m => m.id !== id || !(m.queued || m.failed));
        this.#write(peer);
        this.#emit("messages", { peer, message: null, isNew: false });
    }

    /** Deletes the history with peer on this device only. */
    async deleteHistory(peer) {
        clearTimeout(this.#saveTimers.get(peer));
        this.#saveTimers.delete(peer);
        this.conversations.delete(peer);
        await deleteKey(Stores.dm, peer).catch(() => {});
        this.#emit("messages", { peer, message: null, isNew: false });
        this.#emit("conversations");
    }

    /** Edits one of this account's messages on every device. */
    async editMessage(peer, ref, text) {
        const message = this.#ownMessage(peer, ref);
        if (message.protocol === RATCHET_PROTOCOL) return this.#changeRatchet(peer, message, "edit", text);
        await this.#sendCopies(peer, text, crypto.randomUUID(), false, { dm_action: "edit", ref_client_msg_id: ref });
        this.#add(peer, "", { ...message, body: text, edited: true });
    }

    /** Deletes one of this account's messages on every device. */
    async deleteMessage(peer, ref) {
        const message = this.#ownMessage(peer, ref);
        if (message.protocol === RATCHET_PROTOCOL) return this.#changeRatchet(peer, message, "delete", "");
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
        this.ratchet = consumer;
        const owned = () => this.ratchet === consumer && !consumer.closed;
        consumer.project((record, { isNew }) => this.#project(record, isNew, owned));
        await this.ready;
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
        const body = record.body && typeof record.body === "object" ? record.body : {};
        const text = typeof body.text === "string" ? body.text : "";
        if (record.purpose === "edit" || record.purpose === "delete") {
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
            pending: own && status === "pending", queued: false, failed: own && (status === "rejected" || status === "aborted"),
            ...(own && status === "accepted" ? { receipt: prior?.receipt || "sent" } : {}),
        };
        if (!own && this.typing.delete(record.peer)) this.#emit("typing", { peer: record.peer });
        this.#add(record.peer, "", message, { notify: isNew && !own });
        this.#unquarantine(record.peer, record.transport?.delivery);
        if (!prior && owned()) await putSealed(Stores.dm, record.peer, this.conversations.get(record.peer)).catch(() => {});
    }

    /**
     * Truthful placeholders for authenticated-protocol copies that could not
     * be opened (lost key, failed authentication, untrusted sender). They are
     * never ACKed or decoded as legacy and never count as unread.
     */
    async showQuarantined(list = []) {
        await this.ready;
        const current = new Set();
        for (const q of list) {
            if (q.scope !== "direct" || !q.peer) continue;
            const id = "quarantine:" + q.id;
            current.add(id);
            if (this.conversations.get(q.peer)?.messages.some(m => m.id === id)) continue;
            this.#add(q.peer, "", { id, protocol: RATCHET_PROTOCOL, from: q.sender, self: q.sender === this.ownUID, copied: q.sender === this.ownUID,
                body: null, verified: false, quarantined: q.code, sentAt: q.seenAt || Date.now() / 1000 }, { notify: false });
        }
        for (const conversation of this.conversations.values()) {
            const keep = conversation.messages.filter(m => !m.quarantined || current.has(m.id));
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
        try {
            return await this.ratchet.send({ logicalID: id, scope: "direct", peer, body: { text, sentAt } });
        } catch (error) {
            const committed = error.committed;
            // Without a terminal receipt a committed attempt is uncertain: keep
            // its ORIGINAL committed text queued for exact resolution.
            const retryable = ratchetRetryable(error);
            this.#add(peer, nickname, { id, protocol: RATCHET_PROTOCOL, from: this.ownUID, fromNickname: this.session.nickname, self: true,
                body: committed ? committed.record.body?.text ?? "" : existing?.body ?? text, verified: true, sentAt: committed?.record.sentAt || sentAt,
                pending: false, failed: !retryable, queued: retryable });
            throw Object.assign(error, { retryable });
        }
    }

    async #changeRatchet(peer, message, action, text) {
        // reference = the original sender's logical ID, never a transport UUID.
        await this.ratchet.send({ logicalID: crypto.randomUUID(), scope: "direct", peer, purpose: action, reference: message.id, body: action === "edit" ? { text } : {} });
    }

    // --- History copy ---------------------------------------------------------

    /**
     * Imports a history copy uploaded by this account's desktop. Emits
     * "history" {done, total} as chunks arrive.
     */
    async importHistory() {
        await this.ready;
        const own = await this.devicesOf(this.ownUID, { refresh: true });
        let seq = 0;
        let imported = 0;
        for (;;) {
            const page = await this.conn.request(Msg.DeviceRequest, { action: "history_get", seq }, Msg.DeviceResult, { accept: r => r.action === "history_get" });
            const chunks = page.history || [];
            if (!chunks.length) break;
            for (const chunk of chunks) {
                const sender = own.devices.find(d => d.deviceID === chunk.from_device_id);
                // Data is nonce||box, which JSON carries as standard base64.
                const plain = sender && chunk.data ? openDM(chunk.data, sender.x25519, this.identity.xSecret) : null;
                if (plain) {
                    let messages = [];
                    try { messages = JSON.parse(plain); } catch { messages = []; }
                    for (const m of Array.isArray(messages) ? messages : []) {
                        if (!m?.peer || typeof m.body !== "string") continue;
                        if (this.#add(m.peer, m.peer_nickname || "", {
                            id: m.client_msg_id || `history-${m.peer}-${m.sent_at}-${imported}`, from: m.from, fromNickname: m.from_nickname || "",
                            self: !!m.self, body: m.body, verified: true, sentAt: Number(m.sent_at) || 0, copied: true,
                        })) imported++;
                    }
                }
                seq = chunk.seq + 1;
                this.#emit("history", { done: seq, total: chunk.total, imported });
            }
            await this.conn.request(Msg.DeviceRequest, { action: "history_ack", seq: seq - 1 }, Msg.DeviceResult, { accept: r => r.action === "history_ack" });
        }
        return imported;
    }

    close() {
        this.session.removeEventListener("direct", this.#onDirect);
        this.session.removeEventListener("receipt", this.#onReceipt);
        this.session.removeEventListener("typing-direct", this.#onTyping);
        globalThis.removeEventListener?.("pagehide", this.#onHide);
        globalThis.document?.removeEventListener("visibilitychange", this.#onHide);
        this.flush();
    }
}
