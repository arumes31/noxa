// Review 2b fix2 repros adopted: the removal invariant under debounce
// starvation, same-instant ties, wall-clock steps and a throwing rekey.
// For every removal interval the harness checks: no epoch created while the
// device was out reaches it, an epoch it never held is active by
// tD + REMOVAL_BOUND_MS, none of its epochs comes back, and an epoch is
// active at the end.
import { test } from "node:test";
import assert from "node:assert/strict";
import { world, check } from "./media-keys-removal-harness.mjs";

const ok = (W, name) => assert.deepEqual(check(W), [], name);

test("a stream of roster changes never postpones a removal rekey (debounce starvation)", async () => {
    let W = world(); await W.start(); W.remove(0);
    for (let i = 0; i < 100; i++) { await W.advance(150); W.join(500 + i); }
    await W.advance(10_000); ok(W, "joins every 150 ms");
    W = world(); await W.start(); W.remove(0);
    for (let i = 0; i < 100; i++) { await W.advance(150); if (i % 2) W.remove(19); else W.join(19); }
    await W.advance(10_000); ok(W, "a member toggled every 150 ms");
});

test("a removal in the same instant as an epoch's creation is never dropped", async () => {
    // Removal while a join epoch awaits its key installs: it preempts.
    let W = world(); await W.start(); W.stall(() => true);
    let armed = true; W.hooks.setKey = async () => { if (armed) { armed = false; W.remove(0); } };
    W.join(900); await W.advance(400_000); ok(W, "tie with a non-removal epoch");
    // Removal in the same instant its control of the removal epoch is sent:
    // that epoch was created before the removal; the next one excludes it.
    W = world(); await W.start();
    armed = false; W.hooks.send = s => { if (armed && s.device === "d1") { armed = false; queueMicrotask(() => W.remove(1)); } };
    W.stall(d => d !== "d1"); W.remove(0); armed = true;
    await W.advance(400_000); ok(W, "tie with a removal epoch");
});

test("a wall-clock step back neither delays a removal nor hot-loops", async () => {
    for (const step of [3_000, 60_000, 3_600_000]) {
        const W = world(); await W.start(); W.stall(() => true);
        W.remove(0); W.stepClock(-step); W.intervals[0].removedAt = W.now;
        const before = W.e.counters.epochs;
        await W.advance(5_000);
        ok(W, `step -${step} ms`);
        assert.ok(W.e.counters.epochs - before <= 2, `step -${step} ms: ${W.e.counters.epochs - before} epochs in 5 s`);
    }
});

test("a throwing rekey with a pending removal is retried promptly", async () => {
    const W = world(); await W.start();
    let n = 0; W.hooks.setKey = async () => { if (++n === 1) throw new Error("worker busy"); };
    W.remove(0); await W.advance(60_000); ok(W, "one transient setKey failure");
});

// Review 2b fix3 P3-A: one stored retry handle; persistent throws plus
// roster churn cannot multiply retry chains.
test("persistent rekey throws under roster churn keep a bounded rekey rate", async () => {
    const W = world(); await W.start();
    let calls = 0; W.hooks.setKey = async () => { calls++; throw new Error("worker rejects"); };
    W.remove(0);
    const marks = [];
    for (let i = 0; i < 300; i++) {
        await W.advance(200);
        W.join(500 + (i % 2)); if (i % 2) W.remove(19 - (i % 3)); else W.join(19 - (i % 3));
        if (i % 50 === 49) marks.push(calls);
    }
    const rates = marks.map((c, i) => c - (marks[i - 1] || 0));
    assert.ok(rates.every(r => r <= 60), `setKey calls per 10 s: ${rates}`);
});

