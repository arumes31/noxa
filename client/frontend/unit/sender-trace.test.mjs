import test from "node:test";
import assert from "node:assert/strict";
import { createSenderTrace, numericSenderTrace } from "../src/sender-trace.js";

const token = "a".repeat(64), scope = "scope-0123456789-abcd";
const record = () => ({ schema: "noxa.sender_trace.numeric.v1", complete: true, reason: "duration", elapsed_ms: 60000,
    timer_cooperative: true, marker_counts: { encode: 1, encoded: 1, enqueue: 1, process: 1 }, dropped_unknown_events: 0,
    events: [{ kind: "encode", trace_us: 10, rtp_timestamp: 123 }, { kind: "process", trace_us: 20, sequence_number: 12 }] });
function fixture(options = {}) {
    let now = 0, alive = true, id = 0;
    const timers = new Map(), listeners = new Set(), sent = [];
    const bridge = { addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn),
        postMessage: value => sent.push(JSON.parse(value.slice("__noxa_sender_trace_v1:".length))) };
    const control = createSenderTrace({ supported: async () => true, arm: async () => token, bridge,
        captureScope: () => ({ tabID: "tab", current: () => alive }), opaqueScope: () => scope, now: () => now,
        schedule: (fn, ms) => { timers.set(++id, { fn, at: now + ms }); return id; }, cancel: key => timers.delete(key), ...options });
    const emit = (state, extra = {}) => { for (const fn of [...listeners]) fn({ data: JSON.stringify({ type: "noxa.sender_trace.v1", token, scope, state, reason: "duration", ...extra }) }); };
    return { control, sent, listeners, timers, emit, stale: () => { alive = false; },
        advance(ms) { const end = now + ms; while (true) { const next = [...timers].sort((a, b) => a[1].at - b[1].at)[0];
            if (!next || next[1].at > end) break; now = next[1].at; timers.delete(next[0]); next[1].fn(); } now = end; } };
}

test("off by default; explicit start and exact token/scope terminal export only", async () => {
    const f = fixture(); assert.equal(f.sent.length, 0); assert.equal(f.listeners.size, 0); assert.equal(f.timers.size, 0);
    assert.equal(await f.control.start(), true); assert.deepEqual(f.sent.map(x => x.command), ["start"]);
    f.emit("recording", { token: "wrong" }); assert.equal(f.control.status().state, "starting");
    f.emit("recording", { scope: "wrong" }); assert.equal(f.control.status().state, "starting");
    f.emit("recording"); assert.equal(f.control.status().state, "recording");
    f.emit("completed", { record: record() }); assert.equal(f.control.status().cached, false, "unsolicited record is not retained");
    assert.equal(f.listeners.size, 0); assert.equal(f.timers.size, 0);
    const pending = f.control.export(); assert.equal(f.sent.at(-1).command, "export");
    f.emit("completed", { record: { ...record(), raw: "private source" } });
    const result = await pending; assert.equal(result.complete, true); assert.equal(result.raw, undefined);
    result.events.length = 0; assert.equal((await f.control.export()).events.length, 2);
    f.control.invalidate(); assert.equal((await f.control.export()).events.length, 2, "explicitly copied numeric cache survives native replacement");
    assert.equal(f.listeners.size, 0); assert.equal(f.timers.size, 0);
});

test("late arm after dispose or timeout never starts native tracing", async () => {
    for (const interrupted of ["dispose", "timeout"]) {
        let resolve;
        const f = fixture({ arm: () => new Promise(done => { resolve = done; }) });
        const pending = f.control.start(); await Promise.resolve();
        if (interrupted === "dispose") f.control.dispose(); else f.advance(10000);
        resolve(token); assert.equal(await pending, false);
        assert.deepEqual(f.sent.map(x => x.command), ["stop"]);
        assert.equal(f.control.status().state, "failed"); assert.equal(f.listeners.size, 0); assert.equal(f.timers.size, 0);
    }
});

test("scope change stops even while native stopping; exported result stays incomplete", async () => {
    for (const nativeStopping of [false, true]) {
        const f = fixture(); await f.control.start(); f.emit("recording");
        if (nativeStopping) f.emit("stopping");
        f.stale(); f.advance(1000); assert.equal(f.sent.at(-1).command, "stop");
        f.emit("completed"); const pending = f.control.export(); f.emit("completed", { record: record() });
        assert.equal((await pending).complete, false); assert.equal((await f.control.export()).reason, "scope_changed");
        assert.equal(f.listeners.size, 0); assert.equal(f.timers.size, 0);
    }
});

