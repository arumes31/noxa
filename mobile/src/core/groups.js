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
import { ProtocolError } from "./connection.js";
import { openDM, sealDM } from "./crypto.js";

const MsgConversationRequest = 170;
const MsgConversationResult = 171;

export class Groups extends EventTarget {
    constructor(session, dm) {
        super();
        this.session = session;
        this.dm = dm;
        this.conn = session.conn;
        this.uid = session.uniqueID;
        /** id -> Conversation */
        this.list = new Map();
        /** id -> messages, oldest first */
        this.messages = new Map();
        this.#onEvent = event => {
            const { type, data } = event.detail;
            if (type === "conversation_changed" && data?.id) this.#changed(data).catch(() => {});
        };
        session.addEventListener("server-event", this.#onEvent);
    }

    #onEvent;

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    #request(request) {
        return this.conn.request(MsgConversationRequest, request, MsgConversationResult, { accept: r => r.action === request.action });
    }

    /** Refreshes the list of groups this account belongs to. */
    async refresh() {
        const result = await this.#request({ action: "list" });
        this.list = new Map((result.conversations || []).map(c => [c.id, c]));
        this.#emit("list");
        return [...this.list.values()];
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

    async #store(id, page) {
        const opened = await Promise.all(page.map(message => this.#open(id, message)));
        const byID = new Map((this.messages.get(id) || []).map(m => [m.id, m]));
        for (const message of opened) byID.set(message.id, message);
        this.messages.set(id, [...byID.values()].sort((a, b) => a.id - b.id));
        this.#emit("messages", { id });
    }

    /** Loads one page of older messages; returns whether more exist. */
    async loadOlder(id) {
        const oldest = this.messages.get(id)?.[0]?.id || 0;
        const result = await this.#request({ action: "history", id, before_id: oldest });
        await this.#store(id, result.messages || []);
        return (result.messages || []).length === 25;
    }

    /** Loads messages newer than the newest one shown. */
    async loadNewer(id) {
        const known = this.messages.get(id);
        if (!known?.length) return this.loadOlder(id);
        const newest = known.at(-1).id;
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

    markRead(id) {
        const newest = this.messages.get(id)?.at(-1)?.id;
        if (!newest) return;
        this.#request({ action: "mark_read", id, read_message_id: newest }).catch(() => {});
    }

    // --- Sending -----------------------------------------------------------------------

    async send(id, text, retried = false) {
        const conversation = await this.get(id);
        if (!conversation) throw new Error("group not found");
        const own = await this.dm.devicesOf(this.uid, { refresh: retried });
        if (!own.devices.some(d => d.deviceID === this.session.deviceID)) throw Object.assign(new Error("not confirmed"), { code: "unconfirmed" });
        const reference = this.pendingReference || crypto.randomUUID();
        this.pendingReference = reference;
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
        try {
            const result = await this.#request({ action: "send", id, revision: conversation.revision,
                message: { epoch: conversation.epoch, reference, envelopes, device_envelopes: deviceEnvelopes } });
            this.pendingReference = null;
            const messages = this.messages.get(id) || [];
            if (!messages.some(m => m.id === result.message_id)) {
                messages.push({ id: result.message_id, conversation_id: id, epoch: conversation.epoch, from_unique_id: this.uid, reference, text, created_at: Date.now() / 1000 });
                this.messages.set(id, messages);
            }
            this.#emit("messages", { id });
            return result.message_id;
        } catch (error) {
            if (!retried && error instanceof ProtocolError && (error.devicesStale || /conflict|revision|epoch/i.test(error.message))) return this.send(id, text, true);
            this.pendingReference = null;
            throw error;
        }
    }

    close() {
        this.session.removeEventListener("server-event", this.#onEvent);
    }
}
