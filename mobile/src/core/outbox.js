// outbox.js — messages written while offline. Entries are stored encrypted
// and sealed for their recipients only when sent, with the client message
// ID chosen when they were written, so a resend is never a duplicate.
//
// Each entry has two parts:
//   draft     entry.text — editable: enqueue with the same ID replaces it,
//             and after 14 days unsent it is dropped (shown as not sent).
//   attempt   entry.committed = {text} — written durably BEFORE a
//             noxa-olm-v1 send starts, because the consumer commits an
//             immutable operation for that ID. From then on the entry only
//             resolves that operation (always with its committed text): a
//             replaced draft never overwrites it, and expiry never drops it
//             while it may still be delivered.
// An entry leaves the outbox once the server accepts it. A flush is fenced to
// the identity generation it started in: after logout nothing is sent or
// written back.
import { Stores, currentFence, deleteKey, deleteKeyIn, getSealed, keys, putSealed, putSealedIn, updateSealed } from "./db.js";

export const OUTBOX_TTL = 14 * 24 * 3600 * 1000;
export const SYNC_TAG = "noxa-outbox";

/**
 * Adds a message, or replaces the draft of the entry with the same ID (a
 * retry). target is {kind: "channel", scope}, {kind: "dm", peer, nickname} or
 * {kind: "conversation", conversation} for a private group, where the ID
 * doubles as the message reference. An entry whose attempt is committed keeps
 * it: the new text is not stored and the existing entry is returned with
 * replaced: false; an explicit retry of such an entry that failed terminally
 * clears the failure, so the next flush resolves the SAME operation again.
 * The read-check-write is one compare-and-swap (a concurrent flush commit or
 * tab is never overwritten), and an entry that cannot be read is refused
 * (code "unreadable"), never replaced. Returns the entry.
 */
export async function enqueue(target, text, { id = crypto.randomUUID(), sentAt = Date.now() / 1000, holdUntil = 0, replyTo = 0 } = {}) {
    // holdUntil (ms) keeps an "Undo send" message back until its countdown ends.
    const entry = { id, ...target, text, sentAt, createdAt: Date.now(), holdUntil, replyTo };
    let result = entry;
    await updateSealed(Stores.outbox, id, existing => {
        if (!existing?.committed) { result = entry; return entry; }
        const { failed, error, ...kept } = existing;
        result = { ...kept, replaced: false };
        return failed || error ? kept : undefined;
    });
    requestBackgroundSync();
    return result;
}

/**
 * Lists entries oldest first. Drafts past the TTL lose their text and are
 * returned with expired set; committed attempts never expire. An entry that
 * cannot be read is returned as {id, unreadable: true} (never dropped, never
 * sent), so a storage failure cannot silently lose or replay a message.
 */
