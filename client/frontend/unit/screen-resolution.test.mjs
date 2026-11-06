import assert from "node:assert/strict";
import test from "node:test";
import { createScreenResolutionController } from "../src/screen-resolution.js";

function fixture(options = {}) {
    const update = createScreenResolutionController();
    let time = 0, captured = 0, encoded = 0, scale = 1;
    return { tick({ reason = "bandwidth", captureFPS = 30, encodeFPS = 10, ms = 5000, ...extra } = {}) {
        time += ms; captured += captureFPS * ms / 1000; encoded += encodeFPS * ms / 1000;
        scale = update({ scale, width: 1920, height: 1080, fps: 30, ...options,
            sample: { id: "sender:source", time, captureTime: time, captured, encoded, reason, ...extra } });
        return scale;
    }, update };
}

test("sustained throughput loss snaps 1080p to 720p, then lowers it gradually", () => {
    const f = fixture();
    assert.equal(f.tick(), 1); // first sample only establishes a baseline
    assert.equal(f.tick(), 1.5); // 5 s of pressure: 1080 -> 720 in one step
    assert.equal(f.tick(), 1.5); // the encoder gets a full period at 720p
    assert.ok(Math.abs(f.tick() - 1.62) < 1e-9); // 10 s more: gradual steps
    for (let i = 0; i < 100; i++) f.tick();
    assert.equal(f.tick(), 2); // 1080 / 540, never 180p
    assert.equal(f.tick({ encodeFPS: 30 }), 1.6);
    assert.ok(Math.abs(f.tick({ encodeFPS: 30 }) - 1.28) < 1e-9);
    f.tick({ encodeFPS: 30 });
    assert.equal(f.tick({ encodeFPS: 30 }), 1);
});

test("latency, loss, CPU overload and a quiet desktop alone do not downscale", () => {
    for (const input of [{ reason: "none", encodeFPS: 30, roundTripTime: 2, packetsLost: 1000 },
        { reason: "cpu" }, { reason: null }, { captureFPS: 1, encodeFPS: 1 },
        { captureFPS: 30, encodeFPS: 30 }]) {
        const f = fixture();
        for (let i = 0; i < 30; i++) assert.equal(f.tick(input), 1);
    }
});

test("brief bursts under five seconds keep the selected resolution", () => {
    const f = fixture();
    f.tick();
    for (let i = 0; i < 10; i++) {
        assert.equal(f.tick({ ms: 4000 }), 1);
        assert.equal(f.tick({ ms: 1000, encodeFPS: 30, reason: "none" }), 1);
    }
});

test("sources at or below 720p skip the snap and keep the gradual 10 s rule", () => {
    for (const size of [{ width: 1280, height: 720 }, { width: 960, height: 540 }]) {
        const f = fixture(size);
        f.tick();
        assert.equal(f.tick(), 1);
        if (size.height === 720) {
            assert.equal(f.tick(), 1.08);
        } else {
            for (let i = 0; i < 20; i++) assert.equal(f.tick(), 1); // already at the 540 floor
        }
    }
});

test("1440p and 4K sources also snap to a 720p short side", () => {
    for (const [size, want] of [[{ width: 2560, height: 1440 }, 2], [{ width: 3840, height: 2160 }, 3]]) {
        const f = fixture(size);
        f.tick();
        assert.equal(f.tick(), want);
    }
});

test("missing, reset, repeated and long-gap stats cannot manufacture sustained congestion", () => {
    const f = fixture();
    f.tick();
    assert.equal(f.tick({ ms: 0 }), 1);
    assert.equal(f.tick({ ms: 15000 }), 1);
    assert.equal(f.tick({ ms: 4000 }), 1);
    assert.equal(f.tick({ encoded: 0 }), 1);
    assert.equal(f.update({ sample: null, scale: 1.5, width: 1920, height: 1080 }), 1.5);
    assert.equal(f.tick({ captured: NaN }), 1);
    assert.equal(f.tick(), 1);
});

test("explicit frame cap and portrait/source geometry are respected", () => {
    const capped = fixture({ fps: 15 });
    for (let i = 0; i < 30; i++) assert.equal(capped.tick({ captureFPS: 60, encodeFPS: 15 }), 1);
    for (const options of [{ width: 1080, height: 1920 }, { width: 1920, height: 1080 }]) {
        const f = fixture(options);
        f.tick();
        assert.equal(f.tick(), 1.5);
        for (let i = 0; i < 80; i++) f.tick();
        assert.equal(f.tick(), 2);
    }
    const small = fixture({ width: 640, height: 360 });
    for (let i = 0; i < 30; i++) assert.equal(small.tick(), 1);
});

test("intermittent recovery restarts the full downgrade grace period", () => {
    const f = fixture();
    f.tick();
    assert.equal(f.tick({ ms: 4000 }), 1);
    assert.equal(f.tick({ encodeFPS: 30, reason: "none" }), 1);
    assert.equal(f.tick({ ms: 4000 }), 1);
    assert.equal(f.tick(), 1.5);
});

test("a downscaled moving share restores sharp text when capture becomes static", () => {
    for (const captureFPS of [0, 1, 5]) {
        const f = fixture();
        for (let i = 0; i < 20; i++) f.tick();
        assert.equal(f.tick(), 2);
        for (let i = 0; i < 4; i++) f.tick({ captureFPS, encodeFPS: captureFPS });
        assert.equal(f.tick({ captureFPS, encodeFPS: captureFPS }), 1);
    }
});
