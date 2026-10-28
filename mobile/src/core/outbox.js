// outbox.js — messages written while offline. Entries are stored encrypted
// and sealed for their recipients only when sent, with the client message
// ID chosen when they were written, so a resend is never a duplicate.
// An entry leaves the outbox once the server accepts it, or after 14 days,
// when its text is dropped and it is shown as not sent.
import { Stores, deleteKey, getSealed, keys, putSealed } from "./db.js";

export const OUTBOX_TTL = 14 * 24 * 3600 * 1000;
export const SYNC_TAG = "noxa-outbox";

/**
 * Adds a message, or replaces the entry with the same ID (a retry). target is
 * {kind: "channel", scope}, {kind: "dm", peer, nickname} or {kind:
 * "conversation", conversation} for a private group, where the ID doubles as
 * the message reference. Returns the entry.
 */
export async function enqueue(target, text, { id = crypto.randomUUID(), sentAt = Date.now() / 1000, holdUntil = 0, replyTo = 0 } = {}) {
    // holdUntil (ms) keeps an "Undo send" message back until its countdown ends.
    const entry = { id, ...target, text, sentAt, createdAt: Date.now(), holdUntil, replyTo };
    await putSealed(Stores.outbox, id, entry);
    requestBackgroundSync();
    return entry;
}

/**
 * Lists entries oldest first. Entries past the TTL lose their text and are
 * returned with expired set.
 */
export async function pending(now = Date.now()) {
    const out = [];
    for (const id of await keys(Stores.outbox)) {
        const entry = await getSealed(Stores.outbox, id).catch(() => undefined);
        if (!entry) continue;
        if (!entry.expired && now - entry.createdAt > OUTBOX_TTL) {
            entry.expired = true;
            entry.text = "";
            await putSealed(Stores.outbox, id, entry).catch(() => {});
        }
        out.push(entry);
    }
    return out.sort((a, b) => a.createdAt - b.createdAt);
}

export function remove(id) {
    return deleteKey(Stores.outbox, id);
}

/** Asks the browser to flush the outbox when it is online again (Android). */
export async function requestBackgroundSync() {
    try {
        const registration = await globalThis.navigator?.serviceWorker?.getRegistration("/app/");
        await registration?.sync?.register(SYNC_TAG);
    } catch {
        // Background Sync is optional; the app flushes on its next start.
    }
}

let flushing = null;
let heldTimer = 0;

/**
 * Sends waiting entries in order through a signed-in session, its direct
 * messages and its private groups. Stops at the first lost connection;
 * refused entries are kept as failed for the user to see. Returns the
 * number sent.
 */
export function flush(session, dm, groups = null) {
    if (flushing) return flushing;
    flushing = (async () => {
        let sent = 0;
        let nextHeld = Infinity;
        try {
            for (const entry of await pending()) {
                if (entry.expired || entry.failed) {
                    show(session, dm, entry, groups);
                    continue;
                }
                // Without groups (an older caller) the entry waits for one.
                if (entry.kind === "conversation" && !groups) continue;
                // Held for Undo: sent by a later flush once the countdown ends.
                if (entry.holdUntil > Date.now()) {
                    nextHeld = Math.min(nextHeld, entry.holdUntil);
                    continue;
                }
                try {
                    let outcome = null;
                    for (let attempt = 0; ; attempt++) {
                        try {
                            if (entry.kind === "channel") {
                                await session.send(entry.scope, entry.text, { clientMsgID: entry.id, sentAt: entry.sentAt, replyTo: entry.replyTo || 0 });
                            } else if (entry.kind === "conversation") {
                                outcome = await groups.send(entry.conversation, entry.text, { reference: entry.id, sentAt: entry.sentAt });
                            } else {
                                outcome = await dm.send(entry.peer, entry.text, entry.nickname || "", { id: entry.id, sentAt: entry.sentAt });
                            }
                            break;
                        } catch (error) {
                            // The server meters sends per user; a short pause clears the limit.
                            if (!error.retryAfterMS || attempt >= 2 || !session.conn.open) throw error;
                            await new Promise(resolve => setTimeout(resolve, Math.min(error.retryAfterMS, 10000)));
                        }
                    }
                    if (outcome?.mismatch) {
                        // The queued (changed) text was NOT sent: an earlier
                        // operation with this ID was. Keep it, truthfully failed.
                        throw Object.assign(new Error("logical_mismatch: a different message was already sent with this ID"), { code: "logical_mismatch" });
                    }
                    await remove(entry.id);
                    sent++;
                } catch (error) {
                    if (error.retryable) break;
                    console.warn(`outbox: ${entry.kind} message ${entry.id} refused: ${error.message}`);
                    await putSealed(Stores.outbox, entry.id, { ...entry, failed: true, error: error.message }).catch(() => {});
                }
            }
        } finally {
            flushing = null;
        }
        clearTimeout(heldTimer);
        if (nextHeld < Infinity && session.conn?.open) heldTimer = setTimeout(() => flush(session, dm, groups).catch(() => {}), Math.max(50, nextHeld - Date.now() + 50));
        return sent;
    })();
    return flushing;
}

/** Shows waiting and expired entries in their conversations. */
export async function showPending(session, dm, groups = null) {
    for (const entry of await pending()) show(session, dm, entry, groups);
}

export function show(session, dm, entry, groups = null) {
    const state = { text: entry.text, sentAt: entry.sentAt, expired: !!entry.expired || !!entry.failed, holdUntil: entry.holdUntil || 0, replyTo: entry.replyTo || 0 };
    if (entry.kind === "channel") session.showQueued(entry.scope, { clientMsgID: entry.id, ...state });
    else if (entry.kind === "conversation") groups?.showQueued(entry.conversation, { reference: entry.id, ...state });
    else dm?.showQueued(entry.peer, { id: entry.id, ...state });
}

/** Number of entries still waiting to send. */
export async function waitingCount() {
    return (await pending()).filter(entry => !entry.expired && !entry.failed).length;
}
