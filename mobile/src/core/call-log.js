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

/** The calls with peer, oldest first. */
export async function callsWith(peer) {
    if (cache.has(peer)) return cache.get(peer);
    let list = [];
    try { list = (await getSealed(Stores.calls, peer)) || []; } catch { list = []; }
    if (!cache.has(peer)) cache.set(peer, Array.isArray(list) ? list : []);
    return cache.get(peer);
}

/** Adds one finished call; a record with a known ID replaces the old one. */
export function recordCall(record) {
    if (!record?.peer || !record.id) return Promise.resolve();
    writes = writes.then(async () => {
        const list = (await callsWith(record.peer)).filter(entry => entry.id !== record.id);
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
    writes = writes.then(async () => {
        cache.set(peer, []);
        await deleteKey(Stores.calls, peer).catch(() => {});
        callLog.dispatchEvent(new CustomEvent("change", { detail: { peer } }));
    });
    return writes;
}

/** Records every finished call of a Calls instance. */
export function watchCallLog(calls) {
    calls.addEventListener("record", event => { recordCall(event.detail).catch(() => {}); });
}
