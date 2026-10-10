import test from "node:test";
import assert from "node:assert/strict";
import { createPeerStatsSampler } from "../src/peer-stats.js";
import { summarizeMedia } from "../src/connection-stats.js";

const report = (timestamp = 1000, bytesReceived = 1000) => new Map([["in", {
    id: "in", type: "inbound-rtp", kind: "audio", timestamp, bytesReceived,
}]]);
const deferred = () => {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
};

test("concurrent consumers and a recent completed sample share one native collection", async () => {
    let time = 100, calls = 0;
    const sample = createPeerStatsSampler({ now: () => time }), pending = deferred(), snapshot = report();
    const pc = { getStats() { calls++; return pending.promise; } };
    const one = sample(pc), two = sample(pc);
    assert.equal(one, two);
    assert.equal(calls, 1);
    pending.resolve(snapshot);
    assert.equal(await one, snapshot);
    time += 250;
    assert.equal(await sample(pc), snapshot);
    assert.equal(calls, 1);
    time++;
    await sample(pc);
    assert.equal(calls, 2);
});

test("freshness starts when collection starts, and clock rollback cannot reuse a sample", async () => {
    let time = 100, calls = 0;
    const sample = createPeerStatsSampler({ now: () => time }), pending = deferred();
    const pc = { getStats() { return ++calls === 1 ? pending.promise : report(); } };
    const first = sample(pc);
    time += 300;
    pending.resolve(report());
    await first;
    await sample(pc);
    assert.equal(calls, 2);
    time = 50;
    await sample(pc);
    assert.equal(calls, 3);
});

test("fresh publication samples wait for an older native call without accepting its result", async () => {
    const sample = createPeerStatsSampler(), before = deferred(), after = deferred();
    let calls = 0;
    const pc = { getStats() { return ++calls === 1 ? before.promise : after.promise; } };
    const old = sample(pc), fresh = sample(pc, { fresh: true });
    assert.equal(calls, 1);
    before.resolve(report(1000));
    assert.equal((await old).get("in").timestamp, 1000);
    assert.equal(calls, 2);
    const otherConsumer = sample(pc);
    after.resolve(report(2000));
    assert.equal((await fresh).get("in").timestamp, 2000);
    assert.equal(await otherConsumer, await fresh);
    assert.equal(calls, 2);
    await sample(pc, { fresh: true });
    assert.equal(calls, 3);
});

test("different and replacement peers have independent caches, and closed peers reject late reports", async () => {
    const sample = createPeerStatsSampler(), pending = deferred();
    let calls = 0;
    const old = { connectionState: "connected", getStats() { calls++; return pending.promise; } };
    const replacement = { getStats() { calls++; return report(2000); } };
    const waiting = sample(old);
    assert.equal((await sample(replacement)).get("in").timestamp, 2000);
    old.connectionState = "closed";
    pending.resolve(report());
    await assert.rejects(waiting, /closed/);
    await assert.rejects(sample(old), /closed/);
    assert.equal(calls, 2);
    replacement.signalingState = "closed";
    await assert.rejects(sample(replacement), /closed/);
    assert.equal(calls, 2);
});

test("rejections, synchronous errors and invalid reports do not poison later collection", async () => {
    const sample = createPeerStatsSampler();
    let calls = 0;
    const pc = { getStats() {
        if (++calls === 1) throw new Error("sync failure");
        if (calls === 2) return Promise.reject(new Error("async failure"));
        if (calls === 3) return null;
        return report();
    } };
    await assert.rejects(sample(null), /unavailable/);
    await assert.rejects(sample({}), /unavailable/);
    await assert.rejects(sample(pc), /sync failure/);
    await assert.rejects(sample(pc), /async failure/);
    await assert.rejects(sample(pc), /invalid/);
    assert.equal((await sample(pc)).get("in").timestamp, 1000);
    assert.equal(calls, 4);
});

test("timeout bounds all consumers, prevents overlapping retry storms and discards a late result", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const sample = createPeerStatsSampler(), pending = deferred();
    let calls = 0;
    const pc = { getStats() { return ++calls === 1 ? pending.promise : report(2000); } };
    const first = assert.rejects(sample(pc), /timeout/);
    const fresh = assert.rejects(sample(pc, { fresh: true }), /timeout/);
    t.mock.timers.tick(5000);
    await Promise.all([first, fresh]);
    for (let i = 0; i < 10; i++) await assert.rejects(sample(pc), /timeout/);
    assert.equal(calls, 1);
    pending.resolve(report(1000));
    await Promise.resolve();
    assert.equal((await sample(pc)).get("in").timestamp, 2000);
    assert.equal(calls, 2);
});

test("sharing snapshots leaves each consumer's delta baseline independent", async () => {
    let time = 0, calls = 0;
    const sample = createPeerStatsSampler({ now: () => time });
    const snapshots = [report(1000, 1000), report(2000, 2000), report(3000, 5000)];
    const pc = { getStats: () => snapshots[calls++] };
    const fastBefore = summarizeMedia(await sample(pc));
    const slowBefore = summarizeMedia(await sample(pc));
    time = 1000;
    const fastMiddle = summarizeMedia(await sample(pc), fastBefore);
    time = 2000;
    const latest = await sample(pc);
    assert.equal(summarizeMedia(latest, fastMiddle).inRate, 3000);
    assert.equal(summarizeMedia(latest, slowBefore).inRate, 2000);
    assert.equal(calls, 3);
    assert.deepEqual(snapshots.map(s => s.get("in").bytesReceived), [1000, 2000, 5000]);
});
