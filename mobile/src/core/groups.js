// groups.js — private groups ("conversations") on the phone.
//
//   170 ConversationRequest {action, id, revision, before_id, read_message_id, message}
//   171 ConversationResult {conversations, messages, message_id}
//
// Each message carries an envelope per member sealed to the member's
// account key, and one per confirmed device that cannot open it (this
// phone among them), keyed "uid/deviceID". The plaintext matches the
// desktop (client/conversation.go): {conversation_id, epoch, reference,
// from, to, text}. A device envelope names the sending device; its key
// must be one of the sender's trusted devices.
//
// Activity (conversation_activity_v1): typing goes out as Typing
// {conversation_id} and comes back as "conversation_typing"; a mark_read
// with share_read advances the member's shared position, relayed as
// "conversation_read" and listed as members[].seen_message_id.
import { ProtocolError } from "./connection.js";
import { openDM, sealDM } from "./crypto.js";
import { Msg } from "./frame.js";
import { RATCHET_PROTOCOL } from "./ratchet-consumer.js";
import { ratchetRetryable } from "./direct.js";
/** Bound on edits/deletes waiting for an unknown (sender, logical) target. */
const MAX_WAITING_CHANGES = 200;

const MsgConversationRequest = 170;
const MsgConversationResult = 171;
export const ACTIVITY_CAPABILITY = "conversation_activity_v1";
/** netproto.MaxConversationMembers, invitations included. */
export const MAX_GROUP_MEMBERS = 16;
/** A member's typing entry lives a little longer than the server's 2 s throttle. */
export const GROUP_TYPING_TTL_MS = 4500;
const TYPING_PING_MS = 3000;

/** Legacy server history keeps numeric IDs; noxa-olm-v1 records are opaque. */
export const legacyID = m => !m.local && typeof m.id === "number" && m.id > 0;

/**
 * Server messages by ID, then messages not yet accepted (local) by time.
 * Opaque ratcheted records order by authenticated send time; their string
 * IDs are never coerced into legacy numeric positions.
 */
function byOrder(a, b) {
    if (a.local !== b.local) return a.local ? 1 : -1;
    if (!a.local && legacyID(a) && legacyID(b)) return a.id - b.id;
    return (a.created_at || 0) - (b.created_at || 0);
}

/**
 * Places every other accepted member's shared read position on the newest
 * message it covers: Map(messageID -> [uid, ...]). Mirrors the desktop's
 * group-activity.js: the viewer is never listed, nor a member under their
 * own message, nor a member without a shared position.
 */
export function seenMarkers(messages, members, selfUID) {
    const ordered = messages.filter(m => !m.local && Number(m.id) > 0).sort((a, b) => a.id - b.id);
    const markers = new Map();
    for (const member of members || []) {
        const seen = Number(member?.seen_message_id) || 0;
        if (!seen || member.pending || member.unique_id === selfUID) continue;
        let target = null;
        for (const message of ordered) {
            if (message.id > seen) break;
            target = message;
        }
        if (!target || target.from_unique_id === member.unique_id) continue;
        if (!markers.has(target.id)) markers.set(target.id, []);
        markers.get(target.id).push(member.unique_id);
    }
    return markers;
}

export class Groups extends EventTarget {
    constructor(session, dm) {
        super();
        this.session = session;
        this.dm = dm;
        this.conn = session.conn;
        this.uid = session.uniqueID;
        /** id -> Conversation */
        this.list = new Map();
        /** id -> messages, oldest first; local ones (not yet accepted) last */
        this.messages = new Map();
        /** id -> Map(uid -> {nickname, until}) */
        this.typing = new Map();
        /** Whether to send typing and shared read receipts; the app sets it from the settings. */
        this.shareActivity = () => false;
        this.#onEvent = event => {
            const { type, data } = event.detail;
            if (type === "conversation_changed" && data?.id) this.#changed(data).catch(() => {});
            else if (type === "conversation_typing" && data?.conversation_id) this.#typing(data);
            else if (type === "conversation_read" && data?.conversation_id) this.#seen(data);
        };
        session.addEventListener("server-event", this.#onEvent);
    }

