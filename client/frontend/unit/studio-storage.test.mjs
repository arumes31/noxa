import test from "node:test";
import assert from "node:assert/strict";
import { studioLayoutKey, studioAssetDB, wipeStudioIdentity, dropLegacyStudio, LEGACY_STUDIO_DB } from "../src/studio-storage.js";
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