export async function pending(now = Date.now(), { fence = null } = {}) {
    const out = [];
    for (const id of await keys(Stores.outbox)) {
        let entry;
        try { entry = await getSealed(Stores.outbox, id); }
        catch (error) { out.push({ id, unreadable: true, error: error?.message || String(error), createdAt: 0 }); continue; }
        if (!entry) continue;
        if (!entry.committed && !entry.expired && now - entry.createdAt > OUTBOX_TTL) {
            entry.expired = true;
            entry.text = "";
            // Fenced when called by a flush: never written back after logout.
            await (fence ? putSealedIn(fence, Stores.outbox, id, entry) : putSealed(Stores.outbox, id, entry)).catch(() => {});
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

/**
 * The running flush per session: a flush stuck on an old session's dead
 * connection never swallows a new session's flush.
 */
const flushing = new WeakMap();
/** The Undo-hold re-flush timer per session (an old flush never clears a new session's). */
const heldTimers = new WeakMap();

/**
 * Writes an entry's new state back only while it is still queued: a flush of
 * an older session that wakes up late never resurrects an entry that another
 * flush already sent and removed.
 */
function keepQueued(fence, entry) {
    return updateSealed(Stores.outbox, entry.id, cur => (cur ? entry : undefined), fence);
}

const sameFence = (a, b) => a.generation === b.generation && a.active && b.active;

/**
 * Sends waiting entries in order through a signed-in session, its direct
 * messages and its private groups. Stops at the first lost connection;
 * refused entries are kept as failed for the user to see. Returns the
 * number sent.
 */
export function flush(session, dm, groups = null) {
    const key = session && typeof session === "object" ? session : flushing;
    const running = flushing.get(key);
    if (running) return running;
    const run = (async () => {
        let sent = 0;
        let nextHeld = Infinity;
        // Destinations whose earlier entry stayed queued: later entries to
        // the same peer/conversation/channel wait, so send order is kept.
        const waiting = new Set();
        try {
            const fence = await currentFence();
            const stillCurrent = async () => sameFence(fence, await currentFence());
            for (const entry of await pending(Date.now(), { fence })) {
                if (entry.unreadable) continue;
                if (!(await stillCurrent())) break;
                const ratchet = (entry.kind === "dm" && !!dm?.ratchet) || (entry.kind === "conversation" && !!groups?.ratchet);
                // A committed attempt is resolved even when its draft expired;
                // only a terminal refusal (failed) stops it.
                if (entry.failed || (entry.expired && !entry.committed)) {
                    show(session, dm, entry, groups);
                    continue;
                }
                // Without groups (an older caller) the entry waits for one.
                if (entry.kind === "conversation" && !groups) continue;
                if (waiting.has(destination(entry))) continue;
                // Held for Undo: sent by a later flush once the countdown ends.
                if (entry.holdUntil > Date.now() && !entry.committed) {
                    nextHeld = Math.min(nextHeld, entry.holdUntil);
                    continue;
                }
                let current = entry;
                if (ratchet && !current.committed) {
                    // Durable BEFORE the consumer commits an operation for this
                    // ID, as a CAS over the CURRENT draft (a concurrent replace
                    // is committed as it stands, never lost or overwritten).
                    // Another tab's failed (never attempted) draft is not committed.
                    let latest;
                    try { latest = await updateSealed(Stores.outbox, current.id, cur => (cur && !cur.committed && !cur.failed ? { ...cur, committed: { text: cur.text } } : undefined), fence); }
                    catch (error) {
                        if (error.code === "fenced") break;
                        continue; // one unreadable/contended entry never stops the others
                    }
                    if (!latest) continue; // removed meanwhile
                    current = latest;
                    if (current.failed || !current.committed) continue;
                }
                const text = current.committed ? current.committed.text : current.text;
                try {
                    let outcome = null;
                    for (let attempt = 0; ; attempt++) {
                        try {
                            if (current.kind === "channel") {
                                await session.send(current.scope, text, { clientMsgID: current.id, sentAt: current.sentAt, replyTo: current.replyTo || 0 });
                            } else if (current.kind === "conversation") {
                                outcome = await groups.send(current.conversation, text, { reference: current.id, sentAt: current.sentAt });
                            } else {
                                outcome = await dm.send(current.peer, text, current.nickname || "", { id: current.id, sentAt: current.sentAt });
                            }
                            break;
                        } catch (error) {
                            // The server meters sends per user; a short pause clears the limit.
                            if (!error.retryAfterMS || attempt >= 2 || !session.conn.open) throw error;
                            await new Promise(resolve => setTimeout(resolve, Math.min(error.retryAfterMS, 10000)));
                        }
                    }
                    if (outcome?.mismatch) {
                        // The queued text was NOT sent: an earlier operation
                        // with this ID was. Keep it, truthfully failed.
                        throw Object.assign(new Error("logical_mismatch: a different message was already sent with this ID"), { code: "logical_mismatch" });
                    }
                    await deleteKeyIn(fence, Stores.outbox, current.id);
                    sent++;
                } catch (error) {
                    if (error.code === "fenced" || !(await stillCurrent())) break;
                    // The consumer's committed original is authoritative.
                    const original = error.committed?.record?.body?.text;
                    if (typeof original === "string" && current.committed && original !== current.committed.text) {
                        current = { ...current, committed: { text: original } };
                    }
                    // Secure messaging blocked (no consumer yet, old server,
                    // re-link needed): this entry stays queued with the reason
                    // (a committed attempt is never marked failed) and the
                    // flush continues with the others (channel messages need
                    // no secure messaging). Only a lost connection or a
                    // rate limit (it covers the whole connection) stops it.
                    if (error.blocked) {
                        await keepQueued(fence, { ...current, error: error.message }).catch(() => {});
                        show(session, dm, { ...current, error: error.message }, groups);
                        waiting.add(destination(current));
                        continue;
                    }
                    if (error.retryable) {
                        // Kept with its committed state; any other retryable
                        // failure (e.g. a server backend "unavailable") skips
                        // only this entry, so later ones are not starved.
                        if (current !== entry) await keepQueued(fence, current).catch(() => {});
                        if (stopsFlush(session, error)) break;
                        waiting.add(destination(current));
                        continue;
                    }
                    console.warn(`outbox: ${current.kind} message ${current.id} refused: ${error.message}`);
                    await keepQueued(fence, { ...current, failed: true, error: error.message }).catch(() => {});
                }
            }
        } finally {
            flushing.delete(key);
        }
        clearTimeout(heldTimers.get(key));
        heldTimers.delete(key);
        if (nextHeld < Infinity && session.conn?.open) heldTimers.set(key, setTimeout(() => flush(session, dm, groups).catch(() => {}), Math.max(50, nextHeld - Date.now() + 50)));
        return sent;
    })();
    flushing.set(key, run);
    return run;
}

/** The ordering key of an entry: one peer, conversation or channel. */
function destination(entry) {
    if (entry.kind === "channel") return `channel:${entry.scope}`;
    if (entry.kind === "conversation") return `conversation:${entry.conversation}`;
    return `dm:${entry.peer}`;
}

/** A retryable failure stops the whole flush only when the connection is gone or rate limited. */
function stopsFlush(session, error) {
    return !session.conn?.open || error.code === "rate-limit" || !!error.retryAfterMS;
}

/** Shows waiting and expired entries in their conversations. */
export async function showPending(session, dm, groups = null) {
    for (const entry of await pending()) if (!entry.unreadable) show(session, dm, entry, groups);
}

export function show(session, dm, entry, groups = null) {
    const text = entry.committed ? entry.committed.text : entry.text;
    const state = { text, sentAt: entry.sentAt, expired: (!!entry.expired && !entry.committed) || !!entry.failed, holdUntil: entry.committed ? 0 : entry.holdUntil || 0, replyTo: entry.replyTo || 0 };
    if (entry.kind === "channel") session.showQueued(entry.scope, { clientMsgID: entry.id, ...state });
    else if (entry.kind === "conversation") groups?.showQueued(entry.conversation, { reference: entry.id, ...state });
    else dm?.showQueued(entry.peer, { id: entry.id, ...state });
}

/** Number of entries still waiting to send (unreadable ones included: they are not lost). */
export async function waitingCount() {
    return (await pending()).filter(entry => entry.unreadable || ((!entry.expired || entry.committed) && !entry.failed)).length;
}
