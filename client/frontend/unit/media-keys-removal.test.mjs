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
