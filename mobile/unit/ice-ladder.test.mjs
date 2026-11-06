import assert from "node:assert/strict";
import { test } from "node:test";
import { createIceRestartLadder, iceTransportState } from "../src/core/ice-ladder.js";

function fakeTimers() {
    const pending = new Map();
    let id = 0;
    return {
        setTimeout(callback, delay) { pending.set(++id, { callback, delay }); return id; },
        clearTimeout(timer) { pending.delete(timer); },
        delays: () => [...pending.values()].map(entry => entry.delay),
        async runNext() {
            const [timer, entry] = pending.entries().next().value || [];
            if (!entry) throw new Error("no pending timer");
            pending.delete(timer);
            await entry.callback();
        },
        size: () => pending.size,
    };
}

function ladderFixture(initial = "failed") {
    const timers = fakeTimers();
    let current = initial, restarts = 0, exhausted = 0;
    const ladder = createIceRestartLadder({
        state: () => current, timers,
        restart: async () => { restarts++; },
        onExhausted: () => { exhausted++; },
    });
    return { ladder, timers, set: value => { current = value; }, restarts: () => restarts, exhausted: () => exhausted };
}

test("a failed transport restarts at once, then backs off and reports exhaustion once", async () => {
    const f = ladderFixture("failed");
    f.ladder.update();
    const seen = [];
    for (let i = 0; i < 5; i++) {
        seen.push(f.timers.delays()[0]);
        await f.timers.runNext();
    }
    assert.deepEqual(seen, [0, 1000, 2000, 5000, 15000]);
    assert.equal(f.restarts(), 5);
    assert.equal(f.timers.size(), 0);
    assert.equal(f.exhausted(), 1);
    f.ladder.update();
    assert.equal(f.exhausted(), 1, "exhaustion is reported once per outage");
});

test("disconnected waits briefly and a recovery cancels the restart", async () => {
    const f = ladderFixture("disconnected");
    f.ladder.update();
    assert.deepEqual(f.timers.delays(), [500]);
    f.set("connected");
    f.ladder.update();
    assert.equal(f.timers.size(), 0);
    f.set("failed");
    f.ladder.update();
    assert.deepEqual(f.timers.delays(), [0], "a new outage starts from the first step");
});

test("a network change restarts a waiting ladder from the first step", async () => {
    const f = ladderFixture("failed");
    f.ladder.update();
    await f.timers.runNext();
    await f.timers.runNext();
    assert.deepEqual(f.timers.delays(), [2000]);
    f.ladder.nudge();
    assert.deepEqual(f.timers.delays(), [0]);
});

test("stop cancels pending restarts and ignores later changes", async () => {
    const f = ladderFixture("failed");
    f.ladder.update();
    f.ladder.stop();
    assert.equal(f.timers.size(), 0);
    f.ladder.update();
    f.ladder.nudge();
    assert.equal(f.timers.size(), 0);
    assert.equal(f.restarts(), 0);
});

test("transport state prefers closed, then failed, then disconnected", () => {
    assert.equal(iceTransportState(null), "");
    assert.equal(iceTransportState({ connectionState: "closed", iceConnectionState: "failed" }), "");
    assert.equal(iceTransportState({ connectionState: "connected", iceConnectionState: "failed" }), "failed");
    assert.equal(iceTransportState({ connectionState: "disconnected", iceConnectionState: "connected" }), "disconnected");
    assert.equal(iceTransportState({ connectionState: "connected", iceConnectionState: "completed" }), "connected");
    assert.equal(iceTransportState({ connectionState: "new", iceConnectionState: "checking" }), "");
});

test("a failure during the disconnected grace step restarts at once", async () => {
    const f = ladderFixture("disconnected");
    f.ladder.update();
    assert.deepEqual(f.timers.delays(), [500]);
    f.set("failed");
    f.ladder.update();
    assert.deepEqual(f.timers.delays(), [0], "the grace timer is replaced, not doubled");
    await f.timers.runNext();
    assert.equal(f.restarts(), 1);
    assert.deepEqual(f.timers.delays(), [1000]);
});

test("state changes during an in-flight restart do not start another timer", async () => {
    const timers = fakeTimers();
    let release;
    let restarts = 0;
    const ladder = createIceRestartLadder({
        state: () => "failed", timers,
        restart: () => { restarts++; return new Promise(resolve => { release = resolve; }); },
    });
    ladder.update();
    const run = timers.runNext();
    ladder.update();
    ladder.update();
    assert.equal(timers.size(), 0, "nothing is scheduled while the restart runs");
    release();
    await run;
    assert.equal(restarts, 1);
    assert.deepEqual(timers.delays(), [1000]);
});

test("a network change restarts an exhausted ladder from its first step", async () => {
    const f = ladderFixture("failed");
    f.ladder.update();
    for (let i = 0; i < 5; i++) await f.timers.runNext();
    assert.equal(f.exhausted(), 1);
    assert.equal(f.timers.size(), 0);
    f.ladder.nudge();
    assert.deepEqual(f.timers.delays(), [0]);
    f.ladder.stop();
    f.ladder.nudge();
    assert.equal(f.timers.size(), 0, "a stopped ladder stays stopped");
});
