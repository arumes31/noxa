// chat-session.js — signed-in state for the mobile app: the per-viewer tree
// snapshot, scope keys, channel subscriptions, history and sending, plus the
// message actions (edit, delete, react, pin, polls) and server lookups the
// screens share.
//
// Scope 0 is the global chat. Channel bodies are secretbox-sealed with the
// scope's current key generation; keys arrive sealed to this device's
// X25519 key in the AuthResponse (global), MsgChannelKey pushes (join,
// subscribe, rotation) and history pages (archival generations).
import { fromBase64, openScope, openSealed, sealScope } from "./crypto.js";
import { ProtocolError } from "./connection.js";
import { Msg } from "./frame.js";
import { joinLeaveLog } from "./join-leave.js";
import { keywordHit } from "./mentions.js";
import * as settings from "./settings.js";

export const MsgChatAccepted = Msg.ChatAccepted;
export const MsgChannelJoined = Msg.ChannelJoined;

const HISTORY_PAGE = 50;
/** Polls travel inside the sealed body with this prefix (internal/netproto/poll.go). */
export const POLL_PREFIX = "[noxa-poll:v1]";

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
        this.mediaLimits = auth.media_limits || null;
        this.snapshot = null;
        this.subscriptions = new Set();
        /** scope -> Map(keyID -> Uint8Array) */
        this.keys = new Map();
        /** scope -> keyID of the current generation */
        this.current = new Map();
        /** scope -> messages, oldest first */
        this.messages = new Map();
        /** scope -> {nickname, until} */
        this.typing = new Map();
        /** scope -> pinned message IDs (loaded with loadPins). */
        this.pins = new Map();
        /** The server announcement, decrypted: {text, at} or null. */
        this.announcement = null;
        /** Server rules awaiting acceptance ({text, hash}) or null. */
        this.pendingRules = null;
        /** The MOTD, decrypted when sealed. */
        this.motd = auth.motd_enc ? null : auth.motd || "";
        for (const key of auth.chat_keys || []) this.#installKey(key, true);
        if (auth.motd_enc) this.motd = this.decrypt(0, auth.motd_key_id, auth.motd) ?? "";
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
            conn.on(Msg.ServerRules, rules => this.#setRules(rules)),
            conn.on(Msg.MediaLimitsChanged, limits => { this.mediaLimits = limits.media_limits || limits; this.#emit("media-limits"); }),
        ];
        // Join/leave lines for the channel chats are derived from presence events.
        joinLeaveLog(this);
    }

    #unsubscribe = [];

    close() {
        clearTimeout(this.#relayTimer);
        this.#relayListeners.clear();
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
        // Bodies that arrived before their key can be opened now.
        const list = this.messages.get(scope);
        if (list?.some(m => m.text === null && m.keyID === keyID)) {
            for (const m of list) if (m.text === null && m.keyID === keyID && m.body) m.text = openScope(m.body, key);
            this.#emit("messages", { scope });
        }
        this.#emit("keys", { scope });
    }

    /** Installs a sealed key a page carried (archival unless current is set). */
    installKey(key, current = false) {
        this.#installKey(key, current);
    }

    #setSnapshot(snapshot) {
        this.snapshot = snapshot;
        this.#emit("snapshot", snapshot);
    }

    /** Flattened channel list in tree order: {id, name, depth, textOnly, clients}. */
    channels() {
        const out = [];
        const walk = (nodes, depth, parent) => {
            for (const node of nodes || []) {
                out.push({
                    id: node.ChannelID, name: node.Name, depth, parent, textOnly: !!node.TextOnly, topic: node.Topic || "",
                    description: node.Description || "", maxClients: node.MaxClients || 0, slowMode: node.SlowModeSeconds || 0,
                    hasPassword: !!node.HasPassword, lifetime: node.Lifetime || "",
                    clients: node.clients || [], restricted: !!node.access?.restricted, canConnect: node.access?.can_connect !== false,
                    canManageAccess: !!node.access?.can_manage_access, canManageVoiceQueue: !!node.access?.can_manage_voice_queue,
                    // The home channel (Lobby) has no chat of its own.
                    chat: node.SystemKind !== "home",
                    home: node.SystemKind === "home",
                    node,
                });
                walk(node.children, depth + 1, node.ChannelID);
            }
        };
        walk(this.snapshot?.root_channels, 0, 0);
        return out;
    }

    /** Every visible client, in and outside channels. */
    clients() {
        return [...this.channels().flatMap(c => c.clients), ...(this.snapshot?.unassigned_clients || [])];
    }

    /** This session's own entry in the tree, if visible. */
    self() {
        return this.clients().find(c => c.client_id === this.clientID) || null;
    }

    /** The recipient's authority: "owner", "administrator", "member" or "". */
    get authority() {
        return this.snapshot?.own_authority || "";
    }

    get isAdmin() {
        return this.authority === "owner" || this.authority === "administrator";
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

    messageByID(scope, id) {
        return this.messages.get(scope)?.find(m => m.id === id) || null;
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
                    version: data.version || 0, own: data.from_unique_id === this.uniqueID,
                    mentioned: (data.mentions || []).includes(this.uniqueID) || (data.role_mentions || []).includes(this.uniqueID),
                    mentions: data.mentions || [], roleMentions: data.role_mentions || [],
                };
                // A highlight keyword (settings) counts as a mention for unread badges.
                if (!message.own) message.keyword = keywordHit(text, settings.get("keywords"));
                this.#store(scope, message);
                this.#emit("incoming", { scope, message });
                return;
            }
            case "chat_edited": {
                const scope = Number(data.channel_id || 0);
                const message = this.messageByID(scope, data.message_id);
                if (message) this.#store(scope, { ...message, text: this.decrypt(scope, data.key_id, data.body), body: data.body, keyID: data.key_id, edited: true, version: data.version || message.version });
                return;
            }
            case "chat_deleted": {
                const scope = Number(data.channel_id || 0);
                const message = this.messageByID(scope, data.message_id);
                if (message) this.#store(scope, { ...message, text: "", deleted: true });
                return;
            }
            case "chat_reaction": {
                const scope = Number(data.channel_id || 0);
                const message = this.messageByID(scope, data.message_id);
                if (message) {
                    const mine = new Set(message.myReactions || []);
                    if (data.by === this.uniqueID && data.emoji) data.added ? mine.add(data.emoji) : mine.delete(data.emoji);
                    this.#store(scope, { ...message, reactions: data.reactions || {}, myReactions: [...mine] });
                }
                return;
            }
            case "chat_pinned":
            case "chat_unpinned": {
                const scope = Number(data.channel_id || 0);
                const pinned = type === "chat_pinned";
                const ids = new Set(this.pins.get(scope) || []);
                pinned ? ids.add(data.message_id) : ids.delete(data.message_id);
                this.pins.set(scope, [...ids]);
                const message = this.messageByID(scope, data.message_id);
                if (message) this.#store(scope, { ...message, pinned });
                this.#emit("pins", { scope });
                return;
            }
            case "poll_changed":
                this.#emit("poll", { scope: Number(data.channel_id || 0), messageID: data.message_id, version: data.version });
                return;
            case "announcement": {
                const text = data.enc ? this.decrypt(0, data.key_id, data.text) : data.text || "";
                this.announcement = text ? { text, at: Date.now() } : null;
                this.#emit("announcement", this.announcement);
                return;
            }
            case "typing": {
                if (data.unique_id === this.uniqueID) return;
                if (data.direct) {
                    this.#emit("typing-direct", { uid: data.unique_id, nickname: data.nickname, until: Date.now() + 5000 });
                    return;
                }
                const scope = Number(data.channel_id || 0);
                this.typing.set(scope, { nickname: data.nickname, until: Date.now() + 5000 });
                this.#emit("typing", { scope });
                return;
            }
            case "dm_delivered":
            case "dm_read":
                this.#emit("receipt", { kind: type === "dm_read" ? "read" : "delivered", from: data.from_unique_id, clientMsgID: data.client_msg_id });
                return;
            case "poke":
                this.#emit("poke", { fromClientID: data.from_client_id, from: data.from_nickname, message: data.message || "", at: Date.now() });
                return;
            case "whisper":
                this.#emit("whisper", data);
                return;
            case "kicked":
                this.#emit("kicked", data);
                return;
            case "emoji_added":
            case "emoji_removed":
            case "emoji_renamed":
                this.#emoji = null;
                this.#emit("emoji");
                return;
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
            if ("status_message" in data || "message" in data) client.status_message = data.status_message ?? data.message ?? "";
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
            const lease = await this.conn.request(Msg.RelayRequest, { action: "refresh" }, Msg.RelayResponse, { accept: r => r.action === "refresh" });
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
        this.#relayTimer = setTimeout(() => this.#renewRelay(), Math.max(5000, this.#relay.refreshAt - Date.now()));
    }

    async #renewRelay() {
        if (!this.conn.open || !this.#relayListeners.size) return;
        this.#relay = null;
        try {
            const servers = await this.relayServers();
            for (const listener of this.#relayListeners) listener(servers);
        } catch {
            // A failed refresh has no lease to schedule from. Retry the request
            // itself, and keep the timer cancellable when the session closes.
            if (this.conn.open && this.#relayListeners.size) {
                this.#relayTimer = setTimeout(() => this.#renewRelay(), 30000);
            }
        }
    }

    requestKeys(scope, keyIDs) {
        const missing = keyIDs.filter(id => id && !this.keys.get(scope)?.has(id));
        if (missing.length && this.conn.open) this.conn.send(Msg.ChatKeyRequest, { channel_id: scope, key_ids: missing.slice(0, 64) });
    }

    #subscribing = new Map();
    #subscriptionQueue = Promise.resolve();

    /** Batch every channel, coalesce duplicate intents and preserve intent order. */
    async subscribe(channelIDs, subscribe = true) {
        const ids = [...new Set(channelIDs)].filter(id => Number.isSafeInteger(id) && id > 0 && this.#subscribing.get(id)?.subscribe !== subscribe);
        if (!ids.length) return;
        const intent = { subscribe };
        ids.forEach(id => this.#subscribing.set(id, intent));
        const operation = this.#subscriptionQueue.then(async () => {
            try {
                for (let offset = 0; offset < ids.length; offset += 64) {
                    const state = await this.conn.request(Msg.ChannelSubscribe, { channel_ids: ids.slice(offset, offset + 64), subscribe }, Msg.SubscriptionState);
                    this.subscriptions = new Set(state.channel_ids || []);
                }
            } finally {
                ids.forEach(id => {
                    if (this.#subscribing.get(id) === intent) this.#subscribing.delete(id);
                });
            }
        });
        this.#subscriptionQueue = operation.catch(() => {});
        return operation;
    }

    #entryToMessage(scope, entry) {
        return {
            id: entry.id, fromUniqueID: entry.from_unique_id, from: entry.from_nickname, keyID: entry.key_id,
            body: entry.body_enc, text: entry.deleted ? "" : this.decrypt(scope, entry.key_id, entry.body_enc),
            sentAt: entry.sent_at, edited: !!entry.edited_at, deleted: !!entry.deleted, reactions: entry.reactions || {},
            replyTo: entry.reply_to_id || 0, version: entry.version || 0, own: entry.from_unique_id === this.uniqueID,
        };
    }

    /** Loads one page of older history for a scope. Returns whether more may exist. */
    async loadHistory(scope, { before } = {}) {
        const list = this.messages.get(scope) || [];
        const beforeID = before ?? (list.find(m => m.id)?.id || 0);
        const page = await this.conn.request(Msg.ChatHistory, { channel_id: scope, before_id: beforeID, limit: HISTORY_PAGE },
            Msg.ChatHistoryResponse, { accept: r => r.channel_id === scope });
        for (const key of page.keys || []) this.#installKey(key, false);
        for (const entry of page.messages || []) this.#store(scope, this.#entryToMessage(scope, entry));
        return (page.messages || []).length === HISTORY_PAGE;
    }

    /**
     * Loads history until the message with id is present (for jumping to a
     * reply's original); gives up after maxPages.
     */
    async loadUntil(scope, id, maxPages = 10) {
        for (let i = 0; i < maxPages && !this.messageByID(scope, id); i++) {
            if (!await this.loadHistory(scope)) break;
        }
        return this.messageByID(scope, id);
    }

    /**
     * Searches a scope's history for text (case-insensitive), loading up to
     * maxPages older pages. Returns matching messages, newest first.
     */
    async search(scope, query, { maxPages = 6, from = "" } = {}) {
        const needle = query.trim().toLowerCase();
        for (let i = 0; i < maxPages; i++) {
            if (!await this.loadHistory(scope).catch(() => false)) break;
        }
        return (this.messages.get(scope) || [])
            .filter(m => !m.deleted && typeof m.text === "string" && (!needle || m.text.toLowerCase().includes(needle)) && (!from || m.fromUniqueID === from))
            .reverse();
    }

    /**
     * Sends a channel (scope > 0) or global (scope 0) message with the
     * scope's current key and waits for the server's acceptance.
     */
    async send(scope, text, { replyTo = 0, clientMsgID = crypto.randomUUID(), sentAt = Date.now() / 1000 } = {}) {
        this.#store(scope, { clientMsgID, fromUniqueID: this.uniqueID, from: this.nickname, text, sentAt, own: true, pending: true, queued: false, failed: false, holdUntil: 0, replyTo });
        const keyID = this.current.get(scope);
        const key = keyID && this.keys.get(scope)?.get(keyID);
        if (!key) {
            this.#store(scope, { clientMsgID, pending: false, queued: true });
            throw Object.assign(new Error("This chat's key has not arrived yet."), { retryable: true });
        }
        const request = { ack_requested: true, text: sealScope(text, key), enc: true, key_id: keyID, client_msg_id: clientMsgID, reply_to_id: replyTo || undefined };
        if (scope > 0) request.channel_id = String(scope);
        try {
            const accepted = await this.conn.request(Msg.ChatSend, request, Msg.ChatAccepted, { accept: r => r.client_msg_id === clientMsgID });
            this.#store(scope, { clientMsgID, id: accepted.message_id, pending: false });
            return accepted;
        } catch (error) {
            // A lost connection or a rate limit leaves the message for the outbox to retry.
            const limited = error instanceof ProtocolError && /rate limit/i.test(error.message);
            const retryable = !(error instanceof ProtocolError) || limited;
            this.#store(scope, { clientMsgID, failed: !retryable, queued: retryable, pending: false, error: error.message });
            throw Object.assign(error, { retryable, retryAfterMS: limited ? error.retryAfterMS || 3000 : undefined });
        }
    }

    /** Shows an own message waiting in the outbox (holdUntil: an Undo countdown, ms). */
    showQueued(scope, { clientMsgID, text, sentAt, expired = false, holdUntil = 0, replyTo = 0 }) {
        const held = !expired && holdUntil > Date.now();
        this.#store(scope, { clientMsgID, fromUniqueID: this.uniqueID, from: this.nickname, text, sentAt, own: true, queued: !expired && !held, failed: expired, pending: false, holdUntil: held ? holdUntil : 0, replyTo });
    }

    /** Removes a message that never reached the server from the local list. */
    discardLocal(scope, clientMsgID) {
        const list = (this.messages.get(scope) || []).filter(m => m.clientMsgID !== clientMsgID || m.id);
        this.messages.set(scope, list);
        this.#emit("messages", { scope });
    }

    /** Sends a poll as a message; definition is {question, options, multiple, closesAt}. */
    createPoll(scope, { question, options, multiple = false, closesAt }) {
        const body = POLL_PREFIX + JSON.stringify({ question, options, multiple, closes_at: closesAt });
        return this.send(scope, body);
    }

    sendTyping(scope) {
        if (this.conn.open) this.conn.send(Msg.Typing, scope > 0 ? { channel_id: scope } : {});
    }

    /** Typing indicator for a direct conversation. */
    sendTypingDirect(uid) {
        if (this.conn.open) this.conn.send(Msg.Typing, { to_unique_id: uid });
    }

    // --- Message actions ----------------------------------------------------------

    #mutation(type, request) {
        return this.conn.request(type, { ack_requested: true, ...request }, Msg.ChatMutationSaved,
            { accept: r => r.operation === type && r.message_id === request.message_id });
    }

    /** Edits an own message in place. */
    async edit(scope, messageID, text) {
        const message = this.messageByID(scope, messageID);
        const keyID = this.current.get(scope);
        const key = keyID && this.keys.get(scope)?.get(keyID);
        if (!key) throw new Error("This chat's key has not arrived yet.");
        await this.#mutation(Msg.ChatEdit, { message_id: messageID, new_text: sealScope(text, key), enc: true, key_id: keyID, expected_version: message?.version || undefined });
        if (message) this.#store(scope, { ...message, text, edited: true });
    }

    /** Deletes a message (own, or any with manage_messages). */
    async remove(scope, messageID) {
        await this.#mutation(Msg.ChatDelete, { message_id: messageID });
        const message = this.messageByID(scope, messageID);
        if (message) this.#store(scope, { ...message, text: "", deleted: true });
    }

    /** Toggles a reaction. */
    react(scope, messageID, emoji) {
        return this.#mutation(Msg.ChatReact, { message_id: messageID, emoji });
    }

    /** Pins or unpins a channel message. */
    pin(scope, messageID, pinned = true) {
        return this.#mutation(Msg.ChatPin, { channel_id: scope, message_id: messageID, pinned });
    }

    /** Loads a channel's pins, newest first, with their decrypted messages. */
    async loadPins(scope) {
        const response = await this.conn.request(Msg.ChatPins, { channel_id: scope }, Msg.ChatPinsResponse, { accept: r => r.channel_id === scope });
        for (const key of response.keys || []) this.#installKey(key, false);
        const pins = (response.pins || []).map(pin => ({
            messageID: pin.message_id, pinnedBy: pin.pinned_by, pinnedAt: pin.pinned_at,
            message: pin.message ? this.#entryToMessage(scope, pin.message) : this.messageByID(scope, pin.message_id),
        }));
        this.pins.set(scope, pins.map(p => p.messageID));
        // Only messages already in the log are marked: inserting an old pinned
        // message would leave a gap and move the history cursor past it.
        for (const pin of pins) {
            const known = this.messageByID(scope, pin.messageID);
            if (known && !known.pinned) this.#store(scope, { ...known, pinned: true });
        }
        this.#emit("pins", { scope });
        return pins;
    }

    /** Poll state; action is "get", "vote" (with choices) or "close". */
    poll(messageID, action = "get", choices = []) {
        return this.conn.request(Msg.PollRequest, { message_id: messageID, action, choices }, Msg.PollState,
            { accept: r => r.message_id === messageID && r.action === action });
    }

    /** Parses a message body as a poll, or returns null. */
    static parsePoll(text) {
        if (typeof text !== "string" || !text.startsWith(POLL_PREFIX)) return null;
        try {
            const p = JSON.parse(text.slice(POLL_PREFIX.length));
            if (!p || typeof p.question !== "string" || !Array.isArray(p.options)) return null;
            return { question: p.question, options: p.options, multiple: !!p.multiple, closesAt: Number(p.closes_at) || 0 };
        } catch { return null; }
    }

    // --- Server lookups ----------------------------------------------------------

    #previews = new Map();

    /** A link preview from the server (provider "web", "youtube", "x", "twitch", ...). */
    linkPreview(provider, resource) {
        if (!this.capabilities.has("server_link_previews_v1")) return Promise.reject(new Error("link previews are not available on this server"));
        const key = `${provider}\u0000${resource}`;
        if (!this.#previews.has(key)) {
            const pending = this.conn.request(Msg.LinkPreviewRequest, { provider, resource }, Msg.LinkPreviewResult, { timeout: 15000 })
                .catch(error => { this.#previews.delete(key); throw error; });
            this.#previews.set(key, pending);
        }
        return this.#previews.get(key);
    }

    #emoji = null;
    #emojiImages = new Map();

    /** The server's custom emoji: [{name, file_name}]. Cached until the server reports a change. */
    async emojiList() {
        if (!this.#emoji) this.#emoji = this.conn.request(Msg.EmojiList, {}, Msg.EmojiListResponse).then(r => r.emojis || []).catch(error => { this.#emoji = null; throw error; });
        return this.#emoji;
    }

    /** A data: URL for one custom emoji. */
    async emojiImage(name) {
        if (!this.#emojiImages.has(name)) {
            const pending = this.conn.request(Msg.EmojiGet, { name }, Msg.EmojiData, { accept: r => r.name === name })
                .then(data => `data:${data.content_type || "image/png"};base64,${data.data_base64}`)
                .catch(error => { this.#emojiImages.delete(name); throw error; });
            this.#emojiImages.set(name, pending);
        }
        return this.#emojiImages.get(name);
    }

    serverInfo() {
        return this.conn.request(Msg.ServerInfoQuery, {}, Msg.ServerInfoResponse);
    }

    /** Round-trip time of one Ping in milliseconds. */
    async ping() {
        const started = performance.now();
        await this.conn.request(Msg.Ping, {}, Msg.Pong, { timeout: 5000 });
        return performance.now() - started;
    }

    // --- Server rules ------------------------------------------------------------

    // The server keeps a session out of channels and chat until it accepts
    // the rules (internal/server/handlers_rules.go). A ServerRules frame with
    // a hash asks; an empty one confirms the acceptance.
    #setRules(rules) {
        this.pendingRules = rules?.hash ? { text: rules.text || "", hash: rules.hash } : null;
        this.#emit("rules", this.pendingRules);
    }

    /**
     * Accepts the rules shown, by hash. A stale hash is refused and the
     * server sends the current text, which re-emits "rules".
     */
    async acceptRules(hash) {
        const reply = await this.conn.request(Msg.ServerRulesAccept, { hash }, Msg.ServerRules);
        this.#setRules(reply);
        return !this.pendingRules;
    }

    #avatars = new Map();

    /** A data: URL of a user's avatar, or "" when none. */
    async avatar(uid) {
        if (!this.#avatars.has(uid)) {
            const pending = this.conn.request(Msg.AvatarGet, { unique_id: uid }, Msg.AvatarData, { accept: r => r.unique_id === uid })
                .then(data => data.data_base64 ? `data:${data.content_type || "image/png"};base64,${data.data_base64}` : "")
                .catch(() => "");
            this.#avatars.set(uid, pending);
        }
        return this.#avatars.get(uid);
    }

    forgetAvatar(uid) {
        this.#avatars.delete(uid);
    }

    // --- Presence and social -----------------------------------------------------

    /** Sets this session's status ("", online, away, busy, invisible) and message. */
    async setStatus(status, message = "") {
        const saved = await this.conn.request(Msg.SetStatus, { ack_requested: true, status, message }, Msg.StatusSaved);
        const self = this.self();
        if (self) { self.status = saved.status; self.status_message = saved.message; this.#emit("snapshot", this.snapshot); }
        return saved;
    }

    /** Pokes a client with an optional message. */
    poke(clientID, message = "") {
        return this.conn.request(Msg.Poke, { ack_requested: true, client_id: clientID, message }, Msg.PokeAccepted, { accept: r => r.client_id === clientID });
    }

    /** Changes this session's public display name. */
    async setDisplayName(nickname) {
        const saved = await this.conn.request(Msg.DisplayNameSet, { nickname }, Msg.DisplayNameSaved);
        this.nickname = saved.nickname;
        return saved;
    }

    /** Joins a voice channel (0 leaves). Text-only channels are subscribed instead. */
    join(channelID, password = "") {
        return this.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: channelID, password: password || undefined }, Msg.ChannelJoined);
    }
}
