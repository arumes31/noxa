// identity-store.js — this device's keys and link state in IndexedDB. Key
// bytes are encrypted with a non-extractable AES-GCM key that also lives in
// IndexedDB, so the raw secrets are never stored in readable form and the
// wrapping key cannot be exported by page script.
const DB_NAME = "noxa";
const STORE = "device";

function openDB(indexedDBImpl = globalThis.indexedDB) {
    return new Promise((resolve, reject) => {
        const request = indexedDBImpl.open(DB_NAME, 1);
        request.onupgradeneeded = () => request.result.createObjectStore(STORE);
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
    });
}

function run(db, mode, operation) {
    return new Promise((resolve, reject) => {
        const tx = db.transaction(STORE, mode);
        const request = operation(tx.objectStore(STORE));
        tx.oncomplete = () => resolve(request?.result);
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
    });
}

async function wrappingKey(db) {
    const existing = await run(db, "readonly", store => store.get("wrapping-key"));
    if (existing) return existing;
    const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
    await run(db, "readwrite", store => store.put(key, "wrapping-key"));
    return key;
}

const FIELDS = ["edPublic", "edSecret", "xPublic", "xSecret"];

/** Stores the device identity and any link metadata (deviceID, principal). */
export async function saveIdentity(identity, meta = {}) {
    const db = await openDB();
    try {
        const key = await wrappingKey(db);
        const iv = crypto.getRandomValues(new Uint8Array(12));
        const plain = new Uint8Array(FIELDS.reduce((n, f) => n + identity[f].length, 0));
        let offset = 0;
        for (const field of FIELDS) {
            plain.set(identity[field], offset);
            offset += identity[field].length;
        }
        const sealed = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, plain));
        plain.fill(0);
        await run(db, "readwrite", store => store.put({ iv, sealed, meta }, "identity"));
    } finally {
        db.close();
    }
}

/** Returns {identity, meta} or null when this browser has no device yet. */
export async function loadIdentity() {
    const db = await openDB();
    try {
        const record = await run(db, "readonly", store => store.get("identity"));
        if (!record) return null;
        const key = await wrappingKey(db);
        const plain = new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: record.iv }, key, record.sealed));
        const sizes = { edPublic: 32, edSecret: 64, xPublic: 32, xSecret: 32 };
        const identity = {};
        let offset = 0;
        for (const field of FIELDS) {
            identity[field] = plain.slice(offset, offset + sizes[field]);
            offset += sizes[field];
        }
        plain.fill(0);
        return { identity, meta: record.meta || {} };
    } finally {
        db.close();
    }
}

export async function updateMeta(patch) {
    const current = await loadIdentity();
    if (!current) return;
    await saveIdentity(current.identity, { ...current.meta, ...patch });
}

/** Forgets this device, e.g. after it was signed out. */
export async function clearIdentity() {
    const db = await openDB();
    try {
        await run(db, "readwrite", store => store.delete("identity"));
    } finally {
        db.close();
    }
}
