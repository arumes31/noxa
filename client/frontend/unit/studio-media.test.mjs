import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { inspectImageBytes, decodeImage, MAX_IMAGE_BYTES, StudioImageError } from "../src/studio-assets.js";
import { createCaptureRegistry } from "../src/studio-captures.js";
import { createStudioAudio } from "../src/studio-audio.js";
import { outputSize, fitRect, StudioEngine, WEAK_PREVIEW_FPS, PREVIEW_FPS } from "../src/studio-compositor.js";

const code = expected => error => error instanceof StudioImageError && error.code === expected;

function png(width, height) {
    const bytes = new Uint8Array(64);
    bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52]);
    new DataView(bytes.buffer).setUint32(16, width);
    new DataView(bytes.buffer).setUint32(20, height);
    return bytes;
}

function jpeg(width, height) {
    // SOI, APP0 (len 16), SOF0 (len 17), EOI
    const app0 = [0xff, 0xe0, 0x00, 0x10, ...new Array(14).fill(0)];
    const sof = [0xff, 0xc0, 0x00, 0x11, 8, height >> 8, height & 255, width >> 8, width & 255, 3, ...new Array(9).fill(0)];
    return new Uint8Array([0xff, 0xd8, ...app0, ...sof, 0xff, 0xd9]);
}

describe("studio images", () => {
    it("accepts PNG and JPEG by content and bounds declared dimensions", () => {
        assert.deepEqual(inspectImageBytes(png(640, 360)), { type: "image/png", width: 640, height: 360 });
        assert.deepEqual(inspectImageBytes(jpeg(1920, 1080)), { type: "image/jpeg", width: 1920, height: 1080 });
        assert.throws(() => inspectImageBytes(png(4097, 10)), code("image_too_large"));
        assert.throws(() => inspectImageBytes(jpeg(4000, 4097)), code("image_too_large"));
        assert.throws(() => inspectImageBytes(png(0, 10)), code("image_invalid"));
        assert.throws(() => inspectImageBytes(new Uint8Array(MAX_IMAGE_BYTES + 1)), code("image_too_large"));
    });

    it("refuses SVG, GIF, HTML, truncated and empty inputs", () => {
        const text = value => new TextEncoder().encode(value);
        assert.throws(() => inspectImageBytes(text('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>')), code("image_type"));
        assert.throws(() => inspectImageBytes(text("GIF89a......")), code("image_type"));
        assert.throws(() => inspectImageBytes(text("<!doctype html><img src=https://x>")), code("image_type"));
        assert.throws(() => inspectImageBytes(jpeg(10, 10).subarray(0, 12)), code("image_invalid"));
        assert.throws(() => inspectImageBytes(new Uint8Array()), code("image_invalid"));
    });

    it("rejects a decoded size that differs from the header", async () => {
        const closed = [];
        const createBitmap = async () => ({ width: 64, height: 64, close() { closed.push(true); } });
        await assert.rejects(decodeImage(png(640, 360), { createBitmap }), code("image_invalid"));
        assert.equal(closed.length, 1);
        const ok = await decodeImage(png(64, 64), { createBitmap });
        assert.equal(ok.width, 64);
    });

    it("accepts an EXIF-rotated JPEG (swapped size) but never a swapped PNG", async () => {
        const swapped = async () => ({ width: 1080, height: 1920, close() {} });
        const rotated = await decodeImage(jpeg(1920, 1080), { createBitmap: swapped });
        assert.deepEqual([rotated.width, rotated.height], [1080, 1920]);
        await assert.rejects(decodeImage(png(1920, 1080), { createBitmap: swapped }), code("image_invalid"));
    });
});

class FakeTrack extends EventTarget {
    constructor(kind) { super(); this.kind = kind; this.readyState = "live"; this.muted = false; this.label = kind; }
    stop() { this.readyState = "ended"; }
    getSettings() { return {}; }
}
class FakeStream {
    constructor(tracks = []) { this.tracks = [...tracks]; }
    getTracks() { return [...this.tracks]; }
    getVideoTracks() { return this.tracks.filter(track => track.kind === "video"); }
    getAudioTracks() { return this.tracks.filter(track => track.kind === "audio"); }
    removeTrack(track) { this.tracks = this.tracks.filter(item => item !== track); }
}
globalThis.MediaStream ??= FakeStream;
const fakeVideo = () => ({ readyState: 4, videoWidth: 640, srcObject: null, removeAttribute() {}, load() { this.readyState = 0; this.videoWidth = 0; }, pause() {}, play: async () => {} });

