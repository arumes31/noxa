import test from "node:test";
import assert from "node:assert/strict";
import { createDisplayCaptureLeases } from "../src/display-capture-lease.js";

const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
class Track extends EventTarget {
    kind = "video"; readyState = "live"; enabled = true;
    stop() { this.readyState = "ended"; }
    end() { this.stop(); this.dispatchEvent(new Event("ended")); }
}
function harness(overrides = {}) {
    const page = new EventTarget(), timers = new Set(), acquired = [], refreshed = [], released = [], warnings = [];
    const app = {
        async AcquireDisplayCaptureLease() { const id = `lease-${acquired.length}`; acquired.push(id); return id; },
        async RefreshDisplayCaptureLease(id) { refreshed.push(id); },
        async ReleaseDisplayCaptureLease(id) { released.push(id); }, ...overrides,
    };
    const manager = createDisplayCaptureLeases({ native: () => app, page, every: fn => { timers.add(fn); return fn; },
        cancel: fn => timers.delete(fn), warn: () => warnings.push(true) });
    return { manager, app, page, timers, acquired, refreshed, released, warnings,
        async tick() { for (const fn of [...timers]) fn(); await settle(); } };
}

test("idle and audio create no lease; exact track deduplicates and disabled upload retains protection", async () => {
    const h = harness(), track = new Track();
    h.manager.retain({ kind: "audio" });
    assert.equal(h.timers.size, 0);
    h.manager.retain(track); h.manager.retain(track); await settle();
    track.enabled = false; await h.tick();
    assert.deepEqual(h.acquired, ["lease-0"]); assert.deepEqual(h.refreshed, ["lease-0"]);
    h.manager.release(track); h.manager.release(track); await settle();
    assert.deepEqual(h.released, ["lease-0"]); assert.equal(h.timers.size, 0);
});

test("track-ended cleanup and explicit stop cleanup isolate concurrent main/private capture", async () => {
    const h = harness(), main = new Track(), call = new Track();
    h.manager.retain(main); h.manager.retain(call); await settle();
    main.end(); await settle();
    assert.deepEqual(h.released, ["lease-0"]);
    await h.tick(); assert.deepEqual(h.refreshed, ["lease-1"]);
    h.manager.release(call); call.stop(); await settle();
    assert.deepEqual(h.released, ["lease-0", "lease-1"]); assert.equal(h.timers.size, 0);
});

for (const cleanup of ["release", "ended", "pagehide"]) {
    test(`pending acquire after ${cleanup} releases its native result without refresh`, async () => {
        const pending = deferred(), h = harness({ AcquireDisplayCaptureLease: () => pending.promise }), track = new Track();
        h.manager.retain(track);
        if (cleanup === "release") h.manager.release(track);
        else if (cleanup === "ended") track.end();
        else h.page.dispatchEvent(new Event("pagehide"));
        pending.resolve("late"); await settle(); await h.tick();
        assert.deepEqual(h.released, ["late"]); assert.equal(h.timers.size, 0); assert.deepEqual(h.refreshed, []);
    });
}

test("late acquisition cannot release a newer owner of the same track", async () => {
    const pending = deferred(), h = harness(), track = new Track();
    h.app.AcquireDisplayCaptureLease = () => pending.promise;
    h.manager.retain(track); h.manager.release(track);
    h.app.AcquireDisplayCaptureLease = async () => "new";
    h.manager.retain(track); await settle(); pending.resolve("old"); await settle(); await h.tick();
    assert.deepEqual(h.released, ["old"]); assert.deepEqual(h.refreshed, ["new"]);
    h.manager.releaseAll(); await settle(); assert.deepEqual(h.released, ["old", "new"]);
});

test("failed acquisition is nonfatal, warns once per capture and retries only on timer ticks", async () => {
    let calls = 0;
    const h = harness({ AcquireDisplayCaptureLease: async () => { calls++; throw new Error("unavailable"); } }), track = new Track();
    h.manager.retain(track); await settle(); assert.equal(calls, 1); assert.equal(track.readyState, "live");
    await h.tick(); await h.tick(); assert.equal(calls, 3); assert.equal(h.warnings.length, 1);
    h.manager.release(track); await h.tick(); assert.equal(calls, 3);
});

test("missing native bindings leave capture live and bounded diagnostics", async () => {
    const h = harness({ AcquireDisplayCaptureLease: undefined }), track = new Track();
    h.manager.retain(track); await settle(); await h.tick();
    assert.equal(track.readyState, "live"); assert.equal(h.warnings.length, 1); h.manager.releaseAll();
});

test("expired refresh retires stale ID and reacquires once for still-live capture", async () => {
    const h = harness({ RefreshDisplayCaptureLease: async () => { throw new Error("expired"); } }), track = new Track();
    h.manager.retain(track); await settle(); await h.tick();
    assert.deepEqual(h.acquired, ["lease-0", "lease-1"]); assert.deepEqual(h.released, ["lease-0"]);
    assert.equal(h.warnings.length, 1); h.manager.releaseAll(); await settle();
    assert.deepEqual(h.released, ["lease-0", "lease-1"]);
});

for (const reject of [false, true]) {
    test(`deferred refresh ${reject ? "failure" : "success"} after cleanup cannot reacquire`, async () => {
        const pending = deferred(), h = harness({ RefreshDisplayCaptureLease: () => pending.promise }), track = new Track();
        h.manager.retain(track); await settle(); await h.tick(); await h.tick();
        h.page.dispatchEvent(new Event("pagehide"));
        if (reject) pending.reject(new Error("expired")); else pending.resolve();
        await settle(); await h.tick();
        assert.deepEqual(h.acquired, ["lease-0"]); assert.deepEqual(h.released, ["lease-0"]); assert.equal(h.timers.size, 0);
    });
}

test("timer detects unannounced external stop and does not renew an ended track", async () => {
    const h = harness(), track = new Track(); h.manager.retain(track); await settle();
    track.stop(); await h.tick(); assert.deepEqual(h.released, ["lease-0"]); assert.deepEqual(h.refreshed, []);
});

test("hung acquisition is coalesced and pagehide cleans all concurrent owners", async () => {
    const pending = deferred(), h = harness(); let calls = 0;
    h.app.AcquireDisplayCaptureLease = () => { calls++; return pending.promise; };
    h.manager.retain(new Track()); await h.tick(); await h.tick(); assert.equal(calls, 1);
    h.page.dispatchEvent(new Event("pagehide")); pending.resolve("late"); await settle();
    assert.deepEqual(h.released, ["late"]); assert.equal(h.timers.size, 0);
});

test("picker settling after pagehide cannot start a lease; pageshow permits only new ownership", async () => {
    const h = harness(), late = new Track();
    h.page.dispatchEvent(new Event("pagehide")); h.manager.retain(late); await settle(); await h.tick();
    assert.deepEqual(h.acquired, []); assert.equal(h.timers.size, 0);
    h.page.dispatchEvent(new Event("pageshow")); await h.tick(); assert.deepEqual(h.acquired, []);
    h.manager.retain(new Track()); await settle(); assert.deepEqual(h.acquired, ["lease-0"]);
    h.manager.releaseAll(); await settle();
});
