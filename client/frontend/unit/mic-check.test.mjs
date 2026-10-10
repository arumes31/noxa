import test from "node:test";
import assert from "node:assert/strict";
import { measureMic, recommendThreshold } from "../src/mic-check.js";

test("microphone measurements distinguish RMS, peak, silence and clipping", () => {
    const signal = measureMic(new Float32Array([0.5, -0.5, 0, 0]));
    assert.ok(Math.abs(signal.rms - Math.sqrt(0.125)) < 0.00001);
    assert.equal(signal.peak, 0.5);
    assert.equal(signal.mean, 0.25);
    assert.equal(signal.quality, "good");
    assert.equal(measureMic(new Float32Array(4)).quality, "silent");
    assert.equal(measureMic(new Float32Array([0.003, -0.003])).quality, "quiet");
    assert.equal(measureMic(new Float32Array([1, 0, 0, 0])).quality, "clipping");
});

test("calibration chooses a threshold between noise and speech in the live VAD scale", () => {
    const result = recommendThreshold(Array(20).fill(0.005), Array(20).fill(0.06));
    assert.equal(result.valid, true);
    assert.ok(result.suggested / 100 * 0.2 > 0.005);
    assert.ok(result.suggested / 100 * 0.2 < 0.06);
    assert.equal(recommendThreshold([0, 0], [0, 0]).valid, false);
    assert.equal(recommendThreshold([0.1, 0.1], [0.1, 0.1]).valid, false);
});

test("calibration rounds upward to a tenth of a percent without discarding fine adjustments", () => {
    assert.equal(recommendThreshold([0.0021], [0.0131]).suggested, 2.5);
    assert.equal(recommendThreshold([0.004], [0.02]).suggested, 4);
    assert.equal(recommendThreshold([0], [0.01]).suggested, 1.3);
    assert.equal(recommendThreshold([0], [0]).suggested, 1);
    assert.equal(recommendThreshold([1], [1]).suggested, 100);
    for (let i = 1; i <= 100; i++) {
        const floor = i / 100000, voice = 0.014;
        const result = recommendThreshold([floor], [voice]);
        const unrounded = Math.max(0.002, floor * 1.5, floor + (voice - floor) * 0.25) / 0.2 * 100;
        assert.ok(result.suggested >= unrounded - 1e-10);
        assert.ok(result.suggested - unrounded < 0.1 + 1e-10);
    }
});