describe("studio capture ownership", () => {
    it("shares a capture across draft and program and stops it only when unreferenced", () => {
        const released = [];
        const registry = createCaptureRegistry({ createVideo: fakeVideo, onRelease: stream => released.push(stream) });
        const video = new FakeTrack("video");
        const id = registry.add(new FakeStream([video]), { kind: "display" });
        registry.setHolder("draft", [id]);
        registry.setHolder("live", [id]);
        assert.equal(registry.refs(id), 2);
        // Removing the draft source does not stop the picture on air.
        registry.setHolder("draft", []);
        assert.equal(registry.refs(id), 1);
        assert.equal(video.readyState, "live");
        assert.ok(registry.drawable(id));
        registry.setHolder("live", []);
        assert.equal(video.readyState, "ended");
        assert.equal(registry.state(id), "missing");
        assert.equal(released.length, 1);
    });

    it("releases a capture no holder adopted and stops unrequested audio", () => {
        const registry = createCaptureRegistry({ createVideo: fakeVideo });
        const audio = new FakeTrack("audio");
        const video = new FakeTrack("video");
        const id = registry.add(new FakeStream([video, audio]), { kind: "display", audio: false });
        assert.equal(audio.readyState, "ended", "audio that was not explicitly requested stops at once");
        assert.deepEqual(registry.audioTracks(), []);
        registry.setHolder("draft", []);
        assert.equal(registry.state(id), "missing");
        assert.equal(video.readyState, "ended");
    });

    it("neutralizes ended and muted sources immediately, dropping the last frame", () => {
        const changes = [];
        const registry = createCaptureRegistry({ createVideo: fakeVideo, onChange: (id, state) => changes.push(state) });
        const video = new FakeTrack("video");
        const id = registry.add(new FakeStream([video]), { kind: "camera" });
        registry.setHolder("live", [id]);
        const element = registry.get(id).video;
        video.muted = true;
        video.dispatchEvent(new Event("mute"));
        assert.equal(registry.drawable(id), null);
        video.muted = false;
        video.dispatchEvent(new Event("unmute"));
        assert.ok(registry.drawable(id));
        video.readyState = "ended";
        video.dispatchEvent(new Event("ended"));
        assert.equal(registry.drawable(id), null);
        assert.equal(element.srcObject, null, "the video element no longer holds the stream");
        assert.equal(element.videoWidth, 0, "the decoded frame was dropped");
        assert.deepEqual(changes, ["muted", "live", "ended"]);
        // Still referenced (no silent recapture): explicit restart required.
        assert.equal(registry.state(id), "ended");
    });

    it("calls the lease release hook once per capture, even after it ended", () => {
        const released = [];
        const registry = createCaptureRegistry({ createVideo: fakeVideo, onRelease: stream => released.push(stream) });
        const video = new FakeTrack("video");
        const id = registry.add(new FakeStream([video]), { kind: "display" });
        registry.setHolder("live", [id]);
        video.readyState = "ended";
        video.dispatchEvent(new Event("ended"));
        registry.setHolder("live", []);
        assert.equal(released.length, 1);
    });

    it("dispose stops every capture and refuses new ones", () => {
        const registry = createCaptureRegistry({ createVideo: fakeVideo });
        const tracks = [new FakeTrack("video"), new FakeTrack("video")];
        for (const track of tracks) registry.setHolder("draft", [registry.add(new FakeStream([track]), { kind: "display" })]);
        registry.dispose();
        assert.ok(tracks.every(track => track.readyState === "ended"));
        const late = new FakeTrack("video");
        assert.throws(() => registry.add(new FakeStream([late]), { kind: "display" }));
        assert.equal(late.readyState, "ended");
    });
});

class FakeNode {
    constructor(context, name) { this.context = context; this.name = name; this.outputs = new Set(); }
    connect(node) { this.outputs.add(node); this.context.edges.push([this.name, node.name]); return node; }
    disconnect() { this.outputs.clear(); }
}
class FakeAudioContext {
    constructor() { this.edges = []; this.state = "running"; this.sources = 0; this.destination = new FakeNode(this, "speakers"); }
    createMediaStreamDestination() { const node = new FakeNode(this, "publication"); node.stream = new FakeStream([new FakeTrack("audio")]); return node; }
    createGain() { return new FakeNode(this, "gain"); }
    createAnalyser() { const node = new FakeNode(this, "meter"); node.fftSize = 256; node.getFloatTimeDomainData = data => data.fill(0.1); return node; }
    createMediaStreamSource(stream) { this.sources++; const node = new FakeNode(this, `source:${stream.getAudioTracks()[0].label}`); return node; }
    async resume() {}
    async close() { this.state = "closed"; }
}

