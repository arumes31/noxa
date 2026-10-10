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

test("automatic changes require sustained samples while explicit user changes apply immediately", () => {
    let state = { applied: "high" };
    state = settleAutoQuality(state, "low");
    assert.equal(state.applied, "high");
    state = settleAutoQuality(state, "high");
    state = settleAutoQuality(state, "low");
    assert.equal(state.applied, "high");
    state = settleAutoQuality(state, "low");
    assert.equal(state.applied, "low");
    state = settleAutoQuality(state, "high");
    state = settleAutoQuality(state, "high");
    assert.equal(state.applied, "low");
    assert.equal(settleAutoQuality(state, "high").applied, "high");
    assert.equal(settleAutoQuality(state, "mid", true).applied, "mid");
});
