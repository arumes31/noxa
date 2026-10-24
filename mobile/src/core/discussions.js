// discussions.js — threads and forum posts of a channel (180/181
// DiscussionRequest/Result, internal/netproto/discussion.go). Thread
// bodies are sealed with the channel's scope key like ordinary messages;
// pages carry the archival key generations they reference.
import { sealScope } from "./crypto.js";
import { Msg } from "./frame.js";

export class Discussions extends EventTarget {
    constructor(session) {
        super();
        this.session = session;
        this.conn = session.conn;
        /** channel -> {forum, autoArchiveHours, tags, threads: [], canManage, canModerate} */
        this.channels = new Map();
        /** thread -> messages, oldest first */
        this.messages = new Map();
        this.#onEvent = event => {
            const { type, data } = event.detail;
            if (type === "discussion_changed" && data?.channel_id) this.#changed(data);
        };
        session.addEventListener("server-event", this.#onEvent);
    }

    #onEvent;

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    #changed(data) {
        const channelID = Number(data.channel_id);
        const threadID = Number(data.thread_id || 0);
        if (data.deleted) {
            const state = this.channels.get(channelID);
            if (state) state.threads = state.threads.filter(thread => thread.id !== threadID);
            this.messages.delete(threadID);
        } else if (data.deleted_message_id && this.messages.has(threadID)) {
            const list = this.messages.get(threadID);
            const message = list.find(m => m.id === Number(data.deleted_message_id));
            if (message) Object.assign(message, { text: "", deleted: true });
        }
        this.#emit("changed", { channelID, threadID, newMessage: !!data.new_message, messageID: data.message_id, author: data.author, deleted: !!data.deleted });
    }

    async #request(request) {
        const result = await this.conn.request(Msg.DiscussionRequest, request, Msg.DiscussionResult,
            { accept: r => r.action === request.action && r.channel_id === request.channel_id });
        for (const key of result.keys || []) this.session.installKey(key);
        return result;
    }

    #entry(channelID, entry) {
        return {
            id: entry.id, fromUniqueID: entry.from_unique_id, from: entry.from_nickname, keyID: entry.key_id, body: entry.body_enc,
            text: entry.deleted ? "" : this.session.decrypt(channelID, entry.key_id, entry.body_enc),
            sentAt: entry.sent_at, edited: !!entry.edited_at, deleted: !!entry.deleted, reactions: entry.reactions || {},
            replyTo: entry.reply_to_id || 0, own: entry.from_unique_id === this.session.uniqueID,
        };
    }

    #storeState(channelID, result) {
        const state = this.channels.get(channelID) || { threads: [] };
        if (Array.isArray(result.tags)) state.tags = result.tags;
        state.forum = !!result.forum;
        state.autoArchiveHours = result.auto_archive_hours || 0;
        state.canManage = !!result.can_manage;
        state.canModerate = !!result.can_moderate;
        this.channels.set(channelID, state);
        return state;
    }

    /** Lists a channel's threads (newest activity first) and its forum settings. */
    async list(channelID, { beforeID = 0, beforePinned = false } = {}) {
        const result = await this.#request({ action: "list", channel_id: channelID, before_id: beforeID || undefined, before_pinned: beforePinned || undefined });
        const state = this.#storeState(channelID, result);
        const threads = result.threads || [];
        state.threads = beforeID ? [...state.threads, ...threads.filter(t => !state.threads.some(s => s.id === t.id))] : threads;
        state.hasMore = !!result.has_more;
        this.#emit("list", { channelID });
        return state;
    }

    /** One thread's current metadata. */
    async get(channelID, threadID) {
        const result = await this.#request({ action: "get", channel_id: channelID, thread_id: threadID });
        this.#storeState(channelID, result);
        const thread = result.threads?.[0];
        if (thread) this.#upsertThread(channelID, thread);
        return thread || null;
    }

    #upsertThread(channelID, thread) {
        const state = this.channels.get(channelID) || this.#storeState(channelID, {});
        const index = state.threads.findIndex(t => t.id === thread.id);
        if (index >= 0) state.threads[index] = thread; else state.threads.unshift(thread);
        this.#emit("list", { channelID });
    }

    /** Loads a page of a thread's messages (older first); returns whether more exist. */
    async history(channelID, threadID) {
        const list = this.messages.get(threadID) || [];
        const beforeID = list.find(m => m.id)?.id || 0;
        const result = await this.#request({ action: "history", channel_id: channelID, thread_id: threadID, before_id: beforeID || undefined });
        this.#storeState(channelID, result);
        const page = (result.messages || []).map(entry => this.#entry(channelID, entry));
        const merged = [...page.filter(m => !list.some(l => l.id === m.id)), ...list].sort((a, b) => a.sentAt - b.sentAt || a.id - b.id);
        this.messages.set(threadID, merged);
        this.#emit("messages", { channelID, threadID });
        return !!result.has_more;
    }

    #seal(channelID, text) {
        const keyID = this.session.current.get(channelID);
        const key = keyID && this.session.keys.get(channelID)?.get(keyID);
        if (!key) throw new Error("This chat's key has not arrived yet.");
        return { body_enc: sealScope(text, key), key_id: keyID };
    }

    /** Creates a thread (forum post) with a title, tags and first message. */
    async create(channelID, { title, tags = [], text, rootMessageID = 0 }) {
        const result = await this.#request({ action: "create", channel_id: channelID, title, tags, root_message_id: rootMessageID || undefined,
            request_id: crypto.randomUUID(), ...this.#seal(channelID, text) });
        if (result.threads?.[0]) this.#upsertThread(channelID, result.threads[0]);
        return result;
    }

    /** Replies in a thread. */
    async send(channelID, threadID, text) {
        const result = await this.#request({ action: "send", channel_id: channelID, thread_id: threadID, request_id: crypto.randomUUID(), ...this.#seal(channelID, text) });
        const list = this.messages.get(threadID) || [];
        if (result.message_id && !list.some(m => m.id === result.message_id)) {
            list.push({ id: result.message_id, fromUniqueID: this.session.uniqueID, from: this.session.nickname, text, sentAt: Date.now() / 1000, own: true, reactions: {} });
            this.messages.set(threadID, list);
            this.#emit("messages", { channelID, threadID });
        }
        return result;
    }

    /** Edits a thread's title and tags. */
    edit(channelID, threadID, { title, tags = [] }) {
        return this.#request({ action: "edit", channel_id: channelID, thread_id: threadID, title, tags });
    }

    /** Thread flags: archive/reopen, pin, resolve, join/leave, subscribe (follow). */
    async setFlag(channelID, threadID, action, value = true) {
        const request = { action, channel_id: channelID, thread_id: threadID };
        if (action === "pin") request.pinned = value;
        if (action === "resolve") request.resolved = value;
        if (action === "subscribe") request.subscribed = value;
        if (action === "archive" && !value) request.action = "reopen";
        const result = await this.#request(request);
        if (result.threads?.[0]) this.#upsertThread(channelID, result.threads[0]);
        return result;
    }

    /** Deletes a thread or one of its messages. */
    async remove(channelID, threadID, messageID = 0) {
        const result = await this.#request(messageID
            ? { action: "delete_message", channel_id: channelID, thread_id: threadID, message_id: messageID }
            : { action: "delete", channel_id: channelID, thread_id: threadID });
        if (messageID) {
            const message = this.messages.get(threadID)?.find(m => m.id === messageID);
            if (message) Object.assign(message, { text: "", deleted: true });
            this.#emit("messages", { channelID, threadID });
        } else {
            const state = this.channels.get(channelID);
            if (state) state.threads = state.threads.filter(t => t.id !== threadID);
            this.messages.delete(threadID);
            this.#emit("list", { channelID });
        }
        return result;
    }

    /** Channel forum settings (managers): forum mode, tags, auto-archive hours. */
    async configure(channelID, { forum, tags = [], autoArchiveHours = 0 }) {
        const result = await this.#request({ action: "configure", channel_id: channelID, forum: !!forum, tags, auto_archive_hours: autoArchiveHours || undefined });
        this.#storeState(channelID, result);
        this.#emit("list", { channelID });
        return result;
    }

    close() {
        this.session.removeEventListener("server-event", this.#onEvent);
    }
}
