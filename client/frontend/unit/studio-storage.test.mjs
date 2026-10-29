import test from "node:test";
import assert from "node:assert/strict";
import { studioLayoutKey, studioAssetDB, wipeStudioIdentity, dropLegacyStudio, sweepOrphanStudio, studioStoredIdentities, LEGACY_STUDIO_DB } from "../src/studio-storage.js";
import { STUDIO_STORAGE_KEY } from "../src/studio-state.js";

function fakes() {
    const items = new Map(), deleted = [];
    const storage = { getItem: k => items.get(k) ?? null, setItem: (k, v) => items.set(k, String(v)), removeItem: k => items.delete(k) };
    const indexedDB = { deleteDatabase(name) {
        deleted.push(name);
        const request = {};
        queueMicrotask(() => request.onsuccess?.());
        return request;
    } };
    return { items, deleted, storage, indexedDB };
}

// P3-5: Studio data is keyed per identity; without an identity nothing is
// persisted under a shared key.
test("Studio layout and images are keyed by identity", () => {
    assert.equal(studioLayoutKey("uid-a"), `${STUDIO_STORAGE_KEY}:uid-a`);
    assert.equal(studioAssetDB("uid-a"), `${LEGACY_STUDIO_DB}:uid-a`);
    assert.notEqual(studioLayoutKey("uid-a"), studioLayoutKey("uid-b"));
    for (const none of ["", null, undefined, 7, "x".repeat(257)]) {
        assert.equal(studioLayoutKey(none), "");
        assert.equal(studioAssetDB(none), "");
    }
});

test("identity deletion wipes exactly that identity's Studio layout and images", async () => {
    const f = fakes();
    f.storage.setItem(studioLayoutKey("uid-a"), "a");
    f.storage.setItem(studioLayoutKey("uid-b"), "b");
    assert.equal(await wipeStudioIdentity("uid-a", f), true);
    assert.equal(f.items.has(studioLayoutKey("uid-a")), false);
    assert.equal(f.items.get(studioLayoutKey("uid-b")), "b");
    assert.deepEqual(f.deleted, [studioAssetDB("uid-a")]);
    assert.equal(await wipeStudioIdentity("", f), false);
    assert.deepEqual(f.deleted, [studioAssetDB("uid-a")], "no identity, nothing wiped");
});

test("the unscoped layout and image database of older builds are dropped, not migrated", async () => {
    const f = fakes();
    f.storage.setItem(STUDIO_STORAGE_KEY, "legacy");
    f.storage.setItem(studioLayoutKey("uid-a"), "a");
    await dropLegacyStudio(f);
    assert.equal(f.items.has(STUDIO_STORAGE_KEY), false);
    assert.equal(f.items.get(studioLayoutKey("uid-a")), "a");
    assert.deepEqual(f.deleted, [LEGACY_STUDIO_DB]);
});

test("a blocked or failing storage never throws", async () => {
    const throwing = { removeItem() { throw new Error("denied"); } };
    const failingDB = { deleteDatabase() { throw new Error("denied"); } };
    assert.equal(await wipeStudioIdentity("uid-a", { storage: throwing, indexedDB: failingDB }), false);
    await dropLegacyStudio({ storage: null, indexedDB: null });
});

// Task 4 fix review P3-3: a deletion whose webview wipe was cut off is
// completed at the next start; nothing is removed while any identity is
// unreadable or unnamed.
test("startup sweep removes Studio data of identities that no longer exist", async () => {
    const f = fakes();
    for (const uid of ["live", "gone"]) f.items.set(studioLayoutKey(uid), "{}");
    f.items.set("other-key", "x");
    f.storage.key = i => [...f.items.keys()][i] ?? null;
    Object.defineProperty(f.storage, "length", { get: () => f.items.size });
    f.indexedDB.databases = async () => [{ name: studioAssetDB("live") }, { name: studioAssetDB("gone") }, { name: "noxa-other" }];
    assert.deepEqual(await sweepOrphanStudio([{ unique_id: "live" }, { unique_id: "x", error: "unreadable" }], f), []);
    assert.deepEqual(await sweepOrphanStudio([], f), []);
    assert.equal(f.deleted.length, 0);
    assert.deepEqual(await sweepOrphanStudio([{ unique_id: "live" }], f), ["gone"]);
    assert.equal(f.items.has(studioLayoutKey("gone")), false);
    assert.equal(f.items.has(studioLayoutKey("live")), true);
    assert.equal(f.items.has("other-key"), true);
    assert.deepEqual(f.deleted, [studioAssetDB("gone")]);
});

// Startup lists identities only when some Studio data is stored, so a device
// without Studio data never calls App.ListIdentities at launch.
test("stored Studio owners come from layouts and image databases only", async () => {
    const f = fakes();
    f.storage.key = i => [...f.items.keys()][i] ?? null;
    Object.defineProperty(f.storage, "length", { get: () => f.items.size });
    f.indexedDB.databases = async () => [{ name: "noxa-other" }];
    f.items.set("other-key", "x");
    assert.deepEqual(await studioStoredIdentities(f), []);
    f.items.set(studioLayoutKey("a"), "{}");
    f.indexedDB.databases = async () => [{ name: studioAssetDB("b") }, { name: "noxa-other" }];
    assert.deepEqual((await studioStoredIdentities(f)).sort(), ["a", "b"]);
    assert.deepEqual(await studioStoredIdentities({ storage: null, indexedDB: null }), []);
});
