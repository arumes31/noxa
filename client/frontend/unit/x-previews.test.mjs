import test from "node:test";
import assert from "node:assert/strict";
import { xPost } from "../src/x-previews.js";
import { loadPreviewMetadata } from "../src/preview-metadata.js";

test("X previews accept canonical, legacy and media post URLs", () => {
    for (const url of ["https://x.com/jack/status/20", "https://twitter.com/jack/status/20?s=20", "https://www.x.com/i/status/20", "https://mobile.twitter.com/jack/status/20/photo/1", "https://x.com/i/web/status/20", "http://x.com/jack/status/20/video/1"]) {
        assert.deepEqual(xPost(url), { id: "20", url: "https://x.com/i/status/20" });
    }
});
test("X previews reject arbitrary hosts, credentials, malformed IDs and non-post URLs", () => {
    for (const url of ["https://x.com.evil.test/jack/status/20", "https://evil.test/x.com/jack/status/20", "https://user@x.com/jack/status/20", "https://x.com:8443/jack/status/20", "javascript:alert(1)", "https://x.com/jack/status/0", "https://x.com/jack/status/123456789012345678901", "https://x.com/jack/status/20%2Fprivate", "https://x.com/jack", "", null]) assert.equal(xPost(url), null, String(url));
});
test("metadata cache separates providers and connection generations", async () => {
    let calls = 0;
    const request = () => ++calls;
    const scope = { tabID: "cache", generation: 1 };
    assert.deepEqual(await Promise.all([loadPreviewMetadata("x", "20", scope, () => true, request), loadPreviewMetadata("x", "20", scope, () => true, request)]), [1, 1]);
    assert.equal(await loadPreviewMetadata("youtube", "20", scope, () => true, request), 2);
    assert.equal(await loadPreviewMetadata("x", "20", { ...scope, generation: 2 }, () => true, request), 3);
    assert.equal(await loadPreviewMetadata("x", "failure", scope, () => true, () => Promise.reject(Error("unavailable"))), null);
});
test("providers share a four-request limit and skip stale queued work", async () => {
    const release = [];
    let calls = 0, current = true;
    const jobs = Array.from({ length: 6 }, (_, i) => loadPreviewMetadata(i % 2 ? "x" : "youtube", String(i), { tabID: "queue", generation: 1 }, () => i < 4 || current, () => { calls++; return new Promise(resolve => release.push(resolve)); }));
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(calls, 4);
    current = false;
    for (const resolve of release) resolve("loaded");
    assert.deepEqual(await Promise.all(jobs), ["loaded", "loaded", "loaded", "loaded", null, null]);
    assert.equal(calls, 4);
    assert.equal(await loadPreviewMetadata("x", "5", { tabID: "queue", generation: 1 }, () => true, () => "retried"), "retried");
});

test("a current card retries a shared job skipped for an earlier detached card", async () => {
    const release = [];
    const scope = { tabID: "shared-skip", generation: 1 };
    const blockers = Array.from({ length: 4 }, (_, i) => loadPreviewMetadata("youtube", String(i), scope, () => true, () => new Promise(resolve => release.push(resolve))));
    await new Promise(resolve => setTimeout(resolve, 0));
    let originalCurrent = true, oldCalls = 0, currentCalls = 0;
    const oldCard = loadPreviewMetadata("x", "20", scope, () => originalCurrent, () => { oldCalls++; return "old"; });
    const newCard = loadPreviewMetadata("x", "20", scope, () => true, () => { currentCalls++; return "current"; });
    originalCurrent = false;
    for (const resolve of release) resolve("done");
    await Promise.all(blockers);
    assert.equal(await oldCard, null);
    assert.equal(await newCard, "current");
    assert.equal(oldCalls, 0);
    assert.equal(currentCalls, 1);
});


test("transient preview failures expire for every provider", async t => {
    t.mock.timers.enable({ apis: ["Date"], now: 100000 });
    for (const provider of ["youtube", "x", "portal:twitch"]) {
        const scope = { tabID: "failure-expiry", generation: 1 };
        let calls = 0;
        const request = () => { calls++; if (calls === 1) throw Error("rate limited"); return { title: "Recovered" }; };
        assert.equal(await loadPreviewMetadata(provider, "retry", scope, () => true, request), null);
        assert.equal(await loadPreviewMetadata(provider, "retry", scope, () => true, request), null);
        assert.equal(calls, 1);
        t.mock.timers.tick(5001);
        assert.deepEqual(await loadPreviewMetadata(provider, "retry", scope, () => true, request), { title: "Recovered" });
        assert.equal(calls, 2);
    }
});
