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
// Native call shares: Native mode, a stand-in stream, and noXa's picker.
const standIn = { kind: "video", readyState: "live", stop() { this.readyState = "ended"; }, addEventListener() {} };
mock.module(new URL("../src/native-share.js", import.meta.url).href, { namedExports: {
    screenCaptureBackend: () => "native",
    nativeStandInStream: () => ({ getTracks: () => [standIn], getVideoTracks: () => [standIn] }),
} });
mock.module(new URL("../src/display-capture-lease.js", import.meta.url).href, { namedExports: {
    retainDisplayCapture() {}, stopDisplayCapture(stream) { for (const track of stream?.getTracks() || []) track.stop?.(); },
} });
let dialog = null;
mock.module(new URL("../src/video.js", import.meta.url).href, { namedExports: { openCallShareDialog: options => { dialog = options; } } });

function element() {
    return {
        dataset: {}, style: {}, children: [], classList: { toggle() {} }, ownerDocument: {},
        setAttribute() {}, addEventListener() {}, play: async () => {}, pause() {}, remove() {},
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

const { CALL_E2EE_MARKER } = await import("../src/call-media-e2ee.js");
const live = () => ({ kind: "video", readyState: "live", addEventListener() {} });

test("a peer's native screen share comes from its sub-connection and ends with its share state", () => {
    const owner = { uid: "me", panel: element(), peers: new Map(), call: { participants: [{ unique_id: "me", state: "accepted" }] } };
    const media = createCallMedia(owner, { current: () => true, acceptedPeer: () => true, label: id => id, report: error => { throw error; }, render() {} });
    const main = live(), native = live();
    let closed = 0;
    const peer = { mediaTracks: { screen: main }, screenTrack: native, screenSub: { close() { closed++; peer.screenTrack = null; } } };
    owner.peers.set("bob", peer);
    const channel = { label: "call-media", readyState: "open", send() {} };
    media.attachChannel(peer, "bob", channel);
    channel.onmessage({ data: JSON.stringify({ camera: false, screen: true, e2ee: CALL_E2EE_MARKER }) });
    assert.equal(peer.mediaTiles.screen.mediaTrack, native, "the sub-connection's track is shown");
    peer.screenTrack = null; media.updateGrid();
    assert.equal(peer.mediaTiles.screen.mediaTrack, main, "a Browser share on the main slot rebuilds the tile");
    peer.screenTrack = native;
    channel.onmessage({ data: JSON.stringify({ camera: false, screen: false, e2ee: CALL_E2EE_MARKER }) });
    assert.equal(closed, 1, "the share ended: its sub-connection closes");
    assert.equal(peer.mediaTiles.screen, undefined);
});

test("Native mode opens noXa's picker and streams through Go, never the main screen slot", async () => {
    const started = [], stopped = [], updates = [], sent = [];
    globalThis.go = { main: { App: {
        StartNativeCallShareForTab: async (tab, request) => { started.push([tab, request]); },
        StopNativeScreenShareForTab: async (tab, id) => { stopped.push([tab, id]); },
        UpdateNativeCallShareTargetsForTab: async (tab, id, targets) => { updates.push(targets); },
    } } };
    const owner = { uid: "me", tabID: "t1", panel: element(), peers: new Map(), call: { id: "c1", participants: [{ unique_id: "me", state: "accepted" }] } };
    const media = createCallMedia(owner, { current: () => true, acceptedPeer: () => true, label: id => id, report: error => { throw error; }, render() {} });
    const replaced = [];
    const sender = { track: null, replaceTrack: async track => { replaced.push(track); sender.track = track; } };
    const bob = { screenSubSupported: true, mediaSenders: { screen: sender }, mediaChannel: { readyState: "open", send: message => sent.push(JSON.parse(message)) } };
    owner.peers.set("bob", bob);
    dialog = null;
    await media.start("screen");
    assert.ok(dialog, "noXa's picker opened instead of the browser picker");
    assert.equal(media.isActive("screen"), true, "the picker counts as a pending share");
    assert.equal(await dialog.startNative({ source: { monitor: 0, window: "0" }, max_width: 1920, max_height: 1080, fps: 60, bitrate: 6000000, audioMode: "system" }), true);
    assert.equal(started.length, 1);
    assert.equal(started[0][0], "t1");
    assert.deepEqual([started[0][1].call_id, started[0][1].targets, started[0][1].audio], ["c1", ["bob"], "system"]);
    assert.deepEqual(replaced.filter(Boolean), [], "the stand-in never goes on the main connection");
    assert.equal(sent.at(-1).screen, true, "peers are told a screen share is live");
    // A joiner that supports the sub-connection is added to the share.
    owner.peers.set("carol", { screenSubSupported: true });
    media.syncNativeTargets();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(updates, [["bob", "carol"]]);
    await media.toggle("screen");
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(stopped, [["t1", started[0][1].id]], "stopping the share stops Go's capture");
    assert.equal(sent.at(-1).screen, false);
    dialog.onClose();
});

test("an older peer keeps the Browser path", async () => {
    const owner = { uid: "me", tabID: "t1", panel: element(), peers: new Map(), call: { id: "c1", participants: [{ unique_id: "me", state: "accepted" }] } };
    const media = createCallMedia(owner, { current: () => true, acceptedPeer: () => true, label: id => id, report() {}, render() {} });
    owner.peers.set("old", { screenSubSupported: false });
    let browser = 0;
    globalThis.navigator ??= {};
    const previous = globalThis.navigator.mediaDevices;
    Object.defineProperty(globalThis.navigator, "mediaDevices", { configurable: true, value: { getDisplayMedia: async () => { browser++; throw Object.assign(new Error("cancelled"), { name: "NotAllowedError" }); } } });
    dialog = null;
    await media.start("screen");
    Object.defineProperty(globalThis.navigator, "mediaDevices", { configurable: true, value: previous });
    assert.equal(dialog, null);
    assert.equal(browser, 1, "the browser picker opened");
});
