// identity-store.js — this device's keys and link state in IndexedDB. Key
// bytes are encrypted with the database's non-extractable AES-GCM key, so
// the raw secrets are never stored in readable form.
import { Stores, clearStores, openDB, run, seal, unseal } from "./db.js";

const FIELDS = ["edPublic", "edSecret", "xPublic", "xSecret"];
const SIZES = { edPublic: 32, edSecret: 64, xPublic: 32, xSecret: 32 };

/** Stores the device identity and any link metadata (deviceID, principal). */
export async function saveIdentity(identity, meta = {}) {
    const db = await openDB();
    try {
        const plain = new Uint8Array(FIELDS.reduce((n, f) => n + identity[f].length, 0));
        let offset = 0;
        for (const field of FIELDS) {
            plain.set(identity[field], offset);
            offset += identity[field].length;
        }
        const { iv, sealed } = await seal(db, plain);
        plain.fill(0);
        await run(db, Stores.device, "readwrite", store => store.put({ iv, sealed, meta }, "identity"));
    } finally {
        db.close();
    }
}

/** Returns {identity, meta} or null when this browser has no device yet. */
export async function loadIdentity() {
    const db = await openDB();
    try {
        const record = await run(db, Stores.device, "readonly", store => store.get("identity"));
        if (!record) return null;
        const plain = await unseal(db, record);
        const identity = {};
        let offset = 0;
        for (const field of FIELDS) {
            identity[field] = plain.slice(offset, offset + SIZES[field]);
            offset += SIZES[field];
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

/** Forgets this device and its messages, e.g. after it was signed out. */
export async function clearIdentity() {
    const db = await openDB();
    try {
        await run(db, Stores.device, "readwrite", store => store.delete("identity"));
    } finally {
        db.close();
    }
    await clearStores(Stores.dm, Stores.pins, Stores.outbox, Stores.saved);
}
