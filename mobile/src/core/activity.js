// activity.js — the activity center's log: mentions, direct messages,
// calls, invitations and pokes, newest first, kept encrypted on this phone
// (sealed IndexedDB store). Entries carry names and short previews only so
// the list is useful offline; at most CAP are kept.
import { Stores, getSealed, putSealed } from "./db.js";

export const CAP = 500;
export const KINDS = Object.freeze(["mention", "dm", "call", "invite", "poke"]);
const KEY = "log";

/** Storage over the sealed store; tests pass an in-memory one. */
export const sealedStorage = {
    load: () => getSealed(Stores.activity, KEY),
    save: entries => putSealed(Stores.activity, KEY, entries),
};

export class ActivityLog extends EventTarget {
    constructor(storage = sealedStorage) {
        super();
        this.storage = storage;
        this.entries = [];
        this.ready = this.#load();
        this.#saving = Promise.resolve();
    }

    #saving;

    async #load() {
        try {
            const stored = await this.storage.load();
            // Entries added before the load finished stay in front.
            if (Array.isArray(stored)) this.entries = [...this.entries, ...stored.filter(e => !this.entries.some(x => x.id === e.id))].slice(0, CAP);
        } catch {
            // A missing or unreadable log starts empty.
        }
        this.#changed(false);
    }

    #changed(persist = true) {
        if (persist) {
            const snapshot = this.entries.slice();
            this.#saving = this.#saving.then(() => this.storage.save(snapshot)).catch(() => {});
        }
        this.dispatchEvent(new CustomEvent("change"));
    }

    /** Waits until every change so far is stored. */
    flush() {
        return this.#saving;
    }

    /**
     * Adds an entry {kind, title, body, route, tag}. An unread entry of the
     * same tag and kind is replaced, so a busy chat is one line.
     */
    add({ kind, title, body = "", route = "", tag = "", at = Date.now() }) {
        if (!KINDS.includes(kind)) return null;
        const entry = { id: `${at.toString(36)}-${Math.random().toString(36).slice(2, 8)}`, kind, title: String(title).slice(0, 120), body: String(body).slice(0, 200), route, tag, at, read: false };
        const rest = this.entries.filter(e => !(tag && e.tag === tag && e.kind === kind && !e.read));
        this.entries = [entry, ...rest].slice(0, CAP);
        this.#changed();
        return entry;
    }

    /** Entries of a tab: "all", "mention", "dm", "call", "invite" or "poke". */
    list(tab = "all") {
        return tab === "all" ? this.entries : this.entries.filter(e => e.kind === tab);
    }

    get unread() {
        return this.entries.filter(e => !e.read).length;
    }

    markRead(id) {
        const entry = this.entries.find(e => e.id === id);
        if (!entry || entry.read) return;
        entry.read = true;
        this.#changed();
    }

    /** Marks entries of a conversation tag read (it was opened). */
    markTagRead(tag) {
        let changed = false;
        for (const entry of this.entries) if (entry.tag === tag && !entry.read) { entry.read = true; changed = true; }
        if (changed) this.#changed();
    }

    markAllRead() {
        if (!this.unread) return;
        for (const entry of this.entries) entry.read = true;
        this.#changed();
    }

    clear() {
        this.entries = [];
        this.#changed();
    }
}