// Review 2b fix3 P3-B (parent ruling, confidentiality first): while rekeys
// keep failing, the active sender epoch (held by the removed device) is
// retired at tD + REMOVAL_BOUND_MS: nothing is encrypted under it afterwards,
// the status says "paused", and a safe epoch resumes media once keys work.
test("persistent rekey throws past a removal's deadline silence the sender until a safe epoch", async () => {
    const { REMOVAL_BOUND_MS } = await import("../src/media-keys.js");
    const W = world(); await W.start();
    let failing = true; W.hooks.setKey = async () => { if (failing) throw new Error("worker rejects"); };
    const held = W.e.activeEpoch;
    W.remove(0);
    const tD = W.now;
    await W.advance(30_000);
    const retired = W.retired.find(r => r.kind === "one" && r.pid === "self|mic" && r.epoch === held);
    assert.ok(retired, "the removed device's epoch was never retired");
    assert.ok(retired.at - tD <= REMOVAL_BOUND_MS, `retired ${retired.at - tD} ms after the removal`);
    assert.equal(W.e.status().state, "paused");
    assert.ok(!W.active.some(([t, ep]) => t > tD && ep === held), "the retired epoch was activated again");
    failing = false;
    await W.advance(10_000);
    assert.notEqual(W.e.activeEpoch, held);
    assert.notEqual(W.e.status().state, "paused");
    assert.ok(!W.sends.some(s => s.device === "d0" && s.at > tD), "d0 received something after its removal");
});

// CodeRabbit (fix4) 1: a covering epoch that is installed but whose
// activation rejects or stalls never keeps the sender on the removed
// device's epoch past its deadline.
test("a covering epoch whose activation rejects or stalls never extends the exposure", async () => {
    const { REMOVAL_BOUND_MS } = await import("../src/media-keys.js");
    for (const mode of ["reject", "stall"]) {
        const W = world(); await W.start(); W.stall(() => true);
        const held = W.e.activeEpoch;
        let armed = true;
        W.hooks.activate = async (pid, ep) => {
            if (!armed || pid !== "self|mic" || ep === held) return;
            armed = false;
            if (mode === "reject") throw new Error("activate failed");
            await new Promise(() => {});
        };
        W.remove(0); const tD = W.now;
        await W.advance(REMOVAL_BOUND_MS + 50);
        const retired = W.retired.find(r => r.kind === "one" && r.pid === "self|mic" && r.epoch === held);
        assert.ok(retired && retired.at - tD <= REMOVAL_BOUND_MS, `${mode}: held epoch not retired by the deadline`);
        await W.advance(10_000);
        assert.ok(!W.active.some(([t, ep]) => t > tD && ep === held), `${mode}: held epoch active again`);
        if (mode === "reject") assert.notEqual(W.e.status().state, "paused", "reject: a later safe epoch resumes media");
        else assert.equal(W.e.status().state, "paused", "stall: stays paused while activation is stuck");
    }
});

// CodeRabbit (fix4) 2: a paused sender never resumes on an epoch whose
// recipient snapshot predates a removal whose deadline has passed.
test("an epoch that a later-removed device received never resumes sending after that device's deadline", async () => {
    const { REMOVAL_BOUND_MS } = await import("../src/media-keys.js");
    const W = world(); await W.start();
    W.stall(d => d !== "d1"); // d1 receives the removal epoch quickly; others stall
    let stallUntil = 0;
    W.hooks.activate = async (pid, ep) => { if (pid === "self|mic" && stallUntil) { const until = stallUntil; stallUntil = 0; await new Promise(r => W.timers.setTimeout(r, until - W.now)); } };
    W.remove(0); const t0 = W.now;
    await W.advance(500);                       // E1 (excludes d0) installed and sent to d1
    const e1 = W.sends.find(s => s.device === "d1" && s.at > t0)?.epoch;
    assert.ok(e1, "d1 received the removal epoch");
    stallUntil = t0 + 3_000;                    // E1's activation stalls past d1's deadline
    W.remove(1); const t1 = W.now;
    await W.advance(10_000);
    // After d1's deadline the sender never encrypts under E1 (which d1 holds).
    const activeAt = t => { let ep = 0; for (const [time, epoch] of W.active) if (time <= t) ep = epoch; return ep; };
    const e1Retired = W.retired.some(r => r.kind === "one" && r.pid === "self|mic" && r.epoch === e1);
    for (const [t, ep] of W.active) if (t >= t1 + REMOVAL_BOUND_MS && ep === e1) assert.ok(e1Retired, "E1 became the sending epoch after d1's deadline without being retired");
    assert.notEqual(activeAt(W.now), e1, "E1 is the final sending epoch");
    assert.notEqual(W.e.status().state, "paused", "a safe epoch resumed media");
    assert.ok(!W.sends.some(s => s.device === "d1" && s.at > t1), "d1 received something after its removal");
});
