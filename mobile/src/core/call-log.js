// call-log.js — this device's history of private 1:1 calls, per contact,
// encrypted in IndexedDB (Stores.calls, key: the contact's unique ID).
// Records come from Calls' "record" events: {id, peer, direction "in" |
// "out", outcome "completed" | "missed" | "declined", startedAt, endedAt,
// duration} with times in milliseconds.
import { Stores, deleteKey, getSealed, putSealed } from "./db.js";

const MAX_PER_PEER = 200;

/** Fires "change" {peer} after a record was added or a history cleared. */
export const callLog = new EventTarget();

const cache = new Map();
let writes = Promise.resolve();
let generation = 0;

/** The calls with peer, oldest first. */
export async function callsWith(peer) {
    if (cache.has(peer)) return cache.get(peer);
    const current = generation;
    let list = [];
    try { list = (await getSealed(Stores.calls, peer)) || []; } catch { list = []; }
    if (current !== generation) return [];
    if (!cache.has(peer)) cache.set(peer, Array.isArray(list) ? list : []);
    return cache.get(peer);
}

/** Adds one finished call; a record with a known ID replaces the old one. */
export function recordCall(record) {
    if (!record?.peer || !record.id) return Promise.resolve();
    const current = generation;
    writes = writes.then(async () => {
        if (current !== generation) return;
        const list = (await callsWith(record.peer)).filter(entry => entry.id !== record.id);
        if (current !== generation) return;
        list.push(record);
        list.sort((a, b) => a.startedAt - b.startedAt);
        if (list.length > MAX_PER_PEER) list.splice(0, list.length - MAX_PER_PEER);
        cache.set(record.peer, list);
        await putSealed(Stores.calls, record.peer, list).catch(() => {});
        callLog.dispatchEvent(new CustomEvent("change", { detail: { peer: record.peer } }));
    });
    return writes;
}

/** Deletes the call history with peer on this device. */
export function clearCalls(peer) {
    const current = generation;
    writes = writes.then(async () => {
        if (current !== generation) return;
        cache.set(peer, []);
        await deleteKey(Stores.calls, peer).catch(() => {});
        callLog.dispatchEvent(new CustomEvent("change", { detail: { peer } }));
    });
    return writes;
}

/** Records every finished call of a Calls instance. */
export function watchCallLog(calls) {
    const current = generation;
    calls.addEventListener("record", event => { if (current === generation) recordCall(event.detail).catch(() => {}); });
}

/** Drop the account cache and drain writes before sign-out clears IndexedDB. */
export async function resetCallLog() {
    generation++;
    cache.clear();
    await writes;
}
