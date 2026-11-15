import test from "node:test";
import assert from "node:assert/strict";
import { clearInlineThumbnails, createThumbCache, inlineThumbnail, thumbSize, THUMB_MAX_SIDE } from "../src/image-thumbs.js";

test("thumbnail size respects the cap and keeps the aspect ratio", () => {
    assert.deepEqual(thumbSize(4000, 3000), { width: THUMB_MAX_SIDE, height: 480 });
    assert.deepEqual(thumbSize(1000, 4000, 640), { width: 160, height: 640 });
    assert.deepEqual(thumbSize(100000, 10, 640), { width: 640, height: 1 });
    assert.equal(thumbSize(640, 400), null, "at the cap keeps the original");
    assert.equal(thumbSize(320, 200), null, "under the cap keeps the original");
    assert.equal(thumbSize(NaN, NaN), null);
});

test("thumbnail cache evicts the least recently used entry and revokes it", () => {
    const revoked = [];
    const cache = createThumbCache(2, url => revoked.push(url));
    cache.set("a", "blob:a"); cache.set("b", "blob:b");
    assert.equal(cache.get("a"), "blob:a"); // a is now most recent
    cache.set("c", "blob:c");
    assert.deepEqual(revoked, ["blob:b"]);
    assert.equal(cache.get("b"), undefined);
    assert.equal(cache.size, 2);
    cache.set("d", null); // under-cap marker: nothing to revoke when it goes
    assert.deepEqual(revoked, ["blob:b", "blob:a"]);
    cache.set("e", "blob:e");
    assert.deepEqual(revoked, ["blob:b", "blob:a", "blob:c"]);
    cache.set("f", "blob:f");
    assert.deepEqual(revoked, ["blob:b", "blob:a", "blob:c"], "evicting the null marker revokes nothing");
    cache.set("f", "blob:f2");
    assert.deepEqual(revoked, ["blob:b", "blob:a", "blob:c", "blob:f"], "replacing a key revokes its old URL");
});

test("GIFs and attachments without a stable key keep the original", async () => {
    assert.equal(await inlineThumbnail("k", "data:image/gif;base64,R0lG", "image/gif"), null);
    assert.equal(await inlineThumbnail(undefined, "data:image/png;base64,iVBO", "image/png"), null);
});

test("a decode failure rejects so the caller keeps the full-size path", async () => {
    await assert.rejects(inlineThumbnail("broken", "data:image/png;base64,AAAA", "image/png"));
});

test("clear revokes every cached thumbnail", () => {
    const revoked = [];
    const cache = createThumbCache(10, url => revoked.push(url));
    cache.set("a", "blob:a"); cache.set("b", null); cache.set("c", "blob:c");
    cache.clear();
    assert.deepEqual(revoked, ["blob:a", "blob:c"]);
    assert.equal(cache.size, 0);
    assert.equal(cache.get("a"), undefined);
    clearInlineThumbnails(); // the module cache resets without throwing
});
