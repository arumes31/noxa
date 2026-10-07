import { test } from "node:test";
import assert from "node:assert/strict";
import { VoiceGate } from "../src/voice-gate-core.js";

function render(gate, value, frames, channels = 1) {
    const result = Array.from({ length: channels }, () => []);
    for (let at = 0; at < frames; at += 128) {
        const size = Math.min(128, frames - at);
        const input = Array.from({ length: channels }, (_, c) => new Float32Array(size).fill(Array.isArray(value) ? value[c] : value));
        const output = input.map(() => new Float32Array(size));
        gate.process(input, output);
        output.forEach((samples, c) => result[c].push(...samples));
    }
    return result;
}

for (const rate of [44100, 48000]) {
    test(`VAD preserves the quiet onset after a minute of silence at ${rate} Hz`, () => {
        const gate = new VoiceGate(rate);
        gate.configure({ mode: "vad", threshold: 0.1, blocked: false });
        render(gate, 0, rate * 60);
        render(gate, 0.04, rate * 0.06);
        const [heard] = render(gate, 0.4, rate * 0.2);
        assert.ok(heard.slice(0, rate * 0.04).every(sample => sample === 0));
        assert.ok(heard.slice(rate * 0.04, rate * 0.1).every(sample => Math.abs(sample - 0.04) < 1e-6));
        assert.ok(heard.slice(rate * 0.1).every(sample => Math.abs(sample - 0.4) < 1e-6));
    });
}

test("VAD retains 450 ms of quiet endings after the delayed speech", () => {
    const gate = new VoiceGate(48000);
    gate.configure({ mode: "vad", threshold: 0.1, blocked: false });
    render(gate, 0.4, 4800);
    render(gate, 0.04, 48000 * 0.54);
    assert.equal(gate.active, true);
    render(gate, 0.04, 48000 * 0.02);
    assert.equal(gate.active, false);
});

test("mute clears pre-roll and never buffers private speech", () => {
    const gate = new VoiceGate(48000);
    gate.configure({ mode: "vad", threshold: 0.1, blocked: false });
    render(gate, 0.4, 4800);
    gate.configure({ mode: "vad", threshold: 0.1, blocked: true });
    assert.ok(render(gate, 0.8, 4800)[0].every(sample => sample === 0));
    gate.configure({ mode: "vad", threshold: 0.1, blocked: false });
    assert.ok(render(gate, 0.4, 4800)[0].every(sample => sample === 0));
});

test("PTT and continuous bypass the buffer and preserve stereo", () => {
    const gate = new VoiceGate(48000);
    gate.configure({ mode: "vad", threshold: 0.1, blocked: false });
    render(gate, 0.8, 4800);
    for (const mode of ["ptt", "continuous"]) {
        gate.configure({ mode, blocked: false });
        const result = render(gate, [0.5, -0.25], 128, 2);
        assert.ok(result[0].every(sample => sample === 0.5));
        assert.ok(result[1].every(sample => sample === -0.25));
        gate.configure({ mode, blocked: true });
        assert.ok(render(gate, 0.8, 128)[0].every(sample => sample === 0));
    }
});

test("right-channel speech opens VAD and repeated settings do not erase pre-roll", () => {
    const gate = new VoiceGate(48000);
    const settings = { mode: "vad", threshold: 0.1, blocked: false };
    gate.configure(settings);
    render(gate, [0.02, 0.04], 4800, 2);
    gate.configure(settings);
    const heard = render(gate, [0, 0.4], 128, 2);
    assert.equal(gate.active, true);
    assert.ok(heard[0].every(sample => Math.abs(sample - 0.02) < 1e-6));
    assert.ok(heard[1].every(sample => Math.abs(sample - 0.04) < 1e-6));
});
