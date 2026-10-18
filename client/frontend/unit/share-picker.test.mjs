import test from "node:test";
import assert from "node:assert/strict";
import { cropDisplayStream, cropRect, normalizeRegion, regionLargeEnough, sourceConstraints, MIN_REGION_PX } from "../src/share-region.js";
import { DEFAULT_SHARE_PRESET, presetSpec, shareSummary, screenShareConstraints, shareQuality } from "../src/screen-share-quality.js";

test("a drag in any direction becomes a rectangle inside the preview", () => {
    assert.deepEqual(normalizeRegion({ x: 0.75, y: 0.5 }, { x: 0.25, y: 0.1 }), { x: 0.25, y: 0.1, width: 0.5, height: 0.4 });
    const outside = normalizeRegion({ x: -0.2, y: 0.5 }, { x: 1.4, y: 2 });
    assert.deepEqual(outside, { x: 0, y: 0.5, width: 1, height: 0.5 });
});

test("crop rectangles are even-aligned and stay inside odd-sized frames", () => {
    assert.deepEqual(cropRect(1920, 1080, { x: 0.25, y: 0.25, width: 0.5, height: 0.5 }), { x: 480, y: 270, width: 960, height: 540 });
    const odd = cropRect(1001, 601, { x: 0.333, y: 0.111, width: 0.777, height: 0.9 });
    for (const value of Object.values(odd)) assert.equal(value % 2, 0);
    assert.ok(odd.x + odd.width <= 1001 && odd.y + odd.height <= 601);
    const edge = cropRect(640, 360, { x: 1, y: 1, width: 0, height: 0 });
    assert.ok(edge.width >= 2 && edge.height >= 2 && edge.x + edge.width <= 640 && edge.y + edge.height <= 360);
});

test("tiny areas are rejected before sharing", () => {
    assert.equal(regionLargeEnough({ x: 0, y: 0, width: MIN_REGION_PX / 1920, height: 0.5 }, 1920, 1080), true);
    assert.equal(regionLargeEnough({ x: 0, y: 0, width: 0.01, height: 0.5 }, 1920, 1080), false);
    assert.equal(regionLargeEnough({ x: 0, y: 0, width: 0.5, height: 0.5 }, 0, 0), false);
    assert.equal(regionLargeEnough(null, 1920, 1080), false);
});

test("the source is scaled so the cropped area fits the chosen quality", () => {
    const native = { width: 3840, height: 2160 };
    const half = { x: 0, y: 0, width: 0.5, height: 0.5 };
    const hd = screenShareConstraints(shareQuality("hd"), null);
    // Half of a 4K screen is already Full HD: the source keeps its size.
    assert.deepEqual(sourceConstraints(native, half, hd), { width: { ideal: 3840, max: 3840 }, height: { ideal: 2160, max: 2160 }, frameRate: hd.frameRate });
    const whole = sourceConstraints(native, { x: 0, y: 0, width: 1, height: 1 }, hd);
    assert.deepEqual([whole.width.max, whole.height.max], [1920, 1080]);
    // A tall area is limited by height.
    const tall = sourceConstraints(native, { x: 0.4, y: 0, width: 0.1, height: 1 }, hd);
    assert.deepEqual([tall.width.max, tall.height.max], [1920, 1080]);
    // Original quality only follows server limits.
    const original = screenShareConstraints(shareQuality("original"), null);
    assert.deepEqual(sourceConstraints(native, half, original).width, { ideal: 3840, max: 3840 });
    const limited = sourceConstraints(native, half, screenShareConstraints(shareQuality("original"), { video_max_width: 1280, video_max_height: 720 }));
    assert.deepEqual([limited.width.max, limited.height.max], [2560, 1440]);
    // Never upscales a small source.
    assert.deepEqual(sourceConstraints({ width: 800, height: 600 }, half, hd).width, { ideal: 800, max: 800 });
});

test("the share bar summarises preset, size, frame rate and bitrate; Full HD is the default", () => {
    assert.equal(DEFAULT_SHARE_PRESET, "hd");
    assert.equal(presetSpec("hd"), "1080p · 30 fps");
    assert.equal(presetSpec("hdMotion"), "1080p · 60 fps");
    assert.equal(presetSpec("custom", { width: 3440, height: 1440, fps: 60 }), "3440×1440 · 60 fps");
    assert.match(presetSpec("original"), /30 fps/);
    const summary = shareSummary({ preset: "hd", bitrateCeiling: 0 });
    assert.equal(summary.spec, "1080p · 30 fps · Auto");
    assert.equal(shareSummary({ preset: "uhd", bitrateCeiling: 6000000 }).spec, "2160p · 30 fps · 6 Mbit/s");
});

test("a failed crop frame ends the generated track and releases its source", async (t) => {
    const source = new EventTarget();
    source.getSettings = () => ({ width: 1920, height: 1080 });
    source.readyState = "live";
    source.stop = () => { source.readyState = "ended"; };
    let frameClosed = false;
    const frame = { codedWidth: 1920, codedHeight: 1080, close: () => { frameClosed = true; } };
    const replacements = {
        MediaStreamTrackProcessor: class {
            readable = new ReadableStream({ start: controller => controller.enqueue(frame) });
        },
        MediaStreamTrackGenerator: class extends EventTarget {
            writable = new WritableStream();
            readyState = "live";
            stop() { this.readyState = "ended"; }
        },
        VideoFrame: class { constructor() { throw new Error("crop rejected by runtime"); } },
        MediaStream: class {
            constructor(tracks) { this.tracks = tracks; }
            getVideoTracks() { return this.tracks; }
        },
    };
    for (const [key, value] of Object.entries(replacements)) {
        const descriptor = Object.getOwnPropertyDescriptor(globalThis, key);
        Object.defineProperty(globalThis, key, { value, configurable: true, writable: true });
        t.after(() => { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key]; });
    }
    const { stream } = cropDisplayStream({ getVideoTracks: () => [source], getAudioTracks: () => [] },
        { x: 0.1, y: 0.1, width: 0.5, height: 0.5 });
    const track = stream.getVideoTracks()[0];
    await new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error("crop failure did not end publication")), 1000);
        track.addEventListener("ended", () => { clearTimeout(timer); resolve(); }, { once: true });
    });
    assert.equal(track.readyState, "ended");
    assert.equal(source.readyState, "ended");
    assert.equal(frameClosed, true);
});
