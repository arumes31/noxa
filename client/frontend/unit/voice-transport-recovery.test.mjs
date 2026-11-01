import { test } from "node:test";
import assert from "node:assert/strict";
import { createVoiceTransportRecovery, voiceTransportFailed } from "../src/voice-transport-recovery.js";

function fixture(rebuild) {
    let clock = 0, enabled = true, sequence = 0;
    const jobs = new Map(), calls = [], aborted = [];
    const peer = () => ({ connectionState: "failed", iceConnectionState: "failed" });
    const state = { pc: peer(), activeTabID: "tab", serverGeneration: 1, sessionGeneration: 1, myChannelID: 16, myClientID: "self", muted: true, deafened: true };
    const recovery = createVoiceTransportRecovery({ state, abort: () => {aborted.push(state.pc);state.pc=null;}, allowed: () => enabled, now: () => clock,
        timers: { setTimeout(fn, delay) { const id = ++sequence; jobs.set(id, {fn, delay}); return id; }, clearTimeout(id) { jobs.delete(id); } },
        rebuild: async (pc, current) => { calls.push(pc); await rebuild?.({state, peer, current}); },
    });
    return {state, peer, calls, jobs, aborted, recovery, enable: value => {enabled = value;},
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
    f.recovery.schedule(f.state.pc);
    const secondJob=[...f.jobs.entries()].at(-1);f.jobs.delete(secondJob[0]);const second=secondJob[1].fn();
    finishes[0](); await first;
    assert.equal(f.recovery.schedule(f.state.pc), false);
    f.enable(false); finishes[1](); await second;
    assert.equal(f.calls.length, 2); assert.equal(f.jobs.size, 0);
});


test("hung replacement is cancelled after 30 seconds and can retry without changing mute", async () => {
    const f=fixture(()=>new Promise(()=>{}));
    f.recovery.schedule(f.state.pc);const attempt=f.next();
    assert.equal(f.delay(),30000);await f.next();await attempt;
    assert.equal(f.aborted.length,1);assert.equal(f.state.pc,null);
    assert.equal(f.calls.length,1);assert.equal(f.delay(),1000);
    assert.equal(f.state.muted,true);assert.equal(f.state.deafened,true);
    f.enable(false);await f.next();assert.equal(f.jobs.size,0);
});

test("a late startup timeout cannot abort a new voice scope or a connected replacement", async () => {
    for(const mode of ["scope","connected"]){
        const f=fixture(()=>new Promise(()=>{}));f.recovery.schedule(f.state.pc);const attempt=f.next();
        if(mode==="scope")f.state.sessionGeneration++;
        else f.state.pc={connectionState:"connected",iceConnectionState:"connected"};
        await f.next();await attempt;
        assert.equal(f.aborted.length,0,mode);assert.equal(f.jobs.size,0,mode);
    }
});
