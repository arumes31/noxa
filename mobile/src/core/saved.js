// saved.js — messages the user keeps for themselves, as references only
// (the desktop's SavedMessageReference): kind, scope ids and the message
// id, never text or keys. Resolving a reference re-reads the conversation,
// so a message the user may no longer see is not shown. Collections are
// free-form labels; "" is the default collection.
import { Stores, getSealed, putSealed } from "./db.js";

const KEY = "saved-messages";

export function sameReference(a, b) {
    if (a.kind !== b.kind) return false;
    if (a.kind === "dm") return a.peer === b.peer && a.clientMsgID === b.clientMsgID;
    if (a.kind === "group") return a.groupID === b.groupID && a.messageID === b.messageID;
    return a.channelID === b.channelID && (a.threadID || 0) === (b.threadID || 0) && a.messageID === b.messageID;
}

export class SavedMessages extends EventTarget {
    constructor() {
        super();
        this.references = [];
        this.ready = this.#load();
    }

    async #load() {
        try {
            const stored = await getSealed(Stores.saved, KEY);
            if (Array.isArray(stored)) this.references = stored;
        } catch {
            // Storage unavailable: the list lives in memory.
        }
    }

    #write() {
        putSealed(Stores.saved, KEY, this.references).catch(() => {});
        this.dispatchEvent(new CustomEvent("change"));
    }

    /** Saves a reference ({kind:"channel"|"thread"|"group"|"dm", ...}) into a collection. */
    async save(reference, collection = "") {
        await this.ready;
        const entry = { ...reference, collection, savedAt: Date.now() };
        const index = this.references.findIndex(r => sameReference(r, reference));
        if (index >= 0) this.references[index] = { ...this.references[index], collection };
        else this.references.unshift(entry);
        this.#write();
        return entry;
    }

    async remove(reference) {
        await this.ready;
        this.references = this.references.filter(r => !sameReference(r, reference));
        this.#write();
    }

    async move(reference, collection) {
        return this.save(reference, collection);
    }

    async isSaved(reference) {
        await this.ready;
        return this.references.some(r => sameReference(r, reference));
    }

    /** Collection names in use, the default first. */
    collections() {
        return [...new Set(["", ...this.references.map(r => r.collection || "")])];
    }

    list(collection = null) {
        return this.references.filter(r => collection === null || (r.collection || "") === collection);
    }
}
