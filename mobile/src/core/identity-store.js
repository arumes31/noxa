// identity-store.js — this device's keys and link state in IndexedDB. Key
// bytes are encrypted with the database's non-extractable AES-GCM key, so
// the raw secrets are never stored in readable form.
import { Stores, defaultFence, identityFence, fencedWrite, openDB, run, seal, unseal } from "./db.js";

import { clearAccountSettings } from "./settings.js";

const FIELDS = ["edPublic", "edSecret", "xPublic", "xSecret"];
const SIZES = { edPublic: 32, edSecret: 64, xPublic: 32, xSecret: 32 };

/** Stores the device identity and any link metadata (deviceID, principal). */
export async function saveIdentity(identity, meta = {}, { onboarding = false, fence } = {}) {
    if (onboarding && !fence) throw new Error("explicit onboarding fence required");
    for (const field of FIELDS) if (!(identity[field] instanceof Uint8Array) || identity[field].length !== SIZES[field]) throw new Error("invalid device identity");
    const db = await openDB();
    try {
        const expected = fence || await identityFence(db);
        const plain = new Uint8Array(FIELDS.reduce((n, f) => n + identity[f].length, 0));
        let offset = 0;
        for (const field of FIELDS) {
            plain.set(identity[field], offset);
            offset += identity[field].length;
        }
        const { iv, sealed } = await seal(db, plain);
        plain.fill(0);
        await fencedWrite(db, Object.values(Stores), expected, (tx, current) => {
            const device = tx.objectStore(Stores.device);
            if (onboarding) {
                if (current.generation >= Number.MAX_SAFE_INTEGER) throw new Error("identity generation exhausted");
                device.put({ generation: current.generation + 1, active: true }, "identity-fence");
            }
            device.put({ iv, sealed, meta }, "identity");
        }, { onboarding });
    } finally {
        db.close();
    }
}

/** Returns {identity, meta} or null when this browser has no device yet. */
export async function loadIdentity() {
    const db = await openDB();
    try {
        const expected = await identityFence(db);
        if (!expected.active) return null;
        const record = await run(db, Stores.device, "readonly", store => store.get("identity"));
        if (!record) return null;
        const plain = await unseal(db, record);
        if (plain.length !== 160) { plain.fill(0); throw new Error("unreadable device identity"); }
        const identity = {};
        let offset = 0;
        for (const field of FIELDS) {
            identity[field] = plain.slice(offset, offset + SIZES[field]);
            offset += SIZES[field];
        }
        plain.fill(0);
        try { await fencedWrite(db, [], expected, () => {}); } catch (error) { if (error.code === "fenced") return null; throw error; }
        return { identity, meta: record.meta || {} };
    } finally {
        db.close();
    }
}

export async function updateMeta(patch) {
    const db = await openDB();
    try {
        const expected = await identityFence(db);
        await fencedWrite(db, [], expected, tx => {
            const store = tx.objectStore(Stores.device);
            const read = store.get("identity");
            read.onsuccess = () => { if (read.result) store.put({ ...read.result, meta: { ...read.result.meta, ...patch } }, "identity"); };
        });
    } finally { db.close(); }
}

/** Explicit onboarding token. Capture before asynchronous link/sign-in work. */
export async function beginIdentity() {
    const db = await openDB();
    try { return await identityFence(db); } finally { db.close(); }
}

/** Logout clears account data and advances its fence in ONE transaction. */
export async function clearIdentity() {
    const db = await openDB();
    try {
        const next = await new Promise((resolve, reject) => {
            const tx = db.transaction(Object.values(Stores), "readwrite", { durability: "strict" });
            const device = tx.objectStore(Stores.device);
            const read = device.get("identity-fence");
            let next, failure;
            read.onsuccess = () => {
                const current = read.result || defaultFence();
                if (current.generation >= Number.MAX_SAFE_INTEGER) { failure = new Error("identity generation exhausted"); tx.abort(); return; }
                next = { generation: current.generation + 1, active: false };
                const keyRead = device.get("wrapping-key");
                keyRead.onsuccess = () => {
                    device.clear();
                    if (keyRead.result) device.put(keyRead.result, "wrapping-key");
                    device.put(next, "identity-fence");
                };
                for (const name of Object.values(Stores)) if (name !== Stores.device) tx.objectStore(name).clear();
            };
            tx.oncomplete = () => resolve(next);
            tx.onabort = tx.onerror = () => reject(failure || tx.error || new Error("identity clear aborted"));
        });
        clearAccountSettings();
        return next;
    } finally { db.close(); }
}