    #onEvent;
    #readSent = new Map();
    #typingSent = new Map();

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    #request(request) {
        return this.conn.request(MsgConversationRequest, request, MsgConversationResult, { accept: r => r.action === request.action });
    }

    get activityAvailable() {
        return !!this.session.capabilities?.has(ACTIVITY_CAPABILITY);
    }

    /** Refreshes the list of groups this account belongs to. */
    async refresh() {
        const result = await this.#request({ action: "list" });
        this.list = new Map((result.conversations || []).map(c => [c.id, c]));
        this.#emit("list");
        return [...this.list.values()];
    }

    async #mutate(buildRequest, signal) {
        for (let attempt = 0; ; attempt++) {
            signal?.throwIfAborted();
            const request = await buildRequest();
            signal?.throwIfAborted();
            if (!request) return;
            try { return await this.#request(request); }
            catch (error) {
                // Only an explicit pre-mutation rate rejection is safe to retry.
                if (!(error instanceof ProtocolError) || error.message !== "group rate limit exceeded" ||
                    !Number.isFinite(error.retryAfterMS) || error.retryAfterMS <= 0 || error.retryAfterMS > 30000 || attempt >= 3) throw error;
                await waitForGroupRetry(error.retryAfterMS, signal);
            }
        }
    }

    async create(name, { signal } = {}) {
        const result = await this.#mutate(() => ({ action: "create", name: name.trim() }), signal);
        const group = result.conversations?.[0];
        if (!group?.id) throw new Error("missing group response");
        this.list.set(group.id, group);
        this.#emit("list");
        return group;
    }

    async invite(id, target, { signal } = {}) {
        await this.#mutate(async () => {
            const group = await this.get(id);
            if (!group) throw new Error("group unavailable");
            if (group.members?.some(member => member.unique_id === target)) return null;
            return { action: "invite", id, revision: group.revision, target };
        }, signal);
        await this.get(id);
    }

    /** One owner-or-member change at the group's current revision (remove, transfer, rename, leave, delete, delete_message). */
    async #change(id, fields) {
        await this.#mutate(async () => {
            const group = await this.get(id);
            if (!group) throw new Error("group unavailable");
            return { id, revision: group.revision, ...fields };
        });
    }

    /** Removes a member or withdraws an invitation (owner). */
    async remove(id, target) {
        await this.#change(id, { action: "remove", target });
        await this.get(id);
    }

    /** Hands the group to another accepted member (owner). */
    async transfer(id, target) {
        await this.#change(id, { action: "transfer", target });
        await this.get(id);
    }

    async rename(id, name) {
        await this.#change(id, { action: "rename", name: name.trim() });
        await this.get(id);
    }

    /** Leaves the group; an owner must hand it over first unless alone. */
    async leave(id) {
        await this.#change(id, { action: "leave" });
        this.#forget(id);
    }

    /** Deletes the group for everyone (owner). */
    async deleteGroup(id) {
        await this.#change(id, { action: "delete" });
        this.#forget(id);
    }

    /** Deletes one message for every member (owner). */
    async deleteMessage(id, messageID) {
        const record = (this.messages.get(id) || []).find(m => m.id === messageID);
        if (record?.protocol === RATCHET_PROTOCOL) {
            if (!this.ratchet || record.from_unique_id !== this.uid) throw new Error("only your sent messages can be changed");
            // reference = the original sender's logical ID, never a transport UUID.
            await this.ratchet.send({ logicalID: crypto.randomUUID(), scope: "group", conversation: id, purpose: "delete", reference: record.reference, body: {} });
            return;
        }
        await this.#change(id, { action: "delete_message", message_id: messageID });
        const list = this.messages.get(id) || [];
        const index = list.findIndex(m => m.id === messageID);
        if (index >= 0) list[index] = { ...list[index], text: "", deleted: true };
        this.#emit("messages", { id });
    }

    #forget(id) {
        this.list.delete(id);
        this.messages.delete(id);
        this.typing.delete(id);
        this.#emit("list");
    }

    async get(id) {
        const result = await this.#request({ action: "get", id });
        const conversation = result.conversations?.[0];
        if (conversation) {
            this.list.set(id, conversation);
            this.#emit("list");
        }
        return conversation;
    }

    async #changed({ id, message_id: messageID, invitation }) {
        await this.get(id).catch(() => this.refresh());
        if (invitation) return;
        if (messageID) {
            await this.loadNewer(id).catch(() => {});
            this.#emit("incoming", { id, messageID });
        }
    }

    // --- Activity --------------------------------------------------------------------

    #typing({ conversation_id: id, unique_id: uid, nickname }) {
        if (!uid || uid === this.uid) return;
        if (!this.typing.has(id)) this.typing.set(id, new Map());
        this.typing.get(id).set(uid, { nickname: nickname || "", until: Date.now() + GROUP_TYPING_TTL_MS });
        this.#emit("typing", { id });
    }

    /** Nicknames of the members typing in a group now. */
    typingNames(id, now = Date.now()) {
        return [...(this.typing.get(id)?.values() || [])].filter(entry => entry.until > now).map(entry => entry.nickname);
    }

    #seen({ conversation_id: id, unique_id: uid, message_id: messageID }) {
        const member = this.list.get(id)?.members?.find(m => m.unique_id === uid && !m.pending);
        const position = Number(messageID) || 0;
        if (!member || position <= (Number(member.seen_message_id) || 0)) return;
        member.seen_message_id = position;
        this.#emit("seen", { id });
    }

    /** Announces typing, at most every 3 s, unless activity is private. */
    sendTyping(id, now = Date.now()) {
        if (!this.shareActivity() || !this.activityAvailable || !this.conn.open) return false;
        if (this.#typingSent.has(id) && now - this.#typingSent.get(id) < TYPING_PING_MS) return false;
        this.#typingSent.set(id, now);
        this.conn.send(Msg.Typing, { conversation_id: id });
        return true;
    }

    // --- Reading -------------------------------------------------------------------

    async #senderKey(message) {
        const entry = await this.dm.devicesOf(message.from_unique_id).catch(() => null);
        if (!entry || entry.trust !== "ok") return null;
        if (message.sender_device_id) {
            let device = entry.devices.find(d => d.deviceID === message.sender_device_id && d.x25519B64 === message.sender_device_key);
            if (!device) {
                const fresh = await this.dm.devicesOf(message.from_unique_id, { refresh: true }).catch(() => null);
                device = fresh?.devices.find(d => d.deviceID === message.sender_device_id && d.x25519B64 === message.sender_device_key);
            }
            if (device) return device.x25519;
            return this.dm.historicalSenderKey(message.from_unique_id, message.sender_device_id,
                message.sender_device_key, message.sender_device_chain).catch(() => null);
        }
        // An older desktop sealed with the account key: the first device's.
        return entry.devices.find(d => d.first)?.x25519 || null;
    }

    async #open(conversationID, message) {
        if (!message.body) return { ...message, text: "", deleted: true };
        if (!message.device_envelope) return { ...message, text: null };
        const key = await this.#senderKey(message);
        const plain = key ? openDM(message.body, key, this.session.identity.xSecret) : null;
        let text = null;
        try {
            const p = plain ? JSON.parse(plain) : null;
            if (p && p.conversation_id === conversationID && p.epoch === message.epoch && p.reference === message.reference &&
                p.from === message.from_unique_id && p.to === this.uid && typeof p.text === "string") text = p.text;
        } catch {
            text = null;
        }
        return { ...message, text };
    }

    #put(id, list) {
        // An accepted message replaces its local copy (same own reference).
        const accepted = new Set(list.filter(m => !m.local && m.from_unique_id === this.uid).map(m => m.reference));
        this.messages.set(id, list.filter(m => !m.local || !accepted.has(m.reference)).sort(byOrder));
        this.#emit("messages", { id });
    }

    async #store(id, page) {
        const opened = await Promise.all(page.map(message => this.#open(id, message)));
        const byID = new Map((this.messages.get(id) || []).map(m => [m.id, m]));
        const typing = this.typing.get(id);
        for (const message of opened) {
            byID.set(message.id, message);
            // A member whose message arrived is done typing it.
            if (typing?.delete(message.from_unique_id)) this.#emit("typing", { id });
        }
        this.#put(id, [...byID.values()]);
    }

    #newest(id) {
        return (this.messages.get(id) || []).filter(legacyID).at(-1)?.id || 0;
    }

    /** Loads one page of older messages; returns whether more exist. */
    async loadOlder(id) {
        const oldest = (this.messages.get(id) || []).find(legacyID)?.id || 0;
        const result = await this.#request({ action: "history", id, before_id: oldest });
        await this.#store(id, result.messages || []);
        return (result.messages || []).length === 25;
    }

    /** Loads messages newer than the newest one shown. */
    async loadNewer(id) {
        const newest = this.#newest(id);
        if (!newest) return this.loadOlder(id);
        let before = 0;
        for (;;) {
            const result = await this.#request({ action: "history", id, before_id: before });
            const page = (result.messages || []).filter(m => m.id > newest);
            await this.#store(id, page);
            if (page.length < 25) return;
            before = page.at(-1).id;
        }
    }

    /** Answers an invitation (action accept or decline). */
    async answer(id, action) {
        const conversation = this.list.get(id) || await this.get(id);
        await this.#request({ action, id, revision: conversation.revision });
        return action === "accept" ? this.get(id) : this.refresh();
    }

    /**
     * Marks the newest message read. While activity is shared the read
     * position is shared too ("Seen by" for the others).
     */
    markRead(id) {
        const newest = this.#newest(id);
        if (!newest || this.#readSent.get(id) >= newest) return;
        this.#readSent.set(id, newest);
        const share = this.shareActivity() && this.activityAvailable;
        this.#request({ action: "mark_read", id, read_message_id: newest, ...(share ? { share_read: true } : {}) })
            .catch(() => this.#readSent.delete(id));
    }

    // --- Sending -----------------------------------------------------------------------

    /** Adds or updates an own message that the server has not accepted yet. */
    #local(id, { reference, text, sentAt, ...state }) {
        const prior = this.messages.get(id) || [];
        // A projected own record (it has a key) is committed: a changed draft
        // never replaces its text, even while it is still pending.
        const committed = prior.find(m => m.local && m.key && m.reference === reference && m.from_unique_id === this.uid);
        if (committed) text = committed.text;
        const list = prior.filter(m => !(m.local && m.reference === reference));
        if (list.some(m => !m.local && m.reference === reference && m.from_unique_id === this.uid)) return;
        // The replacement keeps the committed record's key, so a later changed
        // draft still finds it committed (never painted as pending).
        list.push({ id: `local:${reference}`, local: true, reference, conversation_id: id, from_unique_id: this.uid, text, created_at: sentAt,
            pending: false, queued: false, failed: false, ...state, ...(committed ? { key: committed.key, protocol: committed.protocol } : {}) });
        this.#put(id, list);
    }

    /** Shows an own message waiting in the outbox. */
    showQueued(id, { reference, text, sentAt, expired = false }) {
        this.#local(id, { reference, text, sentAt, queued: !expired, failed: expired });
    }

    /** Removes a message that never reached the server. */
    discardLocal(id, reference) {
        this.#put(id, (this.messages.get(id) || []).filter(m => !(m.local && m.reference === reference)));
    }

    /**
     * Sends text to every accepted member. reference is the client message
     * ID: the server stores a reference once, so a resend never duplicates.
     * Lost connections throw with retryable set (for the outbox).
     */
    async send(id, text, { reference = crypto.randomUUID(), sentAt = Date.now() / 1000 } = {}, retried = false) {
        if (this.ratchet) return this.#sendRatchet(id, text, reference, sentAt);
        this.#local(id, { reference, text, sentAt, pending: true });
        try {
            const conversation = await this.get(id);
            if (!conversation) throw Object.assign(new Error("group not found"), { code: "unavailable" });
            const own = await this.dm.devicesOf(this.uid, { refresh: retried });
            if (!own.devices.some(d => d.deviceID === this.session.deviceID)) throw Object.assign(new Error("not confirmed"), { code: "unconfirmed" });
            const envelopes = {};
            const deviceEnvelopes = {};
            for (const member of conversation.members.filter(m => !m.pending)) {
                const entry = member.unique_id === this.uid ? own : await this.dm.devicesOf(member.unique_id, { refresh: retried });
                const first = entry.devices.find(d => d.first);
                if (entry.trust !== "ok" || !first) throw Object.assign(new Error("identity unavailable"), { code: "unavailable" });
                const plain = JSON.stringify({ conversation_id: id, epoch: conversation.epoch, reference, from: this.uid, to: member.unique_id, text });
                envelopes[member.unique_id] = sealDM(plain, first.x25519, this.session.identity.xSecret);
                for (const device of entry.devices) {
                    // Including this device lets it reread its own messages later.
                    if (device.first) continue;
                    deviceEnvelopes[`${member.unique_id}/${device.deviceID}`] = sealDM(plain, device.x25519, this.session.identity.xSecret);
                }
            }
            const result = await this.#request({ action: "send", id, revision: conversation.revision,
                message: { epoch: conversation.epoch, reference, envelopes, device_envelopes: deviceEnvelopes } });
            const messages = (this.messages.get(id) || []).filter(m => m.id !== result.message_id);
            messages.push({ id: result.message_id, conversation_id: id, epoch: conversation.epoch, from_unique_id: this.uid, reference, text, created_at: Date.now() / 1000 });
            this.#put(id, messages);
            return result.message_id;
        } catch (error) {
            if (!retried && error instanceof ProtocolError && (error.devicesStale || /conflict|revision|epoch/i.test(error.message))) {
                return this.send(id, text, { reference, sentAt }, true);
            }
            // Lost connections leave the message for the outbox; refusals fail it.
            const retryable = !(error instanceof ProtocolError) && !error.code;
            this.#local(id, { reference, text, sentAt, queued: retryable, failed: !retryable });
            throw Object.assign(error, { retryable });
        }
    }

    // --- noxa-olm-v1 (explicitly enabled sessions only) --------------------

    /** Routes private group sends through the session's durable consumer. */
    async attachRatchet(consumer) {
        this.ratchet = consumer;
        const owned = () => this.ratchet === consumer && !consumer.closed;
        consumer.project((record, { isNew }) => { if (owned()) this.#project(record, isNew); });
        const history = await consumer.history();
        for (const record of history) {
            if (!owned()) return;
            if (record.scope === "group") this.#project(record, false);
        }
    }

    /** Truthful locked placeholders for unopenable group copies (never ACKed). */
    showQuarantined(list = []) {
        const current = new Set();
        for (const q of list) {
            if (q.scope !== "group" || !q.conversation) continue;
            const id = "quarantine:" + q.id;
            current.add(id);
            const messages = this.messages.get(q.conversation) || [];
            if (messages.some(m => m.id === id)) continue;
            this.#put(q.conversation, [...messages, { id, protocol: RATCHET_PROTOCOL, conversation_id: q.conversation, from_unique_id: q.sender,
                text: null, quarantined: q.code, created_at: q.seenAt || Date.now() / 1000 }]);
        }
        for (const [id, messages] of this.messages) {
            const keep = messages.filter(m => !m.quarantined || current.has(m.id));
            if (keep.length !== messages.length) this.#put(id, keep);
        }
    }

    #project(record, isNew) {
        if (record.scope !== "group" || record.protocol !== RATCHET_PROTOCOL || !record.conversation) return;
        const id = record.conversation, list = [...(this.messages.get(id) || [])];
        const text = typeof record.body?.text === "string" ? record.body.text : "";
        if (record.purpose === "delete" || record.purpose === "edit") {
            // An own change shows only once accepted (linked copies carry no status).
            if (record.self && record.status && record.status !== "accepted") return;
            // Authenticated sender + logical reference; a different sender that
            // reused the same logical string can never match.
            const index = list.findIndex(m => m.key === record.referenceKey);
            if (index < 0) {
                this.#waiting.delete(record.referenceKey);
                this.#waiting.set(record.referenceKey, record);
                if (this.#waiting.size > MAX_WAITING_CHANGES) this.#waiting.delete(this.#waiting.keys().next().value);
                return;
            }
            list[index] = record.purpose === "delete" ? { ...list[index], text: "", deleted: true } : { ...list[index], text, edited: true };
            this.#put(id, list);
            return;
        }
        const key = "olm:" + record.key, own = record.sender === this.uid;
        if (record.transport?.delivery) {
            const placeholder = list.findIndex(m => m.id === "quarantine:" + record.transport.delivery);
            if (placeholder >= 0) list.splice(placeholder, 1);
        }
        const index = list.findIndex(m => m.id === key);
        const status = record.status || (own ? "" : "received");
        const message = { id: key, key: record.key, protocol: RATCHET_PROTOCOL, reference: record.logicalID, conversation_id: id,
            from_unique_id: record.sender, text, created_at: record.sentAt, transport: record.transport || {},
            ...(own && status === "pending" ? { local: true, pending: true } : {}),
            ...(own && (status === "rejected" || status === "aborted") ? { local: true, failed: true } : {}) };
        if (index >= 0) {
            // Keep later authenticated edits/deletes over a replayed original.
            const prior = list[index];
            list[index] = { ...message, ...(prior.deleted ? { text: "", deleted: true } : prior.edited ? { text: prior.text, edited: true } : {}) };
        } else list.push(message);
        if (message.local) message.id = "local:" + record.logicalID;
        this.#put(id, list.filter(m => !(m.local && m.reference === record.logicalID && m.from_unique_id === this.uid && m !== message)));
        const waiting = this.#waiting.get(record.key);
        if (waiting) { this.#waiting.delete(record.key); this.#project(waiting, false); }
        if (isNew && !own) this.#emit("incoming", { id, messageID: key });
    }

    #waiting = new Map();

    async #sendRatchet(id, text, reference, sentAt) {
        // Committed = any projected own record of this reference, including a
        // still-pending one (local + key), never only a local draft placeholder.
        const committed = (this.messages.get(id) || []).find(m => m.reference === reference && m.from_unique_id === this.uid && m.protocol === RATCHET_PROTOCOL && (m.key || !m.local));
        if (!committed) this.#local(id, { reference, text, sentAt, pending: true, protocol: RATCHET_PROTOCOL });
        const consumer = this.ratchet;
        try {
            // Returns the typed {record, receipt, reused}; receipt.message_id
            // is an opaque transport UUID, NOT a legacy numeric message ID.
            const outcome = await consumer.send({ logicalID: reference, scope: "group", conversation: id, body: { text, sentAt } });
            if (outcome.reused && (outcome.record?.body?.text ?? "") !== text) outcome.mismatch = true;
            return outcome;
        } catch (error) {
            const original = error.committed;
            // An accepted receipt is success even if later housekeeping failed.
            if (original?.receipt?.state === "accepted") {
                const committedText = original.record?.body?.text ?? "";
                // The projection never ran: paint the ORIGINAL committed text
                // as sent, never this (possibly changed) draft as pending.
                if (this.ratchet === consumer && !consumer.closed) this.#local(id, { reference, text: committedText, sentAt: original.record?.sentAt || sentAt, protocol: RATCHET_PROTOCOL });
                const outcome = { record: original.record, receipt: original.receipt, reused: original.reused };
                if (original.reused && committedText !== text) outcome.mismatch = true;
                return outcome;
            }
            const retryable = ratchetRetryable(error);
            this.#local(id, { reference, text: original ? original.record.body?.text ?? "" : text, sentAt: original?.record.sentAt || sentAt,
                queued: retryable, failed: !retryable, protocol: RATCHET_PROTOCOL });
            throw Object.assign(error, { retryable });
        }
    }

    close() {
        this.session.removeEventListener("server-event", this.#onEvent);
    }
}

function waitForGroupRetry(ms, signal) {
    return new Promise((resolve,reject) => {
        const finish = () => { signal?.removeEventListener("abort", abort); resolve(); };
        const timer = setTimeout(finish, ms);
        const abort = () => { clearTimeout(timer); signal?.removeEventListener("abort", abort); reject(signal.reason); };
        signal?.addEventListener("abort", abort, { once: true });
        if (signal?.aborted) abort();
    });
}