test("failure and 90-second cooperative timeout never claim completed, never auto retry", async () => {
    for (const nativeFailure of [false, true]) {
        const f = fixture(); await f.control.start(); f.emit("recording");
        if (nativeFailure) f.emit("failed", { reason: "event_failed" }); else f.advance(90000);
        assert.equal(f.control.status().state, "failed");
        assert.equal(f.sent.filter(x => x.command === "start").length, 1);
        assert.equal(f.sent.some(x => x.command === "export"), false);
        assert.equal(f.listeners.size, 0); assert.equal(f.timers.size, 0);
        const pending = f.control.export(); f.emit("failed", { record: record() });
        assert.equal((await pending).complete, false);
    }
});

test("stop/export timeouts release listeners; stale terminal callbacks cannot finish a newer attempt", async () => {
    const f = fixture(); await f.control.start(); f.control.stop(); f.advance(10000);
    assert.equal(f.control.status().state, "failed"); assert.equal(f.listeners.size, 0);
    const pending = f.control.export(); f.advance(10000); assert.equal(await pending, null); assert.equal(f.listeners.size, 0);
    f.control.invalidate(); f.emit("completed", { record: record() }); assert.equal(f.control.status().cached, false);
});

test("unsupported or ineligible start does not arm; invalidated archive cannot fetch old native cache", async () => {
    let armed = 0;
    const f = fixture({ supported: async () => false, arm: async () => { armed++; return token; } });
    assert.equal(await f.control.start(), false); assert.equal(armed, 0); assert.equal(f.sent.length, 0);
    const g = fixture(); g.stale(); assert.equal(await g.control.start(), false); assert.equal(g.timers.size, 0);
    const h = fixture(); await h.control.start(); h.emit("completed"); h.control.dispose(); h.control.invalidate();
    assert.equal(await h.control.export(), null); assert.equal(h.sent.some(x => x.command === "export"), false);
});

test("numeric projection rejects unsupported markers, invalid counters and event identifiers", () => {
    for (const mutate of [x => { x.events[0].kind = "payload"; }, x => { x.events[0].rtp_timestamp = 2 ** 32; },
        x => { x.events[1].sequence_number = 65536; }, x => { x.marker_counts.encode = NaN; },
        x => { x.timer_cooperative = false; }, x => { x.events = Array(32769).fill(x.events[0]); }]) {
        const x = record(); mutate(x); assert.equal(numericSenderTrace(x), null);
    }
    const x = record(); x.events[0].source_title = "private";
    assert.equal(numericSenderTrace(x).events[0].source_title, undefined);
});


test("scope-lost flag is stop-only; normal stop omits it; empty successful reason stays empty", async () => {
    const f = fixture(); await f.control.start();
    assert.equal(Object.hasOwn(f.sent[0], "scope_lost"), false);
    f.control.dispose(); assert.equal(f.sent.at(-1).scope_lost, true);
    const g = fixture(); await g.control.start(); g.control.stop(); assert.equal(Object.hasOwn(g.sent.at(-1), "scope_lost"), false);
    const h = fixture(); await h.control.start(); h.emit("completed", { reason: "" }); assert.equal(h.control.status().reason, "");
    const pending = h.control.export(); h.emit("completed", { record: { ...record(), reason: "" } });
    assert.equal((await pending).reason, "");
});

test("scope lost while awaiting support or arm finishes immediately and cancels exact returned token", async () => {
    for (const step of ["supported", "arm"]) {
        let resolve;
        const f = fixture({ [step]: () => new Promise(done => { resolve = done; }) });
        const pending = f.control.start(); await Promise.resolve(); f.stale(); resolve(step === "arm" ? token : true);
        assert.equal(await pending, false); assert.equal(f.control.status().state, "failed");
        assert.equal(f.control.status().reason, "scope_changed"); assert.equal(f.timers.size, 0); assert.equal(f.listeners.size, 0);
        if (step === "arm") assert.deepEqual(f.sent, [{ command: "stop", token, scope, scope_lost: true }]);
        else assert.equal(f.sent.length, 0);
    }
});

test("failed pre-start numeric record with no events remains explicitly copyable", async () => {
    const f = fixture(); await f.control.start(); f.emit("failed", { reason: "start_failed" });
    const pending = f.control.export(); f.emit("failed", { record: { ...record(), complete: false, reason: "start_failed", elapsed_ms: 0,
        marker_counts: { encode: 0, encoded: 0, enqueue: 0, process: 0 }, events: [] } });
    assert.deepEqual((await pending).events, []); assert.equal((await f.control.export()).complete, false);
});


test("scope loss during manual stop overrides manual reason and reaches native invalidation", async () => {
    const f = fixture(); await f.control.start(); f.control.stop(); f.control.dispose();
    assert.equal(f.sent.at(-1).scope_lost, true); f.emit("completed");
    const pending = f.control.export(); f.emit("completed", { record: record() });
    assert.equal((await pending).reason, "scope_changed");
});
