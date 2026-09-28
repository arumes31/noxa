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
