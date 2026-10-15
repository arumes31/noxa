import test from "node:test";
import assert from "node:assert/strict";
import { createUpdateCountdown } from "../src/update-countdown.js";
import { createUpdateCore, measuredUpdateProgress } from "../src/update-core.js";

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


test("core redraws only for observed changes, skips hidden pixels and disposes all observers", () => {
    const saved = Object.fromEntries(["window", "document", "ResizeObserver", "MutationObserver", "getComputedStyle"].map(k => [k, globalThis[k]]));
    const events = new Map(), preferences = new Map(), observers = [], arcs = [];
    const preference = { matches: true, addEventListener: (n, fn) => preferences.set(n, fn), removeEventListener: n => preferences.delete(n) };
    globalThis.window = { devicePixelRatio: 2, matchMedia: () => preference };
    globalThis.document = { hidden: false, documentElement: { dataset: {} }, addEventListener: (n, fn) => events.set(n, fn), removeEventListener: n => events.delete(n) };
    globalThis.getComputedStyle = () => ({ getPropertyValue: () => "#ffffff" });
    globalThis.ResizeObserver = globalThis.MutationObserver = class { constructor(fn) { this.fn = fn; this.closed = false; observers.push(this); } observe() {} disconnect() { this.closed = true; } };
    const ctx = { scale() {}, beginPath() {}, arc(...args) { arcs.push(args); }, fill() {}, save() {}, clip() {}, fillRect() {}, restore() {} };
    const canvas = { isConnected: true, clientWidth: 500, clientHeight: 200, getClientRects: () => [1], getContext: () => ctx };
    try {
        const core = createUpdateCore(canvas);
        core.update(.25); assert.equal(arcs.length, 80); assert.equal(canvas.width, 750);
        const first = arcs[0];
        core.update(.5); assert.deepEqual(arcs[80], first, "reduced motion keeps the core stationary");
        document.hidden = true; core.update(.75); assert.equal(arcs.length, 160);
        document.hidden = false; events.get("visibilitychange")(); assert.equal(arcs.length, 240);
        preference.matches = false; preferences.get("change")(); assert.notDeepEqual(arcs[240], first);
        document.documentElement.dataset.reduceMotion = "1"; observers[1].fn(); assert.deepEqual(arcs[320], first);
        core.dispose(); assert.equal(events.size, 0); assert.equal(preferences.size, 0); assert.ok(observers.every(o => o.closed));
        core.update(1); observers[0].fn(); assert.equal(arcs.length, 400);
    } finally { for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; } }
});
