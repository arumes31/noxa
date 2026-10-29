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

test("brief LTE bursts keep resolution; sustained throughput loss lowers it continuously", () => {
    const f = fixture();
    for (let i = 0; i < 4; i++) assert.equal(f.tick(), 1);
    assert.equal(f.tick(), 1.08);
    assert.ok(Math.abs(f.tick() - 1.1664) < 1e-9);
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

test("missing, reset, repeated and long-gap stats cannot manufacture sustained congestion", () => {
    const f = fixture();
    for (let i = 0; i < 4; i++) f.tick();
    assert.equal(f.tick({ ms: 0 }), 1);
    assert.equal(f.tick({ ms: 15000 }), 1);
    for (let i = 0; i < 3; i++) assert.equal(f.tick(), 1);
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
        for (let i = 0; i < 80; i++) f.tick();
        assert.equal(f.tick(), 2);
    }
    const small = fixture({ width: 640, height: 360 });
    for (let i = 0; i < 30; i++) assert.equal(small.tick(), 1);
});

test("intermittent recovery restarts the full downgrade grace period", () => {
    const f = fixture();
    for (let i = 0; i < 4; i++) f.tick();
    assert.equal(f.tick({ encodeFPS: 30, reason: "none" }), 1);
    for (let i = 0; i < 3; i++) assert.equal(f.tick(), 1);
    assert.equal(f.tick(), 1.08);
});
