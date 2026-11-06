import assert from "node:assert/strict";
import test from "node:test";
import { DEFAULT_ROW_PX, buildPrefix, computeWindow, createHeightCache, rowAt } from "../src/virtual-log.js";

const keys = n => Array.from({ length: n }, (_, i) => "k" + i);

test("the height cache estimates unmeasured rows from the average", () => {
    const cache = createHeightCache();
    assert.equal(cache.get("a"), DEFAULT_ROW_PX);
    assert.equal(cache.set("a", 40), true);
    assert.equal(cache.set("a", 40), false, "an unchanged height reports no change");
    cache.set("b", 80);
    assert.equal(cache.get("c"), 60);
    cache.set("a", 20);
    assert.equal(cache.estimate(), 50, "a remeasured row replaces its old height in the average");
    assert.equal(cache.set("d", -1), false);
    assert.equal(cache.set("d", Number.NaN), false);
    assert.equal(cache.size, 2);
    cache.clear();
    assert.equal(cache.get("a"), DEFAULT_ROW_PX);
});

test("prefix sums and rowAt locate rows by offset", () => {
    const cache = createHeightCache(10);
    const k = keys(5);
    cache.set("k2", 30);
    const prefix = buildPrefix(k, cache);
    // Unmeasured rows use the average of measured ones (30).
    assert.deepEqual([...prefix], [0, 30, 60, 90, 120, 150]);
    cache.set("k0", 10);
    const p2 = buildPrefix(k, cache);
    assert.deepEqual([...p2], [0, 10, 30, 60, 80, 100]);
    assert.equal(rowAt(p2, 0), 0);
    assert.equal(rowAt(p2, 9.9), 0);
    assert.equal(rowAt(p2, 10), 1);
    assert.equal(rowAt(p2, 59), 2);
    assert.equal(rowAt(p2, 1e9), 4, "offsets past the end clamp to the last row");
    assert.equal(rowAt(new Float64Array([0]), 5), 0);
});

test("the window covers the viewport plus overscan, with exact spacers", () => {
    const cache = createHeightCache(10);
    const prefix = buildPrefix(keys(1000), cache); // 10 px rows, 10,000 px
    const w = computeWindow({ prefix, scrollTop: 5000, viewport: 200, overscan: 100 });
    assert.deepEqual(w, { start: 490, end: 531, before: 4900, after: 4690 });
    assert.equal(w.before + (w.end - w.start) * 10 + w.after, 10000);

    const top = computeWindow({ prefix, scrollTop: 0, viewport: 200, overscan: 100 });
    assert.equal(top.start, 0);
    assert.equal(top.before, 0);

    const bottom = computeWindow({ prefix, scrollTop: 9800, viewport: 200, overscan: 100 });
    assert.equal(bottom.end, 1000);
    assert.equal(bottom.after, 0);

    assert.deepEqual(computeWindow({ prefix: new Float64Array([0]), scrollTop: 0, viewport: 100 }), { start: 0, end: 0, before: 0, after: 0 });
    const tiny = computeWindow({ prefix, scrollTop: 50, viewport: 0, overscan: 0 });
    assert.ok(tiny.end > tiny.start, "at least one row is always rendered");
});

test("100,000 rows window in well under a frame", () => {
    const cache = createHeightCache(48);
    const k = keys(100_000);
    for (let i = 0; i < k.length; i += 7) cache.set(k[i], 30 + (i % 90));
    const started = performance.now();
    const prefix = buildPrefix(k, cache);
    let rendered = 0;
    for (let y = 0; y < prefix[k.length]; y += prefix[k.length] / 500) {
        const w = computeWindow({ prefix, scrollTop: y, viewport: 900 });
        rendered = Math.max(rendered, w.end - w.start);
    }
    const elapsed = performance.now() - started;
    assert.ok(rendered < 120, `window rendered ${rendered} rows`);
    assert.ok(elapsed < 400, `one prefix build plus 500 windows took ${elapsed.toFixed(1)} ms`);
});
