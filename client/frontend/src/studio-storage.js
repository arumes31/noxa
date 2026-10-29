// studio-storage.js — where the Studio keeps its layout and picked images.
//
// Both are scoped per identity (the key-derived unique ID of the account the
// Studio runs for), so another identity on this device never sees them, and
// identity deletion wipes them (security-settings.js). A sign-out or identity
// switch keeps them. The pre-scoping global layout and image database cannot
// be attributed to an identity, so they are dropped instead of migrated.
import { STUDIO_STORAGE_KEY } from "./studio-state.js";

export const LEGACY_STUDIO_DB = "noxa-studio";

const validIdentity = uid => typeof uid === "string" && uid.length > 0 && uid.length <= 256;

/** localStorage key of an identity's Studio layout ("" without an identity). */
export function studioLayoutKey(uid) {
    return validIdentity(uid) ? `${STUDIO_STORAGE_KEY}:${uid}` : "";
}

/** IndexedDB name of an identity's Studio images ("" without an identity). */
export function studioAssetDB(uid) {
    return validIdentity(uid) ? `${LEGACY_STUDIO_DB}:${uid}` : "";
}

function localStore(storage) {
    if (storage !== undefined) return storage;
    try { return globalThis.localStorage; } catch { return null; }
}

function deleteDB(indexedDB, name) {
    if (!indexedDB || !name) return Promise.resolve(false);
    return new Promise(resolve => {
        try {
            const request = indexedDB.deleteDatabase(name);
            request.onsuccess = () => resolve(true);
            request.onerror = () => resolve(false);
            request.onblocked = () => resolve(false);
        } catch { resolve(false); }
    });
}

/** Drops the unscoped layout and image database of older builds. */
export async function dropLegacyStudio({ storage, indexedDB = globalThis.indexedDB } = {}) {
    try { localStore(storage)?.removeItem(STUDIO_STORAGE_KEY); } catch { /* storage is optional */ }
    await deleteDB(indexedDB, LEGACY_STUDIO_DB);
}

/** Erases one identity's Studio layout and images (identity deletion). */
export async function wipeStudioIdentity(uid, { storage, indexedDB = globalThis.indexedDB } = {}) {
    const key = studioLayoutKey(uid);
    if (!key) return false;
    try { localStore(storage)?.removeItem(key); } catch { /* storage is optional */ }
    return deleteDB(indexedDB, studioAssetDB(uid));
}

/**
 * Removes Studio layouts and image databases of identities that no longer
 * exist on this device. It completes a deletion whose webview wipe was cut
 * off (crash or window close after the native deletion committed). `entries`
 * is App.ListIdentities(); nothing is removed unless every entry has a uid
 * (an unreadable identity could still own data). Returns the removed uids.
 */
export async function sweepOrphanStudio(entries, { storage, indexedDB = globalThis.indexedDB } = {}) {
    if (!Array.isArray(entries) || !entries.length || entries.some(e => !validIdentity(e?.unique_id) || e?.error)) return [];
    const live = new Set(entries.map(e => e.unique_id));
    const orphans = new Set();
    const store = localStore(storage);
    const prefix = `${STUDIO_STORAGE_KEY}:`;
    try {
        for (let i = 0; store && i < store.length; i++) {
            const key = store.key(i);
            if (key?.startsWith(prefix) && !live.has(key.slice(prefix.length))) orphans.add(key.slice(prefix.length));
        }
    } catch { /* storage is optional */ }
    try {
        for (const db of (await indexedDB?.databases?.()) || []) {
            const name = String(db?.name || "");
            if (name.startsWith(`${LEGACY_STUDIO_DB}:`) && !live.has(name.slice(LEGACY_STUDIO_DB.length + 1))) orphans.add(name.slice(LEGACY_STUDIO_DB.length + 1));
        }
    } catch { /* databases() is optional */ }
    for (const uid of orphans) await wipeStudioIdentity(uid, { storage, indexedDB });
    return [...orphans];
}
