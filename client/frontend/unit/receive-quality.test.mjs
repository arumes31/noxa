import test from "node:test";
import assert from "node:assert/strict";
import { allocateReceiveQuality, settleAutoQuality } from "../src/receive-quality.js";

const stream = (key, preference = "auto", slot = "screen", highBitrate = 4000000) => ({ key, preference, slot, highBitrate });

test("manual streams retain independent quality even when several request high", () => {
    const result = allocateReceiveQuality([stream("a", "high"), stream("b", "high"), stream("c", "low")],
        { availableBitrate: 1000000, cpuPressure: true, focusedID: "c" });
    assert.deepEqual([...result], [["a", "high"], ["b", "high"], ["c", "low"]]);
});

test("auto admits multiple full resolution shares with headroom and prioritizes focus under contention", () => {
    const streams = [stream("a"), stream("b"), stream("c")];
    assert.deepEqual([...allocateReceiveQuality(streams, { availableBitrate: 20000000 }).values()], ["high", "high", "high"]);
    const limited = allocateReceiveQuality(streams, { availableBitrate: 7000000, focusedID: "b" });
    assert.equal(limited.get("b"), "high");
    assert.notEqual(limited.get("a"), "high");
    assert.notEqual(limited.get("c"), "high");
});

test("manual reservations reduce only the automatic budget and explicit low bandwidth overrides all", () => {
    const streams = [stream("manual", "high"), stream("auto")];
    assert.deepEqual([...allocateReceiveQuality(streams, { availableBitrate: 5500000 }).values()], ["high", "low"]);
    assert.deepEqual([...allocateReceiveQuality(streams, { lowBandwidth: true }).values()], ["low", "low"]);
});

test("unknown bandwidth does not invent congestion and CPU affects only Auto", () => {
    const streams = [stream("screen"), stream("cam", "auto", "cam"), stream("manual", "high")];
    for (const availableBitrate of [undefined, null, NaN, 0, -1]) {
        assert.deepEqual([...allocateReceiveQuality(streams, { availableBitrate }).values()], ["high", "mid", "high"]);
    }
    assert.deepEqual([...allocateReceiveQuality(streams, { cpuPressure: true }).values()], ["mid", "mid", "high"]);
});

test("single-layer publishers reserve measured traffic even when Low was requested", () => {
    const streams = [{ ...stream("legacy", "low"), bitrate: 4000000 }, stream("auto")];
    assert.deepEqual([...allocateReceiveQuality(streams, { availableBitrate: 5500000 }).values()], ["low", "low"]);
});

test("Auto ignores short LTE dips, descends one level at a time and recovers quickly", () => {
    let state = { applied: "high" };
    for (let now = 0; now < 18000; now += 100) {
        state = settleAutoQuality(state, "low", false, now);
        assert.equal(state.applied, "high", "extra UI refreshes cannot accelerate downgrade");
    }
    state = settleAutoQuality(state, "low", false, 18000);
    assert.equal(state.applied, "mid", "never jump directly from high to low");
    state = settleAutoQuality(state, "low", false, 21000);
    assert.equal(settleAutoQuality(state, "low", false, 38000).applied, "mid");
    state = settleAutoQuality(state, "low", false, 39000);
    assert.equal(state.applied, "low");
    state = settleAutoQuality(state, "high", false, 40000);
    assert.equal(settleAutoQuality(state, "high", false, 42999).applied, "low");
    assert.equal(settleAutoQuality(state, "high", false, 43000).applied, "high");
});

test("recovery cancels a pending drop and manual/CPU/low-bandwidth changes stay immediate", () => {
    let state = settleAutoQuality({ applied: "high" }, "low", false, 0);
    state = settleAutoQuality(state, "high", false, 17000);
    state = settleAutoQuality(state, "low", false, 18000);
    assert.equal(settleAutoQuality(state, "low", false, 35000).applied, "high");
    assert.equal(settleAutoQuality(state, "low", true, 35000).applied, "low");
});
