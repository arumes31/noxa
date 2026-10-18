import test from "node:test";
import assert from "node:assert/strict";
import { logoPackets, packetState } from "../src/update-logo.js";

const placed = (packets, ratio) => packets.filter(packet => packetState(packet, ratio).progress === 1).length;

test("the mark is the ring, a three-wide X and both waveform bar groups", () => {
    const packets = logoPackets();
    assert.equal(packets.filter(p => p.part === "ring").length, 60);
    assert.equal(packets.filter(p => p.part === "x").length, 96);
    const bars = packets.filter(p => p.part === "bar");
    assert.ok(bars.length > 40);
    assert.equal(bars.filter(p => p.x < 100).length, bars.filter(p => p.x > 100).length);
    for (const p of packets) assert.ok(p.x >= 0 && p.x <= 200 && p.y >= 0 && p.y <= 200, "inside the 200 box");
});

test("nothing is placed before any bytes arrive and everything is home when complete", () => {
    const packets = logoPackets();
    for (const p of packets) {
        assert.equal(packetState(p, 0).alpha, 0);
        assert.equal(packetState(p, null).alpha, 0, "unknown size shows only the ghost");
        const done = packetState(p, 1);
        assert.equal(done.progress, 1);
        assert.ok(Math.abs(done.x - p.x) < 1e-9 && Math.abs(done.y - p.y) < 1e-9);
    }
});

test("the build advances with the downloaded share and never goes back", () => {
    const packets = logoPackets();
    let last = -1;
    for (let i = 0; i <= 100; i++) {
        const count = placed(packets, i / 100);
        assert.ok(count >= last, `ratio ${i}% placed ${count} after ${last}`);
        last = count;
    }
    // Half the bytes place roughly half of the ring and X.
    const body = packets.filter(p => p.part !== "bar");
    const half = body.filter(p => packetState(p, 0.5).progress === 1).length / body.length;
    assert.ok(half > 0.3 && half < 0.6, `half download placed ${half}`);
});

test("voice first: the waveform bars finish before the ring and X start", () => {
    const packets = logoPackets();
    const lastBar = Math.max(...packets.filter(p => p.part === "bar").map(p => p.start));
    const firstBody = Math.min(...packets.filter(p => p.part !== "bar").map(p => p.start));
    assert.ok(lastBar < firstBody);
    for (const p of packets.filter(p => p.part === "bar")) {
        assert.equal(packetState(p, 0.2).progress, 1, "bars are complete by 20%");
        assert.equal(p.ox, p.x, "bars grow vertically from the centre line");
    }
    for (const p of packets.filter(p => p.part !== "bar")) {
        assert.ok(p.ox === 40 || p.ox === 160, "ring and X grow out of a bar group");
    }
});

test("the painter redraws only for observed changes, skips hidden pixels and disposes all observers", async () => {
    const { createUpdateLogo } = await import("../src/update-logo.js");
    const keys = ["window", "document", "ResizeObserver", "MutationObserver", "getComputedStyle", "requestAnimationFrame", "cancelAnimationFrame"];
    const saved = Object.fromEntries(keys.map(k => [k, globalThis[k]]));
    const events = new Map(), observers = [], frames = [];
    let paints = 0;
    const preference = { matches: true };
    globalThis.window = { devicePixelRatio: 2, matchMedia: () => preference };
    globalThis.document = { hidden: false, documentElement: { dataset: {} }, addEventListener: (n, fn) => events.set(n, fn), removeEventListener: n => events.delete(n) };
    globalThis.getComputedStyle = () => ({ getPropertyValue: () => "" });
    globalThis.ResizeObserver = globalThis.MutationObserver = class { constructor(fn) { this.fn = fn; this.closed = false; observers.push(this); } observe() {} disconnect() { this.closed = true; } };
    globalThis.requestAnimationFrame = fn => { frames.push(fn); return frames.length; };
    globalThis.cancelAnimationFrame = () => {};
    const ctx = { setTransform() { paints++; }, createLinearGradient: () => ({ addColorStop() {} }), beginPath() {}, arc() {}, fill() {}, fillRect() {}, globalAlpha: 1, fillStyle: "" };
    const canvas = { isConnected: true, clientWidth: 400, clientHeight: 230, getClientRects: () => [1], getContext: () => ctx };
    try {
        const logo = createUpdateLogo(canvas);
        logo.update(0.25);
        assert.equal(paints, 1); assert.equal(canvas.width, 800);
        assert.equal(frames.length, 0, "reduced motion paints the measured ratio without frames");
        document.hidden = true; logo.update(0.5); assert.equal(paints, 1, "hidden documents are not painted");
        document.hidden = false; events.get("visibilitychange")(); assert.equal(paints, 2);
        preference.matches = false; logo.update(0.75);
        assert.equal(frames.length, 1, "motion tweens toward newer bytes");
        frames.shift()(globalThis.performance.now() + 10_000);
        assert.equal(frames.length, 0, "the tween stops once it arrives: no idle loop");
        logo.dispose();
        assert.equal(events.size, 0); assert.ok(observers.every(o => o.closed));
        const before = paints; logo.update(1); observers[0].fn(); assert.equal(paints, before);
    } finally { for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value; } }
});
