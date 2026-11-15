import test from "node:test";
import assert from "node:assert/strict";
import { animatedPNG, animatedWebP, clearInlineThumbnails, createThumbCache, inlineThumbnail, thumbSize, THUMB_MAX_SIDE } from "../src/image-thumbs.js";

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

const webp = (chunk, extra = "", flags = 0) => {
    const body = "WEBP" + chunk + "\x0a\x00\x00\x00" + String.fromCharCode(flags) + "\x00".repeat(9) + extra;
    return new Uint8Array([..."RIFF\x00\x00\x00\x00" + body].map(c => c.charCodeAt(0)));
};
// Node has no createImageBitmap; each test installs its own.
const stubBitmap = (t, impl) => { globalThis.createImageBitmap = impl; t.after(() => { delete globalThis.createImageBitmap; }); };
const dataURL = bytes => `data:image/webp;base64,${Buffer.from(bytes).toString("base64")}`;

test("animated WebP bypasses the thumbnail; static WebP is thumbnailed", async (t) => {
    const animated = webp("VP8X", "ANIM\x06\x00\x00\x00", 0x02), still = webp("VP8 ");
    assert.equal(animatedWebP(animated), true);
    assert.equal(animatedWebP(still), false);
    assert.equal(animatedWebP(webp("VP8X", "ICCP", 0x20)), false, "extended but not animated");
    assert.equal(animatedWebP(webp("VP8X", "ICCP" + "x".repeat(200) + "ANIM", 0x22)), true, "ICC profile before ANIM: the flag still detects animation");
    let decodes = 0;
    stubBitmap(t, async () => { decodes++; return { width: 10, height: 10, close() {} }; });
    assert.equal(await inlineThumbnail("anim", dataURL(animated), "image/webp"), null);
    assert.equal(decodes, 0, "an animated WebP is never decoded for a thumbnail");
    assert.equal(await inlineThumbnail("still", dataURL(still), "image/webp"), null, "under the cap");
    assert.equal(decodes, 1, "a static WebP goes through the thumbnail path");
});

test("at most two thumbnail renders run at once", async (t) => {
    const pending = [];
    let outstanding = 0, peak = 0;
    stubBitmap(t, () => new Promise(resolve => {
        outstanding++; peak = Math.max(peak, outstanding);
        pending.push(() => { outstanding--; resolve({ width: 10, height: 10, close() {} }); });
    }));
    const settle = () => new Promise(resolve => setTimeout(resolve, 20));
    const png = "data:image/png;base64,iVBORw0KGgo=";
    const requests = [1, 2, 3, 4, 5].map(i => inlineThumbnail(`limit-${i}`, png, "image/png"));
    await settle();
    assert.equal(pending.length, 2);
    while (pending.length) { pending.shift()(); await settle(); }
    assert.deepEqual(await Promise.all(requests), [null, null, null, null, null]);
    assert.equal(peak, 2);
});

const chunk = (type, length = 0) => {
    const bytes = new Uint8Array(12 + length);
    new DataView(bytes.buffer).setUint32(0, length);
    bytes.set([...type].map(c => c.charCodeAt(0)), 4);
    return bytes;
};
const png = (...chunks) => {
    const parts = [new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), ...chunks];
    const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let at = 0;
    for (const part of parts) { out.set(part, at); at += part.length; }
    return out;
};
const pngURL = bytes => `data:image/png;base64,${Buffer.from(bytes).toString("base64")}`;

test("animated PNG bypasses the thumbnail; plain PNG is thumbnailed", async (t) => {
    const apng = png(chunk("IHDR", 13), chunk("acTL", 8), chunk("IDAT", 4), chunk("IEND"));
    const plain = png(chunk("IHDR", 13), chunk("IDAT", 4), chunk("acTL", 8), chunk("IEND"));
    assert.equal(animatedPNG(apng), true);
    assert.equal(animatedPNG(plain), false, "acTL after IDAT is not an animation control");
    let decodes = 0;
    stubBitmap(t, async () => { decodes++; return { width: 10, height: 10, close() {} }; });
    assert.equal(await inlineThumbnail("apng", pngURL(apng), "image/png"), null);
    assert.equal(decodes, 0, "an APNG is never decoded for a thumbnail");
    assert.equal(await inlineThumbnail("plain-png", pngURL(plain), "image/png"), null, "under the cap");
    assert.equal(decodes, 1, "a plain PNG goes through the thumbnail path");
});

test("a queued render whose caller left the view is skipped without decoding", async (t) => {
    const pending = [];
    let decodes = 0;
    stubBitmap(t, () => new Promise(resolve => { decodes++; pending.push(() => resolve({ width: 10, height: 10, close() {} })); }));
    const settle = () => new Promise(resolve => setTimeout(resolve, 20));
    const url = pngURL(png(chunk("IHDR", 13), chunk("IDAT", 4)));
    let left = false;
    const busy = [1, 2].map(i => inlineThumbnail(`busy-${i}`, url, "image/png"));
    const stale = inlineThumbnail("left-channel", url, "image/png", () => !left);
    await settle();
    assert.equal(decodes, 2, "the third render waits in the queue");
    left = true;
    while (pending.length) { pending.shift()(); await settle(); }
    assert.equal(await stale, null);
    assert.equal(decodes, 2, "the stale queued render never decoded");
    await Promise.all(busy);
    const again = inlineThumbnail("left-channel", url, "image/png");
    await settle();
    assert.equal(decodes, 3, "a skipped render is not cached, so a current view renders it");
    pending.shift()();
    assert.equal(await again, null);
});
