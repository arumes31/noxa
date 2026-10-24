// direct.js — end-to-end encrypted direct messages on this device.
//
// Each message is sealed once per trusted device of the recipient and of
// this account (dm_devices_v1, internal/netproto/dm_devices.go). Copies for
// this device arrive live or from the server's per-device spool. History
// is kept in IndexedDB, encrypted with the database key.
import { Stores, getSealed, keys, putSealed } from "./db.js";
import { keyFingerprint, openDM, sealDM, toBase64 } from "./crypto.js";
import { ed25519FromPEM, parseKeyRecord, trustedDevices } from "./dm-trust.js";
import { Msg } from "./frame.js";
import { ProtocolError } from "./connection.js";

const MsgChatAccepted = 154;
const MAX_MESSAGES = 5000;
const DIRECTORY_TTL = 5 * 60 * 1000;

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
        this.#onDirect = event => { this.#receive(event.detail).catch(() => {}); };
        session.addEventListener("direct", this.#onDirect);
        // Write pending history before the page is frozen or unloaded.
        this.#onHide = () => { if (document.visibilityState === "hidden") this.flush(); };
        globalThis.addEventListener?.("pagehide", this.#onHide);
        globalThis.document?.addEventListener("visibilitychange", this.#onHide);
    }

    #onHide;

    #saveTimers;
    #onDirect;

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

    #add(peer, nickname, message) {
        const conversation = this.conversation(peer, nickname);
        const existing = conversation.messages.findIndex(m => m.id && m.id === message.id);
        if (existing >= 0) {
            conversation.messages[existing] = { ...conversation.messages[existing], ...message };
        } else {
            conversation.messages.push(message);
            conversation.messages.sort((a, b) => a.sentAt - b.sentAt);
            if (conversation.messages.length > MAX_MESSAGES) conversation.messages.splice(0, conversation.messages.length - MAX_MESSAGES);
        }
        // Own messages are written at once; incoming bursts are batched.
        if (message.self) this.#write(peer);
        else this.#save(peer);
        this.#emit("messages", { peer, message, isNew: existing < 0 });
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
        const entry = { at: Date.now(), trust: Trust.unavailable, devices: [], fingerprint: "" };
        if (signingKey) {
            const pinned = await this.#pin(uid);
            const signingB64 = toBase64(signingKey);
            if (pinned && pinned.signingKey !== signingB64) {
                entry.trust = Trust.changed;
            } else {
                if (!pinned) await putSealed(Stores.pins, uid, { signingKey: signingB64, at: Date.now() }).catch(() => {});
                entry.trust = Trust.ok;
                entry.devices = trustedDevices(uid, devices, signingKey);
            }
            entry.fingerprint = keyFingerprint(`-----BEGIN PUBLIC KEY-----\n${signingB64}\n-----END PUBLIC KEY-----\n`);
        }
        this.directory.set(uid, entry);
        return entry;
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

    /** Accepts a contact's new identity after the user compared it. */
    async trustNewIdentity(uid) {
        await putSealed(Stores.pins, uid, null).catch(() => {});
        this.directory.delete(uid);
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
        this.#add(peer, nickname, {
            id: data.client_msg_id || crypto.randomUUID(), from, fromNickname: data.from, self: own,
            body: text, verified: !!text, sentAt: Date.now() / 1000, offline: !!data.offline,
        });
    }

    async #senderDevice(uid, data, refresh = false) {
        const entry = await this.devicesOf(uid, { refresh }).catch(() => null);
        return entry?.devices.find(d => d.deviceID === data.sender_device_id && d.x25519B64 === data.sender_device_key) || null;
    }

    // --- Sending ----------------------------------------------------------------

    /** Sends text to peer, sealed for every trusted device. */
    async send(peer, text, nickname = "", { id = crypto.randomUUID(), sentAt = Date.now() / 1000 } = {}) {
        await this.ready;
        const message = { id, from: this.ownUID, fromNickname: this.session.nickname, self: true, body: text, verified: true, sentAt, pending: true, queued: false };
        this.#add(peer, nickname, message);
        try {
            await this.#sendCopies(peer, text, id, false);
            this.#add(peer, nickname, { ...message, pending: false });
        } catch (error) {
            // Lost connections leave the message for the outbox; refusals fail it.
            const retryable = !(error instanceof ProtocolError) && !error.code;
            this.#add(peer, nickname, { ...message, pending: false, failed: !retryable, queued: retryable });
            throw Object.assign(error, { retryable });
        }
    }

    /** Shows an own message waiting in the outbox. */
    showQueued(peer, { id, text, sentAt, expired = false }) {
        const existing = this.conversations.get(peer)?.messages.find(m => m.id === id);
        if (existing && !existing.queued && !existing.failed) return;
        this.#add(peer, "", { id, from: this.ownUID, fromNickname: this.session.nickname, self: true, body: text, verified: true, sentAt, queued: !expired, failed: expired });
    }

    async #sendCopies(peer, text, id, retried) {
        const [recipient, own] = await Promise.all([this.devicesOf(peer, { refresh: retried }), this.devicesOf(this.ownUID, { refresh: retried })]);
        if (recipient.trust === Trust.changed) throw Object.assign(new Error("identity changed"), { code: "identity_changed" });
        if (!recipient.devices.length) throw Object.assign(new Error("no devices"), { code: "unavailable" });
        if (!own.devices.some(d => d.deviceID === this.deviceID)) throw Object.assign(new Error("not confirmed"), { code: "unconfirmed" });
        const targets = new Map();
        for (const device of [...own.devices, ...recipient.devices]) targets.set(device.deviceID, device);
        const copies = [...targets.values()].map(device => ({ device_id: device.deviceID, text: sealDM(text, device.x25519, this.identity.xSecret) }));
        try {
            await this.conn.request(Msg.ChatSend, { to_unique_id: peer, enc: true, ack_requested: true, client_msg_id: id, copies },
                MsgChatAccepted, { accept: r => r.client_msg_id === id });
        } catch (error) {
            if (!retried && error instanceof ProtocolError && error.devicesStale) return this.#sendCopies(peer, text, id, true);
            throw error;
        }
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
        globalThis.removeEventListener?.("pagehide", this.#onHide);
        globalThis.document?.removeEventListener("visibilitychange", this.#onHide);
        this.flush();
    }
}
