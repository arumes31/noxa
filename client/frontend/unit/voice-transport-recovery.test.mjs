import { test } from "node:test";
import assert from "node:assert/strict";
import { createVoiceTransportRecovery, voiceTransportFailed } from "../src/voice-transport-recovery.js";

function fixture(rebuild) {
    let clock = 0, enabled = true, sequence = 0;
    const jobs = new Map(), calls = [];
    const peer = () => ({ connectionState: "failed", iceConnectionState: "failed" });
    const state = { pc: peer(), activeTabID: "tab", serverGeneration: 1, sessionGeneration: 1, myChannelID: 16, myClientID: "self", muted: true, deafened: true };
    const recovery = createVoiceTransportRecovery({ state, allowed: () => enabled, now: () => clock,
        timers: { setTimeout(fn, delay) { const id = ++sequence; jobs.set(id, {fn, delay}); return id; }, clearTimeout(id) { jobs.delete(id); } },
        rebuild: async (pc, current) => { calls.push(pc); await rebuild?.({state, peer, current}); },
    });
    return {state, peer, calls, jobs, recovery, enable: value => {enabled = value;},
        async next() { const [id, job] = jobs.entries().next().value; jobs.delete(id); clock += job.delay; await job.fn(); return job.delay; },
        delay: () => jobs.values().next().value?.delay,
    };
}

test("an exhausted transport coalesces replacement and preserves mute/deafen", async () => {
    const f = fixture(({state, peer}) => {state.pc = {...peer(), connectionState: "connected", iceConnectionState: "connected"};});
    assert.equal(f.recovery.schedule(f.state.pc), true);
    assert.equal(f.recovery.schedule(f.state.pc), false);
    assert.equal(await f.next(), 1000);
    assert.equal(f.calls.length, 1); assert.equal(f.jobs.size, 0);
    assert.equal(f.state.muted, true); assert.equal(f.state.deafened, true);
});

test("connected or deliberately closed peers cannot be rebuilt for missing keys", () => {
    const f = fixture();
    for (const connectionState of ["connected", "closed"]) {
        f.state.pc = {connectionState, iceConnectionState: connectionState};
        assert.equal(f.recovery.schedule(f.state.pc), false);
    }
    assert.equal(f.jobs.size, 0);
    assert.equal(voiceTransportFailed({connectionState: "connected", iceConnectionState: "failed"}), true);
    f.state.pc = {connectionState: "connected", iceConnectionState: "failed"};
    assert.equal(f.recovery.schedule(f.state.pc), true);
});

test("every voice scope dimension fences pending replacement", async () => {
    for (const field of ["activeTabID", "serverGeneration", "sessionGeneration", "myChannelID", "myClientID"]) {
        const f = fixture(); f.recovery.schedule(f.state.pc);
        f.state[field] = typeof f.state[field] === "string" ? "other" : f.state[field] + 1;
        await f.next(); assert.equal(f.calls.length, 0, field);
    }
    const f = fixture(); f.recovery.schedule(f.state.pc); f.state.pc = f.peer();
    await f.next(); assert.equal(f.calls.length, 0);
});

test("replacement peers retain a bounded cooldown instead of restarting the budget", async () => {
    const f = fixture(({state, peer}) => {state.pc = peer();});
    const original = f.state.pc; f.recovery.schedule(original);
    for (const delay of [1000, 30000, 60000, 120000, 120000]) {
        assert.equal(await f.next(), delay);
        assert.equal(f.jobs.size, 1);
    }
    f.recovery.cancel(original); assert.equal(f.jobs.size, 1);
    f.recovery.cancel(f.state.pc); assert.equal(f.jobs.size, 0);
});

test("failed startup retries with fresh scope checks and does not loop immediately", async () => {
    const f = fixture(({state}) => {state.pc = null; throw Error("capture unavailable");});
    f.recovery.schedule(f.state.pc); await f.next();
    assert.equal(f.delay(), 30000); assert.equal(f.calls.length, 1);
    f.enable(false); await f.next(); assert.equal(f.calls.length, 1); assert.equal(f.jobs.size, 0);
    assert.equal(f.recovery.schedule(null), false);
});

test("a recovered peer and intentional disconnect cancel pending recovery", async () => {
    for (const mode of ["connected", "disabled", "cancelled"]) {
        const f = fixture(); f.recovery.schedule(f.state.pc);
        if (mode === "connected") Object.assign(f.state.pc, {connectionState:"connected",iceConnectionState:"connected"});
        if (mode === "disabled") f.enable(false);
        if (mode === "cancelled") f.recovery.cancel(f.state.pc);
        if (f.jobs.size) await f.next();
        assert.equal(f.calls.length, 0, mode); assert.equal(f.jobs.size, 0);
    }
});

test("stale async completion cannot clear another scope's in-flight operation", async () => {
    const finishes = [];
    const f = fixture(() => new Promise(resolve => finishes.push(resolve)));
    f.recovery.schedule(f.state.pc); const first = f.next();
    f.state.sessionGeneration++; f.state.pc = f.peer();
    f.recovery.schedule(f.state.pc); const second = f.next();
    finishes[0](); await first;
    assert.equal(f.recovery.schedule(f.state.pc), false);
    f.enable(false); finishes[1](); await second;
    assert.equal(f.calls.length, 2); assert.equal(f.jobs.size, 0);
});
