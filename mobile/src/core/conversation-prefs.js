// conversation-prefs.js — per-conversation preferences of this phone.
//
// Activity: settings.hideChatActivity keeps typing and read receipts private
// (direct messages and private groups alike).
//
// Muted conversations on this phone ("dm:<uid>", "conversation:<id>",
// settings.mutedConversations). A muted conversation is not counted as
// unread, badges nothing, and its pushes are not shown while the app is open:
// the service worker gets the list by postMessage and keeps it in its cache.
import { get, onChange, set } from "./settings.js";

export { watchCallLog, resetCallLog } from "./call-log.js";

/** Whether typing indicators and read receipts may be sent. */
export function activityShared() {
    return !get("hideChatActivity");
}

export const MUTED_CACHE = "noxa-prefs";
export const MUTED_URL = "/app/__prefs/muted.json";

export function mutedTags() {
    const list = get("mutedConversations");
    return Array.isArray(list) ? list : [];
}

export function isMuted(tag) {
    return mutedTags().includes(tag);
}

export function setMuted(tag, muted) {
    const others = mutedTags().filter(entry => entry !== tag);
    set({ mutedConversations: muted ? [...others, tag] : others });
}

async function tellWorker(tags) {
    try {
        const registration = await globalThis.navigator?.serviceWorker?.getRegistration("/app/");
        (registration?.active || registration?.waiting)?.postMessage({ type: "muted", tags });
    } catch {
        // Without a worker there are no pushes to hide.
    }
}

let watching = false;

/** Keeps the service worker's copy of the list current. */
export function syncMuted() {
    tellWorker(mutedTags());
    if (watching) return;
    watching = true;
    onChange(({ changed }) => { if (changed.includes("mutedConversations")) tellWorker(mutedTags()); });
}