describe("studio audio graph", () => {
    it("routes explicitly enabled capture audio exactly once and never to the speakers", () => {
        let context;
        const audio = createStudioAudio({ AudioContextClass: class extends FakeAudioContext { constructor() { super(); context = this; } } });
        const app = new FakeTrack("audio"); app.label = "app";
        const other = new FakeTrack("audio"); other.label = "other";
        const available = [{ id: "c1", track: app }, { id: "c1", track: app }, { id: "c2", track: other }];
        audio.sync(available, new Set(["c1"]));
        audio.sync(available, new Set(["c1"]));
        assert.deepEqual(audio.routed(), ["c1"]);
        assert.equal(context.sources, 1, "duplicate references create one source node");
        assert.ok(context.edges.every(([, to]) => to !== "speakers"), "nothing is connected to the local output");
        assert.ok(audio.level() > 0);
        audio.sync(available, new Set());
        assert.deepEqual(audio.routed(), []);
        audio.dispose();
        assert.equal(context.state, "closed");
        assert.equal(audio.track.readyState, "ended");
    });
});

describe("studio output", () => {
    it("respects the server's existing video limits with a stable 16:9 size", () => {
        assert.deepEqual(outputSize(null), { width: 1280, height: 720 });
        assert.deepEqual(outputSize({ video_max_width: 640, video_max_height: 480 }), { width: 640, height: 360 });
        assert.deepEqual(outputSize({ video_max_width: 3840, video_max_height: 2160 }), { width: 1280, height: 720 });
        assert.deepEqual(outputSize({ video_max_width: 640 }), { width: 640, height: 360 });
        assert.deepEqual(outputSize({ video_max_height: 360 }), { width: 640, height: 360 });
        assert.deepEqual(fitRect({ x: 0, y: 0, w: 100, h: 100 }, 200, 100), { x: 0, y: 25, w: 100, h: 50 });
    });
});

function fakeDocument(draws) {
    const context = () => ({ fillRect() {}, drawImage: (...args) => draws.push(args), save() {}, restore() {}, beginPath() {}, rect() {}, clip() {}, fillText() {}, measureText: text => ({ width: text.length * 8 }) });
    return { createElement: () => { const ctx = context(); return { width: 0, height: 0, getContext: () => ctx, captureStream: () => new FakeStream([new FakeTrack("video")]) }; } };
}

describe("studio engine", () => {
    it("slows only the preview on a weak device; the program rate stays fixed", () => {
        let clock = 0, step = 0;
        const registry = { setHolder() {}, drawable: () => null, state: () => "missing", dispose() {} };
        const engine = new StudioEngine({ registry, width: 64, height: 36, document: fakeDocument([]), now: () => (clock += step), setTimer: () => 1, clearTimer() {} });
        assert.equal(engine.previewEvery, 30 / PREVIEW_FPS);
        step = 40; // every render costs more than the 33 ms frame budget
        for (let i = 0; i < 60; i++) engine.tick();
        assert.equal(engine.previewEvery, 30 / WEAK_PREVIEW_FPS);
        assert.equal(engine.fps, 30);
        engine.dispose();
    });

    it("never draws a source whose capture is not drawable and refuses mutable snapshots", () => {
        const draws = [];
        const registry = { setHolder() {}, drawable: () => null, state: () => "ended", dispose() {} };
        const engine = new StudioEngine({ registry, width: 64, height: 36, document: fakeDocument(draws), setTimer: () => 1, clearTimer() {} });
        const scene = Object.freeze({ id: "sc1", sources: Object.freeze([Object.freeze({ id: "src2", kind: "display", capture: "c1", visible: true, x: 0, y: 0, w: 1, h: 1, crop: Object.freeze({ l: 0, t: 0, r: 0, b: 0 }) })]) });
        engine.take(scene);
        assert.equal(draws.length, 0, "no stale frame is drawn");
        assert.throws(() => engine.take({ id: "sc2", sources: [] }), /immutable/);
        const stopped = [];
        engine.captureTrack = { readyState: "live", stop: () => stopped.push(true) };
        engine.dispose();
        assert.equal(stopped.length, 1);
        assert.equal(engine.program.width, 0);
    });
});

describe("studio error messages", () => {
    it("translates every state and image error code in English and German", async () => {
        const { readFile } = await import("node:fs/promises");
        const { studioEnglish, studioGerman } = await import("../src/studio-messages.js");
        const sources = await Promise.all(["studio-state.js", "studio-assets.js"].map(file => readFile(new URL(`../src/${file}`, import.meta.url), "utf8")));
        const codes = new Set(sources.flatMap(source => [...source.matchAll(/(?:StudioStateError|StudioImageError)\("([a-z_]+)"\)/g)].map(match => match[1])));
        assert.ok(codes.size >= 10);
        for (const code of codes) {
            assert.ok(studioEnglish[`studio.error.${code}`], `en ${code}`);
            assert.ok(studioGerman[`studio.error.${code}`], `de ${code}`);
        }
    });
});
