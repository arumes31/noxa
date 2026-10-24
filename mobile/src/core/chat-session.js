// chat-session.js — signed-in state for the mobile app: the per-viewer tree
// snapshot, scope keys, channel subscriptions, history and sending.
//
// Scope 0 is the global chat. Channel bodies are secretbox-sealed with the
// scope's current key generation; keys arrive sealed to this device's
// X25519 key in the AuthResponse (global), MsgChannelKey pushes (join,
// subscribe, rotation) and history pages (archival generations).
import { fromBase64, openScope, openSealed, sealScope } from "./crypto.js";
import { ProtocolError } from "./connection.js";
import { Msg } from "./frame.js";

export const MsgChatAccepted = 154;
export const MsgChannelJoined = 158;

const HISTORY_PAGE = 50;

export class ChatSession extends EventTarget {
    constructor(conn, identity, auth) {
        super();
        this.conn = conn;
        this.identity = identity;
        this.clientID = auth.client_id;
        this.uniqueID = auth.unique_id;
        this.deviceID = auth.device_id || "";
        this.nickname = auth.nickname;
        this.capabilities = new Set(auth.capabilities || []);
        this.iceServers = auth.ice_servers || [];
        this.snapshot = null;
        this.subscriptions = new Set();
        /** scope -> Map(keyID -> Uint8Array) */
        this.keys = new Map();
        /** scope -> keyID of the current generation */
        this.current = new Map();
        /** scope -> messages, oldest first */
        this.messages = new Map();
        this.typing = new Map();
        for (const key of auth.chat_keys || []) this.#installKey(key, true);
        this.#unsubscribe = [
            conn.on(Msg.Snapshot, snapshot => this.#setSnapshot(snapshot)),
            conn.on(Msg.ChannelKey, key => this.#installKey(key, true)),
            conn.on(Msg.ChatKeyBundle, bundle => (bundle.keys || []).forEach(key => this.#installKey(key, false))),
            conn.on(Msg.SubscriptionState, state => {
                this.subscriptions = new Set(state.channel_ids || []);
                this.#emit("subscriptions");
            }),
            conn.on(Msg.Event, event => this.#handleEvent(event)),
            conn.on(Msg.DeviceEvent, event => this.#emit("device", event)),
        ];
    }

    #unsubscribe = [];

    close() {
        this.#unsubscribe.forEach(stop => stop());
        this.conn.close();
    }

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    #installKey({ channel_id: scope = 0, key_id: keyID, sealed_key: sealed }, current) {
        const key = openSealed(fromBase64(sealed), this.identity.xPublic, this.identity.xSecret);
        if (!key || key.length !== 32) return;
        if (!this.keys.has(scope)) this.keys.set(scope, new Map());
        this.keys.get(scope).set(keyID, key);
        if (current && keyID >= (this.current.get(scope) || 0)) this.current.set(scope, keyID);
        this.#emit("keys", { scope });
    }

    #setSnapshot(snapshot) {
        this.snapshot = snapshot;
        this.#emit("snapshot", snapshot);
    }

    /** Flattened channel list in tree order: {id, name, depth, textOnly, clients}. */
    channels() {
        const out = [];
        const walk = (nodes, depth) => {
            for (const node of nodes || []) {
                out.push({
                    id: node.ChannelID, name: node.Name, depth, textOnly: !!node.TextOnly, topic: node.Topic || "",
                    clients: node.clients || [], restricted: !!node.access?.restricted,
                    // The home channel (Lobby) has no chat of its own.
                    chat: node.SystemKind !== "home",
                });
                walk(node.children, depth + 1);
            }
        };
        walk(this.snapshot?.root_channels, 0);
        return out;
    }

    /** Every visible client, in and outside channels. */
    clients() {
        return [...this.channels().flatMap(c => c.clients), ...(this.snapshot?.unassigned_clients || [])];
    }

    /** Scopes this device can chat in: global when its key arrived, then channels. */
    chatScopes() {
        const scopes = this.current.has(0) ? [0] : [];
        return scopes.concat(this.channels().filter(c => c.chat).map(c => c.id));
    }

    decrypt(scope, keyID, body) {
        const key = this.keys.get(scope)?.get(keyID);
        if (!key) return null;
        return openScope(body, key);
    }

    #store(scope, message) {
        const list = this.messages.get(scope) || [];
        // The server's echo of an own message can arrive before or after its
        // acknowledgement; both carry the client message ID.
        const existing = list.findIndex(m => (message.id && m.id === message.id) || (message.clientMsgID && m.clientMsgID === message.clientMsgID));
        if (existing >= 0) list[existing] = { ...list[existing], ...message, pending: false };
        else list.push(message);
        list.sort((a, b) => (a.sentAt - b.sentAt) || ((a.id || Infinity) - (b.id || Infinity)));
        this.messages.set(scope, list);
        this.#emit("messages", { scope });
    }

    #handleEvent({ type, data }) {
        if (!data) return;
        this.#emit("server-event", { type, data });
        switch (type) {
            case "user_joined":
            case "user_moved":
            case "user_left":
            case "audio_state_changed":
            case "status_changed":
                if (this.#applyPresence(type, data)) this.#emit("snapshot", this.snapshot);
                return;
            case "chat": {
                if (data.direct) {
                    this.#emit("direct", data);
                    return;
                }
                const scope = Number(data.channel_id || 0);
                const text = this.decrypt(scope, data.key_id, data.text);
                if (text === null) this.requestKeys(scope, [data.key_id]);
                const message = {
                    id: data.id, clientMsgID: data.client_msg_id, fromUniqueID: data.from_unique_id, from: data.from,
                    text, keyID: data.key_id, body: data.text, replyTo: data.reply_to_id || 0, sentAt: Date.now() / 1000,
                    own: data.from_unique_id === this.uniqueID,
                    mentioned: (data.mentions || []).includes(this.uniqueID) || (data.role_mentions || []).includes(this.uniqueID),
                };
                this.#store(scope, message);
                this.#emit("incoming", { scope, message });
                return;
            }
            case "chat_edited": {
                const scope = Number(data.channel_id || 0);
                const message = this.messages.get(scope)?.find(m => m.id === data.message_id);
                if (message) this.#store(scope, { ...message, text: this.decrypt(scope, data.key_id, data.body), edited: true });
                return;
            }
            case "chat_deleted": {
                const scope = Number(data.channel_id || 0);
                const message = this.messages.get(scope)?.find(m => m.id === data.message_id);
                if (message) this.#store(scope, { ...message, text: "", deleted: true });
                return;
            }
            case "chat_reaction": {
                const scope = Number(data.channel_id || 0);
                const message = this.messages.get(scope)?.find(m => m.id === data.message_id);
                if (message) this.#store(scope, { ...message, reactions: data.reactions || {} });
                return;
            }
            case "typing": {
                if (data.unique_id === this.uniqueID || data.direct) return;
                const scope = Number(data.channel_id || 0);
                this.typing.set(scope, { nickname: data.nickname, until: Date.now() + 5000 });
                this.#emit("typing", { scope });
                return;
            }
        }
    }

    /** Updates the tree for presence events; returns whether it changed. */
    #applyPresence(type, data) {
        const snapshot = this.snapshot;
        if (!snapshot || !data.client_id) return false;
        const lists = [snapshot.unassigned_clients ||= []];
        const nodes = new Map();
        const walk = list => {
            for (const node of list || []) {
                nodes.set(node.ChannelID, node);
                lists.push(node.clients ||= []);
                walk(node.children);
            }
        };
        walk(snapshot.root_channels);
        let client = null;
        for (const list of lists) {
            const index = list.findIndex(c => c.client_id === data.client_id);
            if (index >= 0) {
                client = list[index];
                if (type === "user_left" || type === "user_moved") list.splice(index, 1);
            }
        }
        if (type === "user_left") return !!client;
        if (type === "audio_state_changed" || type === "status_changed") {
            if (!client) return false;
            if ("muted" in data) client.self_muted = !!data.muted;
            if ("deafened" in data) client.self_deafened = !!data.deafened;
            if ("status" in data) client.status = data.status;
            return true;
        }
        if (type === "user_joined" && client) return false;
        client ||= { client_id: data.client_id, unique_id: data.unique_id, nickname: data.nickname };
        if (data.nickname) client.nickname = data.nickname;
        if (data.on_mobile !== undefined) client.on_mobile = !!data.on_mobile;
        const channelID = data.channel_id || 0;
        client.channel_id = channelID;
        (nodes.get(channelID)?.clients || snapshot.unassigned_clients).push(client);
        return true;
    }

    /**
     * ICE servers with current TURN credentials. Servers with managed TURN
     * lease them per session (193 RelayRequest); the lease is renewed when
     * its refresh time passes.
     */
    async relayServers() {
        if (!this.capabilities.has("managed-relay-v1")) return this.iceServers;
        const now = Date.now();
        if (!this.#relay || now >= this.#relay.refreshAt) {
            const lease = await this.conn.request(193, { action: "refresh" }, 194, { accept: r => r.action === "refresh" });
            const skew = now - (lease.server_time || now);
            this.#relay = { servers: lease.ice_servers || [], refreshAt: (lease.refresh_at || 0) + skew || now + 15 * 60 * 1000 };
            this.#scheduleRelay();
        }
        return [...this.iceServers.filter(server => !server.username), ...this.#relay.servers];
    }

    #relay = null;
    #relayListeners = new Set();
    #relayTimer = 0;

    /**
     * Calls listener with fresh ICE servers whenever the relay lease is
     * renewed, so long calls keep working past the credentials' lifetime.
     * Returns a function that stops listening.
     */
    onRelayRenewal(listener) {
        this.#relayListeners.add(listener);
        this.#scheduleRelay();
        return () => {
            this.#relayListeners.delete(listener);
            if (!this.#relayListeners.size) clearTimeout(this.#relayTimer);
        };
    }

    #scheduleRelay() {
        clearTimeout(this.#relayTimer);
        if (!this.#relay || !this.#relayListeners.size) return;
        this.#relayTimer = setTimeout(async () => {
            if (!this.conn.open) return;
            this.#relay = null;
            try {
                const servers = await this.relayServers();
                for (const listener of this.#relayListeners) listener(servers);
            } catch {
                setTimeout(() => this.#scheduleRelay(), 30000);
            }
        }, Math.max(5000, this.#relay.refreshAt - Date.now()));
    }

    requestKeys(scope, keyIDs) {
        const missing = keyIDs.filter(id => id && !this.keys.get(scope)?.has(id));
        if (missing.length && this.conn.open) this.conn.send(Msg.ChatKeyRequest, { channel_id: scope, key_ids: missing.slice(0, 64) });
    }

    /** Subscribes to text channels so their chat and keys reach this device. */
    async subscribe(channelIDs, subscribe = true) {
        const ids = channelIDs.filter(id => id > 0);
        if (!ids.length) return;
        const state = await this.conn.request(Msg.ChannelSubscribe, { channel_ids: ids.slice(0, 64), subscribe }, Msg.SubscriptionState);
        this.subscriptions = new Set(state.channel_ids || []);
    }

    /** Loads one page of older history for a scope. */
    async loadHistory(scope) {
        const list = this.messages.get(scope) || [];
        const before = list.find(m => m.id)?.id || 0;
        const page = await this.conn.request(Msg.ChatHistory, { channel_id: scope, before_id: before, limit: HISTORY_PAGE },
            Msg.ChatHistoryResponse, { accept: r => r.channel_id === scope });
        for (const key of page.keys || []) this.#installKey(key, false);
        for (const entry of page.messages || []) {
            this.#store(scope, {
                id: entry.id, fromUniqueID: entry.from_unique_id, from: entry.from_nickname, keyID: entry.key_id,
                body: entry.body_enc, text: entry.deleted ? "" : this.decrypt(scope, entry.key_id, entry.body_enc),
                sentAt: entry.sent_at, edited: !!entry.edited_at, deleted: !!entry.deleted, reactions: entry.reactions || {},
                replyTo: entry.reply_to_id || 0, own: entry.from_unique_id === this.uniqueID,
            });
        }
        return (page.messages || []).length === HISTORY_PAGE;
    }

    /**
     * Sends a channel (scope > 0) or global (scope 0) message with the
     * scope's current key and waits for the server's acceptance.
     */
    async send(scope, text, { replyTo = 0, clientMsgID = crypto.randomUUID(), sentAt = Date.now() / 1000 } = {}) {
        this.#store(scope, { clientMsgID, fromUniqueID: this.uniqueID, from: this.nickname, text, sentAt, own: true, pending: true, queued: false, replyTo });
        const keyID = this.current.get(scope);
        const key = keyID && this.keys.get(scope)?.get(keyID);
        if (!key) {
            this.#store(scope, { clientMsgID, pending: false, queued: true });
            throw Object.assign(new Error("This chat's key has not arrived yet."), { retryable: true });
        }
        const request = { ack_requested: true, text: sealScope(text, key), enc: true, key_id: keyID, client_msg_id: clientMsgID, reply_to_id: replyTo || undefined };
        if (scope > 0) request.channel_id = String(scope);
        try {
            const accepted = await this.conn.request(Msg.ChatSend, request, MsgChatAccepted, { accept: r => r.client_msg_id === clientMsgID });
            this.#store(scope, { clientMsgID, id: accepted.message_id, pending: false });
            return accepted;
        } catch (error) {
            // A lost connection leaves the message for the outbox to retry.
            const retryable = !(error instanceof ProtocolError);
            this.#store(scope, { clientMsgID, failed: !retryable, queued: retryable, pending: false });
            throw Object.assign(error, { retryable });
        }
    }

    /** Shows an own message waiting in the outbox. */
    showQueued(scope, { clientMsgID, text, sentAt, expired = false }) {
        this.#store(scope, { clientMsgID, fromUniqueID: this.uniqueID, from: this.nickname, text, sentAt, own: true, queued: !expired, failed: expired, pending: false });
    }

    sendTyping(scope) {
        if (this.conn.open) this.conn.send(Msg.Typing, scope > 0 ? { channel_id: scope } : {});
    }

    /** Joins a voice channel (0 leaves). Text-only channels are subscribed instead. */
    join(channelID) {
        return this.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: channelID }, MsgChannelJoined);
    }
}
