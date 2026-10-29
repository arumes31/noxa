// db.js — the app's IndexedDB database. Values in sealed stores are
// encrypted with a non-extractable AES-GCM key kept in the same database,
// so page script can use but never export it.
const DB_NAME = "noxa";
// Version 8 adds durable ratchet records without rewriting existing stores.
const VERSION = 8;

/** Object stores: device identity, direct-message history, identity pins, unsent messages, saved messages, activity center, call history, content shared into the app. */
export const Stores = Object.freeze({ device: "device", dm: "dm", pins: "pins", outbox: "outbox", saved: "saved", activity: "activity", calls: "calls", share: "share", ratchet: "ratchet" });

export function openDB(indexedDBImpl = globalThis.indexedDB) {
    return new Promise((resolve, reject) => {
        const request = indexedDBImpl.open(DB_NAME, VERSION);
        request.onupgradeneeded = () => {
            for (const name of Object.values(Stores)) {
                if (!request.result.objectStoreNames.contains(name)) request.result.createObjectStore(name);
            }
        };
        request.onsuccess = () => { request.result.onversionchange = () => request.result.close(); resolve(request.result); };
        request.onerror = () => reject(request.error);
    });
}

export function run(db, storeName, mode, operation) {
    return new Promise((resolve, reject) => {
        const tx = db.transaction(storeName, mode);
        const request = operation(tx.objectStore(storeName));
        tx.oncomplete = () => resolve(request?.result);
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
    });
}

export async function wrappingKey(db, create = true) {
    const existing = await run(db, Stores.device, "readonly", store => store.get("wrapping-key"));
    if (existing) return existing;
    if (!create) throw new Error("local wrapping key is missing");
    const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
    // Winner selection is one readwrite transaction. Two first tabs can both
    // generate, but only the winning key may encrypt any persistent data.
    return new Promise((resolve, reject) => {
        const tx = db.transaction(Object.values(Stores), "readwrite", { durability: "strict" });
        const store = tx.objectStore(Stores.device);
        let winner, failure;
        const read = store.get("wrapping-key");
        read.onsuccess = () => {
            if (read.result) { winner = read.result; return; }
            // Missing key with existing encrypted data is corruption, not a new
            // browser profile. Never replace it and orphan those records.
            let pending = Object.values(Stores).length;
            const checked = exists => {
                if (exists) { failure = new Error("local wrapping key is missing for existing data"); tx.abort(); return; }
                if (--pending === 0) { winner = key; store.add(key, "wrapping-key"); }
            };
            const identity = store.get("identity"); identity.onsuccess = () => checked(!!identity.result);
            for (const name of Object.values(Stores)) if (name !== Stores.device) {
                const count = tx.objectStore(name).count(); count.onsuccess = () => checked(count.result > 0);
            }
        };
        tx.oncomplete = () => resolve(winner);
        tx.onabort = tx.onerror = () => reject(failure || tx.error || new Error("wrapping-key transaction aborted"));
    });
}

export async function seal(db, plain, additionalData = new Uint8Array()) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const sealed = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData }, await wrappingKey(db), plain));
    return { iv, sealed };
}

export async function unseal(db, record, additionalData = new Uint8Array()) {
    return new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: record.iv, additionalData }, await wrappingKey(db, false), record.sealed));
}

