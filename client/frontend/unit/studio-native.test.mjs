import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { nativeProgram, overlayKey, nativeScenePayload, renderOverlays, createNativeSync } from "../src/studio-native.js";
import { createCaptureRegistry } from "../src/studio-captures.js";

const geometry = { x: 0, y: 0, w: 1, h: 1, crop: { l: 0, t: 0, r: 0, b: 0 } };
const src = (id, kind, extra = {}) => ({ id, kind, name: id, visible: true, ...geometry, ...extra });
const natives = {
    c1: { kind: "display", value: "m:0", source: { monitor: 0, window: "0" } },
    c2: { kind: "camera", camera: "\\\\?\\usb#cam" },
};
const resolve = source => natives[source.capture] || null;

describe("native studio program", () => {
    it("keeps the z-order: native layers with overlay runs between them", () => {
        const scene = { sources: [
            src("s1", "image", { asset: "a-0123456789abcdef" }),
            src("s2", "display", { capture: "c1", audio: true, x: 0.1, y: 0.2, w: 0.5, h: 0.4, crop: { l: 0.1, t: 0, r: 0, b: 0.2 } }),
            src("s3", "text", { text: "hi" }),
            src("s4", "text", { text: "there" }),
            src("s5", "camera", { capture: "c2" }),
            src("s6", "display", { capture: "gone" }), // no native capture: neutral, in an overlay
            src("s7", "text", { text: "hidden", visible: false }),
        ] };
        const { layers, groups } = nativeProgram(scene, resolve);
        assert.deepEqual(layers.map(layer => layer.kind), ["overlay", "display", "overlay", "camera", "overlay"]);
        assert.deepEqual(groups.map(group => group.map(source => source.id)), [["s1"], ["s3", "s4"], ["s6"]]);
        assert.deepEqual(layers[1], { kind: "display", x: 0.1, y: 0.2, w: 0.5, h: 0.4, crop: { l: 0.1, t: 0, r: 0, b: 0.2 },
            source: { monitor: 0, window: "0" }, audio: true });
        assert.deepEqual(layers[3].camera, "\\\\?\\usb#cam");
        assert.deepEqual([layers[0].overlay, layers[2].overlay, layers[4].overlay], [0, 1, 2]);
    });

    it("an empty or missing program has no layers", () => {
        assert.deepEqual(nativeProgram(null, resolve), { layers: [], groups: [] });
        assert.deepEqual(nativeProgram({ sources: [src("s1", "text", { visible: false })] }, resolve), { layers: [], groups: [] });
    });

    it("the overlay key changes with content and with image loading only", () => {
        const groups = [[src("s1", "text", { text: "a", size: 48, color: "#ffffff", background: "" })], [src("s2", "image", { asset: "a-1" })]];
        let loaded = false;
        const key = () => overlayKey(groups, { width: 1280, height: 720, ready: () => loaded });
        const before = key();
        assert.equal(key(), before);
        loaded = true;
        assert.notEqual(key(), before, "a loaded image redraws");
        const after = key();
        groups[0][0] = { ...groups[0][0], text: "b" };
        assert.notEqual(key(), after);
        assert.notEqual(overlayKey(groups, { width: 640, height: 360, ready: () => true }), key());
    });

    it("payloads carry the Take as a string and a bounded transition", () => {
        assert.deepEqual(nativeScenePayload(3, [], { kind: "fade", ms: 300.4 }, ["x"]), { take: "3", layers: [], transition: { kind: "fade", ms: 300 }, overlays: ["x"] });
        assert.deepEqual(nativeScenePayload(1, [], { kind: "cut", ms: 300 }, []).transition, { kind: "cut", ms: 0 });
        assert.deepEqual(nativeScenePayload(1, [], undefined, []).transition, { kind: "cut", ms: 0 });
    });

    it("renders each overlay group with the engine into a PNG", () => {
        const drawn = [];
        const canvas = { width: 0, height: 0, getContext: () => ({ id: "ctx" }), toDataURL: () => "data:image/png;base64,QUJD" };
        const engine = { drawSource: (ctx, source, width, height, preview) => drawn.push([ctx.id, source.id, width, height, preview]) };
        const out = renderOverlays(engine, [[{ id: "a" }, { id: "b" }], [{ id: "c" }]], 640, 360, { createElement: () => ({ ...canvas }) });
        assert.deepEqual(out, ["QUJD", "QUJD"]);
        assert.deepEqual(drawn, [["ctx", "a", 640, 360, false], ["ctx", "b", 640, 360, false], ["ctx", "c", 640, 360, false]]);
    });

    it("sends a Take at once and coalesces other changes, skipping unchanged programs", () => {
        let program = { take: 1, layers: [], groups: [], key: "k1", transition: { kind: "cut", ms: 0 } };
        const posted = [], timers = [];
        let clock = 0, renders = 0;
        const sync = createNativeSync({ build: () => program, render: () => { renders++; return []; }, post: scene => posted.push(scene),
            now: () => clock, setTimer: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clearTimer: () => {} });
        assert.equal(sync.initial().take, "1");
        sync.send(true);
        assert.equal(posted.length, 0, "the initial program is not sent twice");
        program = { ...program, take: 2 };
        sync.send(true);
        assert.deepEqual(posted.map(scene => scene.take), ["2"]);
        // An image loads: coalesced into one timer, not rendered at once.
        program = { ...program, key: "k2" };
        clock = 100;
        sync.send();
        sync.send();
        assert.equal(timers.length, 1);
        assert.equal(timers[0].ms, 150);
        timers[0].fn();
        assert.deepEqual(posted.map(scene => scene.take), ["2", "2"]);
        const rendered = renders;
        sync.send();
        timers[1].fn();
        assert.equal(posted.length, 2, "nothing changed: nothing sent");
        assert.equal(renders, rendered, "nothing changed: nothing rendered");
    });
});

describe("native captures in the registry", () => {
    it("have no stream, draw their thumbnail and can end", () => {
        const changes = [];
        const canvas = { width: 0, height: 0, drawn: 0, getContext: () => ({ drawImage: () => { canvas.drawn++; } }) };
        const registry = createCaptureRegistry({ createCanvas: () => canvas, onChange: (id, state) => changes.push([id, state]) });
        const id = registry.addNative({ kind: "display", label: "Monitor", surface: "monitor", native: natives.c1, audio: true });
        assert.equal(registry.state(id), "live");
        assert.equal(registry.drawable(id), null, "nothing painted yet");
        registry.paint(id, { naturalWidth: 320, naturalHeight: 180 });
        assert.equal(registry.drawable(id), canvas);
        assert.deepEqual([canvas.width, canvas.height, canvas.drawn], [320, 180, 1]);
        assert.equal(registry.get(id).nativeAudio, true);
        assert.deepEqual(registry.audioTracks(), []);
        const camera = registry.addNative({ kind: "camera", native: natives.c2 });
        assert.equal(registry.drawable(camera), null, "a native camera has no webview picture");
        registry.setHolder("draft", [id, camera]);
        registry.end(id);
        assert.equal(registry.state(id), "ended");
        assert.equal(registry.drawable(id), null);
        registry.setHolder("draft", []);
        assert.equal(registry.state(camera), "missing");
        assert.deepEqual(changes.map(change => change[1]), ["ended", "released", "released"]);
    });
});
