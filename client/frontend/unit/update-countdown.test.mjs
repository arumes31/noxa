import test from "node:test";
import assert from "node:assert/strict";
import { createUpdateCountdown } from "../src/update-countdown.js";
import { measuredUpdateProgress } from "../src/update-core.js";

function clock() {
    let time = 0, id = 0;
    const jobs = new Map();
    return { now: () => time, schedule(fn, delay) { jobs.set(++id, { fn, at: time + delay }); return id; }, unschedule(id) { jobs.delete(id); },
        advance(ms) { const end = time + ms; for (;;) { const next = [...jobs].sort((a,b) => a[1].at-b[1].at)[0]; if (!next || next[1].at > end) break; time = next[1].at; jobs.delete(next[0]); next[1].fn(); } time = end; }, get pending() { return jobs.size; } };
}
const flush = async () => { await Promise.resolve(); await Promise.resolve(); };
test("verified countdown restarts exactly once at 3000ms, never 2999", async () => {
    const c = clock(), ticks = []; let calls = 0;
    const countdown = createUpdateCountdown({ ...c, current: () => true, tick: n => ticks.push(n), restart: async () => { calls++; return true; } });
    c.advance(2999); await flush(); assert.equal(calls, 0); assert.deepEqual(ticks, [3,2,1]);
    c.advance(1); assert.equal(await countdown.done, true); assert.equal(calls, 1);
    await countdown.startNow(); c.advance(10000); assert.equal(calls, 1); assert.equal(c.pending, 0);
});
test("cancel joins the owned timer without applying a verified update", async () => {
    const c = clock(); let calls = 0;
    const d = createUpdateCountdown({ ...c, current: () => true, tick() {}, restart() { calls++; } });
    c.advance(2999); d.cancel(); c.advance(10); assert.equal(await d.done, false); assert.equal(calls, 0); assert.equal(c.pending, 0);
});
test("replaced/closed operation cannot auto restart", async () => {
    const c = clock(); let current = true, calls = 0;
    const d = createUpdateCountdown({ ...c, current: () => current, tick() {}, restart() { calls++; } });
    current = false; c.advance(3000); assert.equal(await d.done, false); await d.startNow(); assert.equal(calls, 0);
});
test("manual restart races safely with deadline and retains restart promise", async () => {
    const c = clock(); let calls = 0, finish;
    const d = createUpdateCountdown({ ...c, current: () => true, tick() {}, restart: () => { calls++; return new Promise(r => { finish = r; }); } });
    const a = d.startNow(); c.advance(3000); const b = d.startNow(); d.cancel(); await flush();
    assert.equal(calls, 1); assert.equal(a, b); finish(true); assert.equal(await d.done, true);
});
test("restart failure settles without a repeating auto-apply loop", async () => {
    const c = clock(); let calls = 0;
    const d = createUpdateCountdown({ ...c, current: () => true, tick() {}, restart: () => { calls++; throw Error("failed"); } });
    c.advance(3000); assert.equal(await d.done, false); c.advance(30000); assert.equal(calls, 1);
});
test("measured bytes determine real core progress and never invent missing bytes", () => {
    assert.deepEqual(measuredUpdateProgress(1, { downloaded_bytes: 500, total_bytes: 1000 }), { downloaded: 500, total: 1000, ratio: .5, blocks: 40 });
    assert.deepEqual(measuredUpdateProgress(50), { downloaded: null, total: null, ratio: .5, blocks: 40 });
    assert.deepEqual(measuredUpdateProgress(-1, { downloaded_bytes: 100, total_bytes: -1 }), { downloaded: 100, total: null, ratio: null, blocks: null });
    assert.equal(measuredUpdateProgress(100, { downloaded_bytes: -2, total_bytes: Infinity }).downloaded, null);
    assert.equal(measuredUpdateProgress(500).ratio, 1);
});

test("automatic install countdown waits five seconds and rechecks permission", async () => {
    const c = clock(), ticks = []; let calls = 0;
    const d = createUpdateCountdown({ ...c, delayMs: 5000, current: () => true, tick: n => ticks.push(n), restart: () => { calls++; return true; } });
    c.advance(4999); await flush(); assert.equal(calls, 0); assert.deepEqual(ticks, [5,4,3,2,1]);
    c.advance(1); assert.equal(await d.done, true); assert.equal(calls, 1);
});