async function withDB(work) {
    const db = await openDB();
    try {
        return await work(db);
    } finally {
        db.close();
    }
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** Stores a JSON value encrypted. */
export function putSealed(storeName, key, value) {
    return withDB(async db => {
        const fence = await identityFence(db);
        const record = await seal(db, encoder.encode(JSON.stringify(value)));
        await fencedWrite(db, [storeName], fence, tx => tx.objectStore(storeName).put(record, key));
    });
}

/** The current persistent identity fence ({generation, active}). */
export function currentFence() {
    return withDB(db => identityFence(db));
}

/**
 * putSealed that commits only while the identity fence still equals expected
 * (captured before a long operation): a write resumed after logout or an
 * identity replacement fails with code "fenced" instead of resurrecting data.
 */
export function putSealedIn(expected, storeName, key, value) {
    return withDB(async db => {
        const record = await seal(db, encoder.encode(JSON.stringify(value)));
        await fencedWrite(db, [storeName], expected, tx => tx.objectStore(storeName).put(record, key));
    });
}

const sameRecord = (a, b) => (!a && !b) || (!!a && !!b && a.iv?.length === b.iv?.length && Array.from(a.iv || []).every((v, i) => v === b.iv[i]));

/**
 * Read-modify-write of one sealed value as a compare-and-swap: mutate(value)
 * gets the current value (undefined when absent) and returns the next value,
 * or undefined to leave it unchanged. The write commits only if the record is
 * still the one that was read (and, with expected, the identity fence still
 * matches); otherwise it retries. A record that cannot be unsealed throws
 * (code "unreadable") and is never overwritten.
 */
export function updateSealed(storeName, key, mutate, expected = null) {
    return withDB(async db => {
        for (let attempt = 0; attempt < 8; attempt++) {
            const before = await run(db, storeName, "readonly", store => store.get(key));
            let value;
            if (before) {
                try { value = JSON.parse(decoder.decode(await unseal(db, before))); }
                catch { throw Object.assign(new Error("stored entry cannot be read"), { code: "unreadable" }); }
            }
            const next = mutate(value);
            if (next === undefined) return value;
            const record = await seal(db, encoder.encode(JSON.stringify(next)));
            const fence = expected || await identityFence(db);
            let lost = false;
            await fencedWrite(db, [storeName], fence, tx => {
                const st = tx.objectStore(storeName), current = st.get(key);
                current.onsuccess = () => {
                    if (!sameRecord(current.result, before)) { lost = true; return; }
                    st.put(record, key);
                };
            });
            if (!lost) return next;
        }
        throw Object.assign(new Error("stored entry changed concurrently"), { code: "conflict" });
    });
}

/** deleteKey under the same identity-fence rule as putSealedIn. */
export function deleteKeyIn(expected, storeName, key) {
    return withDB(db => fencedWrite(db, [storeName], expected, tx => tx.objectStore(storeName).delete(key)));
}

/** Reads a JSON value stored with putSealed, or undefined. */
export function getSealed(storeName, key) {
    return withDB(async db => {
        const record = await run(db, storeName, "readonly", store => store.get(key));
        if (!record) return undefined;
        return JSON.parse(decoder.decode(await unseal(db, record)));
    });
}

/** Lists the keys of a store. */
export function keys(storeName) {
    return withDB(db => run(db, storeName, "readonly", store => store.getAllKeys()));
}

export function clearStores(...names) {
    return withDB(async db => {
        for (const name of names) await run(db, name, "readwrite", store => store.clear());
    });
}

export function deleteKey(storeName, key) {
    return withDB(db => run(db, storeName, "readwrite", store => store.delete(key)));
}

// Persistent generation is shared by tabs and survives logout. No cached
// generation may authorize a write after an awaited crypto operation.
export const defaultFence = () => ({ generation: 1, active: true });
export async function identityFence(db) {
    return await run(db, Stores.device, "readonly", s => s.get("identity-fence")) || defaultFence();
}
export function fencedWrite(db, names, expected, work, { onboarding = false } = {}) {
    return new Promise((resolve, reject) => {
        const tx = db.transaction([...new Set([Stores.device, ...names])], "readwrite", { durability: "strict" });
        let failure, result;
        const req = tx.objectStore(Stores.device).get("identity-fence");
        req.onsuccess = () => {
            const current = req.result || defaultFence();
            if (current.generation !== expected.generation || (!current.active && !onboarding)) {
                failure = Object.assign(new Error("identity operation fenced"), { code: "fenced" }); tx.abort(); return;
            }
            try { result = work(tx, current); } catch (error) { failure = error; tx.abort(); }
        };
        tx.oncomplete = () => resolve(result);
        tx.onabort = tx.onerror = () => reject(failure || tx.error || new Error("transaction aborted"));
    });
}
