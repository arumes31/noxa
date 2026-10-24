// db.js — the app's IndexedDB database. Values in sealed stores are
// encrypted with a non-extractable AES-GCM key kept in the same database,
// so page script can use but never export it.
const DB_NAME = "noxa";
const VERSION = 2;

/** Object stores: device identity, direct-message history, identity pins. */
export const Stores = Object.freeze({ device: "device", dm: "dm", pins: "pins" });

export function openDB(indexedDBImpl = globalThis.indexedDB) {
    return new Promise((resolve, reject) => {
        const request = indexedDBImpl.open(DB_NAME, VERSION);
        request.onupgradeneeded = () => {
            for (const name of Object.values(Stores)) {
                if (!request.result.objectStoreNames.contains(name)) request.result.createObjectStore(name);
            }
        };
        request.onsuccess = () => resolve(request.result);
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

export async function wrappingKey(db) {
    const existing = await run(db, Stores.device, "readonly", store => store.get("wrapping-key"));
    if (existing) return existing;
    const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
    await run(db, Stores.device, "readwrite", store => store.put(key, "wrapping-key"));
    return key;
}

export async function seal(db, plain) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const sealed = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv }, await wrappingKey(db), plain));
    return { iv, sealed };
}

export async function unseal(db, record) {
    return new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: record.iv }, await wrappingKey(db), record.sealed));
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
        const record = await seal(db, encoder.encode(JSON.stringify(value)));
        await run(db, storeName, "readwrite", store => store.put(record, key));
    });
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
