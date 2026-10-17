import { test, mock } from "node:test";
import assert from "node:assert/strict";

// The Messages view's video button calls media.start("camera"): repeating it
// while the camera is pending or live must never switch the camera off.
let pending = [];
mock.module(new URL("../src/camera-capture.js", import.meta.url).href, { namedExports: {
    applyCameraPreview() {},
    captureCamera: (_settings, _limits, { signal }) => new Promise(resolve => pending.push({ resolve, signal })),
} });
mock.module(new URL("../src/i18n.js", import.meta.url).href, { namedExports: { t: key => key } });

function element() {
    return {
        dataset: {}, style: {}, children: [], classList: { toggle() {} },
        setAttribute() {}, addEventListener() {}, play: async () => {}, pause() {},
        append(...items) { this.children.push(...items); },
        replaceChildren(...items) { this.children = items; },
        querySelector() { return element(); }, querySelectorAll() { return []; },
    };
}
globalThis.document = { createElement: element };
globalThis.window = globalThis;
globalThis.addEventListener ??= () => {};
globalThis.removeEventListener ??= () => {};
globalThis.__noxa = { state: { settings: {} }, toast() {} };
globalThis.MediaStream ??= class { constructor(tracks) { this.tracks = tracks; } };

const { createCallMedia } = await import("../src/private-call-media.js");

// The camera module is imported lazily; wait until the capture is requested.
async function capturing() {
    for (let i = 0; i < 200 && !pending.length; i++) await new Promise(resolve => setTimeout(resolve, 5));
}

function fixture() {
    pending = [];
    const owner = { uid: "me", panel: element(), peers: new Map(), call: { participants: [{ unique_id: "me", state: "accepted" }] } };
    const media = createCallMedia(owner, { current: () => true, acceptedPeer: () => true, label: id => id, report: error => { throw error; }, render() {} });
    const track = { kind: "video", readyState: "live", addEventListener() {} };
    let stopped = 0;
    const capture = { stream: { getTracks: () => [track] }, stop: () => { stopped++; } };
    return { media, capture, stopped: () => stopped };
}

test("start keeps a pending camera request instead of cancelling it", async () => {
    const f = fixture();
    const first = f.media.start("camera");
    await capturing();
    assert.equal(pending.length, 1);
    assert.equal(f.media.isActive("camera"), true);
    await f.media.start("camera");
    assert.equal(pending.length, 1, "no second capture");
    assert.equal(pending[0].signal.aborted, false, "pending request kept");
    pending[0].resolve(f.capture); await first;
    assert.equal(f.media.isActive("camera"), true);
    assert.equal(f.stopped(), 0);
});

test("start keeps a live camera; toggle still switches it off", async () => {
    const f = fixture();
    const first = f.media.start("camera");
    await capturing();
    pending[0].resolve(f.capture); await first;
    await f.media.start("camera");
    assert.equal(f.stopped(), 0);
    assert.equal(f.media.isActive("camera"), true);
    await f.media.toggle("camera");
    assert.equal(f.stopped(), 1);
    assert.equal(f.media.isActive("camera"), false);
});
