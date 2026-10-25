import assert from "node:assert/strict";
import { test } from "node:test";
import { createStatusExpiryScheduler, STATUS_EXPIRY_OPTIONS, statusExpiryAt } from "../src/status-expiry.js";

const MINUTE = 60_000;

function fakeClock(start = 1_700_000_000_000) {
    let now = start;
    const timers = new Map();
    let next = 1;
    return {
        now: () => now,
        setTimer: (fn, delay) => { const id = next++; timers.set(id, { fn, at: now + delay }); return id; },
        clearTimer: id => { timers.delete(id); },
        pending: () => timers.size,
        skip(ms) { now += ms; }, // wall clock jumps (sleep) without timers firing
        advance(ms) {
            now += ms;
            for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn(); }
        },
    };
}

test("expiry presets resolve relative to the given clock and never for 'none'", () => {
    const now = new Date(2026, 9, 25, 14, 30).getTime();
    assert.deepEqual(STATUS_EXPIRY_OPTIONS, ["none", "30m", "1h", "4h", "today", "custom"]);
    assert.equal(statusExpiryAt("none", now), 0);
    assert.equal(statusExpiryAt("30m", now), now + 30 * MINUTE);
    assert.equal(statusExpiryAt("1h", now), now + 60 * MINUTE);
    assert.equal(statusExpiryAt("4h", now), now + 240 * MINUTE);
    assert.equal(statusExpiryAt("today", now), new Date(2026, 9, 25, 23, 59, 59, 999).getTime());
    assert.equal(statusExpiryAt("custom", now, "45"), now + 45 * MINUTE);
    assert.equal(statusExpiryAt("custom", now, 0), 0);
    assert.equal(statusExpiryAt("custom", now, "abc"), 0);
    assert.equal(statusExpiryAt("custom", now, 99_999), now + 7 * 24 * 60 * MINUTE, "custom durations are capped at a week");
    assert.equal(statusExpiryAt("unknown", now), 0);
});

test("the scheduler fires once when the status expires and persists every transition", () => {
    const clock = fakeClock();
    const saved = [], expired = [];
    const scheduler = createStatusExpiryScheduler({ ...clock, save: record => saved.push(record), onExpire: record => expired.push(record) });
    assert.equal(scheduler.current(), null);
    const record = scheduler.set({ status: "busy", message: "In a meeting", expiresAt: clock.now() + 30 * MINUTE });
    assert.equal(record.status, "busy");
    assert.equal(clock.pending(), 1);
    clock.advance(29 * MINUTE);
    assert.equal(expired.length, 0);
    assert.equal(scheduler.check(), false, "not yet due");
    clock.advance(MINUTE);
    assert.equal(expired.length, 1);
    assert.equal(expired[0].message, "In a meeting");
    assert.equal(scheduler.current(), null);
    assert.equal(clock.pending(), 0);
    assert.equal(scheduler.check(), false, "nothing left to expire");
    assert.deepEqual(saved.at(-1), null);
    assert.equal(saved[0].status, "busy");
});

test("startup and resume checks clear a status that expired while the client was away", () => {
    const clock = fakeClock();
    const expired = [];
    const stored = { status: "away", message: "Lunch", expiresAt: clock.now() - MINUTE };
    const scheduler = createStatusExpiryScheduler({ ...clock, load: () => stored, onExpire: record => expired.push(record) });
    assert.equal(scheduler.current().status, "away", "a stale record survives load until the startup check");
    assert.equal(expired.length, 0, "loading alone never acts");
    assert.equal(scheduler.check(), true, "the startup check clears it");
    assert.equal(expired.length, 1);
    assert.equal(scheduler.current(), null);

    const live = createStatusExpiryScheduler({ ...clock, load: () => ({ status: "away", message: "Lunch", expiresAt: clock.now() + 10 * MINUTE }), onExpire: record => expired.push(record) });
    assert.equal(live.current().message, "Lunch");
    // Sleep: the wall clock jumps past the expiry without the timer firing.
    clock.skip(11 * MINUTE);
    assert.equal(live.check(), true, "resume check expires it");
    assert.equal(expired.length, 2);
    assert.equal(live.check(), false);
});

test("replacing or clearing the status cancels the previous expiry", () => {
    const clock = fakeClock();
    const expired = [];
    const scheduler = createStatusExpiryScheduler({ ...clock, onExpire: record => expired.push(record) });
    scheduler.set({ status: "busy", expiresAt: clock.now() + 5 * MINUTE });
    scheduler.set({ status: "away", expiresAt: clock.now() + 60 * MINUTE });
    assert.equal(clock.pending(), 1);
    clock.advance(10 * MINUTE);
    assert.equal(expired.length, 0, "the replaced expiry no longer fires");
    scheduler.clear();
    assert.equal(clock.pending(), 0);
    clock.advance(60 * MINUTE);
    assert.equal(expired.length, 0);
    assert.equal(scheduler.set({ status: "busy", expiresAt: 0 }), null, "no expiry means no record");
    assert.equal(scheduler.set({ status: "busy", expiresAt: clock.now() - 1 }), null, "a past expiry is ignored");
    assert.equal(createStatusExpiryScheduler({ ...clock, load: () => "garbage" }).current(), null);
});

test("an expiry that cannot be delivered yet keeps its record and retries", async () => {
    const clock = fakeClock();
    const saved = [];
    let online = false, cleared = 0;
    const stored = { status: "away", message: "Back soon", expiresAt: clock.now() - MINUTE, tabID: "t1" };
    const scheduler = createStatusExpiryScheduler({
        ...clock, load: () => stored, save: record => saved.push(record),
        onExpire: () => { if (!online) return false; cleared++; return true; },
    });
    assert.equal(scheduler.check(), true, "the startup check runs the callback");
    assert.equal(cleared, 0);
    assert.equal(scheduler.current()?.tabID, "t1", "the record survives a failed clear");
    assert.deepEqual(saved, [], "nothing is persisted while the clear is pending");
    assert.equal(clock.pending(), 1, "a retry timer is armed");
    clock.advance(15_000);
    assert.equal(cleared, 0, "still offline: retried, record kept");
    assert.equal(scheduler.current()?.tabID, "t1");
    online = true;
    clock.advance(15_000);
    assert.equal(cleared, 1, "the retry clears the status once a connection is up");
    assert.equal(scheduler.current(), null);
    assert.deepEqual(saved, [null], "the record is consumed exactly once");
    assert.equal(clock.pending(), 0);
});

test("a promise-returning expiry callback settles the record asynchronously", async () => {
    const clock = fakeClock();
    const scheduler = createStatusExpiryScheduler({ ...clock, load: () => ({ status: "busy", expiresAt: clock.now() - 1 }), onExpire: () => Promise.resolve(false) });
    scheduler.check();
    await Promise.resolve();
    assert.ok(scheduler.current(), "rejected async clear keeps the record");
    const done = createStatusExpiryScheduler({ ...clock, load: () => ({ status: "busy", expiresAt: clock.now() - 1 }), onExpire: () => Promise.resolve(true) });
    done.check();
    await Promise.resolve();
    assert.equal(done.current(), null, "successful async clear consumes the record");
});
