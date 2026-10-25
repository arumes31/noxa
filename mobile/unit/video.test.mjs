import test from "node:test";
import assert from "node:assert/strict";

import {
    StageState, DUCKED_VOLUME, callOutcome, callPeer, cameraConstraints, classifyCall, codecCapabilityLine,
    codecOrder, isIOS, receiveCodecPreferences, watchCodecNames, wireQuality,
} from "../src/core/video.js";
import { Calls } from "../src/core/calls.js";
import { Voice } from "../src/core/voice.js";
import { Msg } from "../src/core/frame.js";

const codec = (mimeType, sdpFmtpLine = "") => ({ mimeType, clockRate: 90000, sdpFmtpLine });
const CAPS = [
    codec("video/VP8"), codec("video/rtx", "apt=96"), codec("video/H264", "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"),
    codec("video/VP9", "profile-id=0"), codec("video/AV1"), codec("video/red"), codec("video/ulpfec"),
];
const families = list => list.map(c => c.mimeType.slice(6).toLowerCase());

test("receive codecs: AV1 > VP9 > H.264 > VP8 off iOS, auxiliary codecs last", () => {
    assert.deepEqual(families(receiveCodecPreferences(CAPS, { ios: false })), ["av1", "vp9", "h264", "vp8", "rtx", "red", "ulpfec"]);
});

test("receive codecs: H.264 > VP9 > VP8 on iOS", () => {
    assert.deepEqual(families(receiveCodecPreferences(CAPS, { ios: true })), ["h264", "vp9", "vp8", "av1", "rtx", "red", "ulpfec"]);
    assert.deepEqual(codecOrder(true).slice(0, 3), ["h264", "vp9", "vp8"]);
});

test("receive codecs keep the relative order of equal families and do not drop any", () => {
    const two = [codec("video/H264", "profile-level-id=640c1f"), codec("video/VP8"), codec("video/H264", "profile-level-id=42e01f;packetization-mode=1")];
    const ordered = receiveCodecPreferences(two, { ios: true });
    assert.equal(ordered.length, 3);
    assert.equal(ordered[0], two[0]);
    assert.equal(ordered[1], two[2]);
});

test("watch codec names stay within what the SFU accepts", () => {
    assert.deepEqual(watchCodecNames(CAPS), ["vp8"]); // 42e01f is not the relay's baseline 4200xx
    assert.deepEqual(watchCodecNames([...CAPS, codec("video/H264", "packetization-mode=1;profile-level-id=42001f")]), ["h264", "vp8"]);
    assert.deepEqual(watchCodecNames([codec("video/AV1"), codec("video/VP9")]), ["vp8"]);
});

test("codec capability line and iOS detection", () => {
    assert.equal(codecCapabilityLine(["vp8", "h264", "av1"], false), "AV1 > H264 > VP8");
    assert.equal(codecCapabilityLine([], false), "—");
    assert.equal(isIOS("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)"), true);
    assert.equal(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5), true);
    assert.equal(isIOS("Mozilla/5.0 (Linux; Android 14; Pixel 7)", 5), false);
});

test("wire quality: explicit layers, auto by focus, low bandwidth wins", () => {
    assert.equal(wireQuality("high"), "high");
    assert.equal(wireQuality("medium"), "mid");
    assert.equal(wireQuality("low"), "low");
    assert.equal(wireQuality("auto", { watched: 1 }), "default");
    assert.equal(wireQuality("auto", { watched: 2, focused: true }), "high");
    assert.equal(wireQuality("auto", { watched: 2, focused: false, slot: "cam" }), "mid");
    assert.equal(wireQuality("auto", { watched: 2, focused: false, slot: "screen" }), "high");
    assert.equal(wireQuality("high", { lowBandwidth: true }), "low");
});

test("stage: tap focuses, tap again returns to the grid", () => {
    const stage = new StageState().sync(["a|cam", "b|screen", "c|cam"]);
    assert.equal(stage.layout, "grid");
    assert.equal(stage.focusKey, null);
    stage.tap("b|screen");
    assert.equal(stage.layout, "focus");
    assert.equal(stage.focusKey, "b|screen");
    assert.deepEqual(stage.others, ["a|cam", "c|cam"]);
    stage.tap("b|screen");
    assert.equal(stage.layout, "grid");
    assert.equal(stage.focusKey, null);
});

test("stage: swap exchanges positions and moves focus", () => {
    const stage = new StageState().sync(["a", "b", "c"]);
    stage.tap("a");
    stage.swap("a", "c");
    assert.deepEqual(stage.keys, ["c", "b", "a"]);
    assert.equal(stage.focusKey, "c");
    stage.setLayout("grid").swap("b", "c");
    assert.deepEqual(stage.keys, ["b", "c", "a"]);
    assert.equal(stage.focusKey, null);
});

test("stage: focus layout without an explicit focus uses the first tile; removed focus falls back", () => {
    const stage = new StageState().sync(["a", "b"]).setLayout("focus");
    assert.equal(stage.focusKey, "a");
    stage.sync(["a", "b", "c"]).tap("b");
    stage.sync(["a", "c"]);
    assert.equal(stage.focused, null);
    assert.equal(stage.focusKey, "a");
    // A single stream left needs no focus layout.
    stage.sync(["a"]);
    assert.equal(stage.layout, "grid");
    stage.sync(["a", "d"]);
    assert.deepEqual(stage.keys, ["a", "d"]);
});

test("stage: focused audio at full volume, others ducked; grid all full", () => {
    const stage = new StageState().sync(["a", "b"]);
    assert.equal(stage.volume("a"), 1);
    assert.equal(stage.volume("b"), 1);
    stage.tap("b");
    assert.equal(stage.volume("b"), 1);
    assert.equal(stage.volume("a"), DUCKED_VOLUME);
});

test("camera constraints respect media limits", () => {
    const free = cameraConstraints("environment");
    assert.deepEqual(free.facingMode, { ideal: "environment" });
    assert.equal(free.width.max, undefined);
    const capped = cameraConstraints("user", { video_max_width: 320, video_max_height: 240, video_max_bitrate: 0 });
    assert.equal(capped.width.ideal, 320);
    assert.equal(capped.height.max, 240);
});

// --- Call waiting -------------------------------------------------------------------

const ME = "me";
const call = (id, { caller = "x", mine = "accepted", ended = 0, revision = 1 } = {}) => ({
    id, caller, revision, ended_at: ended, created_at: 100,
    participants: [{ unique_id: caller, state: "accepted" }, ...(caller === ME ? [{ unique_id: "y", state: "accepted" }] : [{ unique_id: ME, state: mine }])],
});

test("call waiting: classification", () => {
    const current = call("one");
    assert.equal(classifyCall(null, call("two", { mine: "ringing" }), ME), "follow");
    assert.equal(classifyCall(current, call("one"), ME), "follow");
    assert.equal(classifyCall(current, call("two", { mine: "ringing" }), ME), "waiting");
    assert.equal(classifyCall(current, call("two", { mine: "declined" }), ME, { id: "two" }), "waiting-ended");
    assert.equal(classifyCall(current, call("two", { mine: "ringing", ended: 5 }), ME, { id: "two" }), "waiting-ended");
    assert.equal(classifyCall(current, call("three", { mine: "missed" }), ME), "ignore");
    // The current call ended: a still-ringing waiting call becomes the call.
    assert.equal(classifyCall(call("one", { ended: 9 }), call("two", { mine: "ringing" }), ME, { id: "two" }), "follow");
});

test("call history fields: peer and outcome", () => {
    assert.equal(callPeer(call("a", { caller: "x" }), ME), "x");
    assert.equal(callPeer(call("a", { caller: ME }), ME), "y");
    assert.equal(callOutcome(call("a"), ME, true), "completed");
    assert.equal(callOutcome(call("a", { mine: "declined" }), ME, false), "declined");
    assert.equal(callOutcome(call("a", { mine: "ringing", ended: 5 }), ME, false), "missed");
    assert.equal(callOutcome(call("a", { caller: ME }), ME, false), "unanswered");
});

function fakeSession(handler) {
    const session = new EventTarget();
    const sent = [];
    session.uniqueID = ME;
    session.conn = { open: true, on: () => () => {}, send: () => {}, request: async (type, payload) => { sent.push({ type, payload }); return handler(type, payload); } };
    return { session, sent };
}

test("call waiting: a second ringing call waits; decline declines only it; it ending clears the banner", async () => {
    const calls = { one: call("one"), two: call("two", { mine: "ringing" }) };
    const { session, sent } = fakeSession((type, payload) => ({ action: payload.action, call: calls[payload.id] }));
    const c = new Calls(session, {});
    c.call = calls.one; // in a call (no media in this test)
    let states = 0;
    c.addEventListener("state", () => states++);
    session.dispatchEvent(new CustomEvent("server-event", { detail: { type: "private_call", data: { id: "two" } } }));
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(c.waiting?.id, "two");
    assert.equal(c.call.id, "one");
    assert.ok(states > 0);
    await c.declineWaiting();
    assert.equal(c.waiting, null);
    assert.deepEqual(sent.at(-1).payload, { action: "decline", id: "two" });

    // A new waiting call that the caller cancels disappears by itself.
    calls.three = call("three", { mine: "ringing" });
    await c.refresh("three");
    assert.equal(c.waiting?.id, "three");
    calls.three = call("three", { mine: "missed", ended: 7, revision: 2 });
    await c.refresh("three");
    assert.equal(c.waiting, null);
    assert.equal(c.call.id, "one");
});

test("call-ended is emitted once with peer, outcome and times", async () => {
    const { session } = fakeSession((type, payload) => ({ action: payload.action, call: call("one", { mine: "declined", ended: 200 }) }));
    const c = new Calls(session, {});
    const ended = [];
    c.addEventListener("call-ended", event => ended.push(event.detail));
    await c.refresh("one");
    await c.refresh("one");
    assert.equal(ended.length, 1);
    assert.deepEqual({ ...ended[0], startedAt: undefined }, { id: "one", peer: "x", conversationID: "", outcome: "declined", startedAt: undefined, endedAt: 200000, video: false });
    assert.equal(ended[0].startedAt, 100000);
});

// --- Receive quality ----------------------------------------------------------------

test("stream quality: scoped VideoQuality after a watch, mapped layers, confirmed by the server", async () => {
    const requests = [];
    const session = new EventTarget();
    session.clientID = "self";
    session.capabilities = new Set(["stream_video_quality_v1"]);
    session.conn = {
        open: true, on: () => () => {}, send: () => {},
        request: async (type, payload) => {
            requests.push({ type, payload });
            if (type === Msg.VideoStreamControl && payload.action === "list") return { action: "list", session: "7", streams: [] };
            if (type === Msg.VideoStreamControl) return { action: "watch", session: "7" };
            return { operation: Msg.VideoQuality, publisher_id: payload.publisher_id, slot: payload.slot, quality: payload.quality };
        },
    };
    const voice = new Voice(session);
    await voice.streams();
    const stream = { publisher_id: "pub", slot: "cam", generation: "3" };
    await voice.watch(stream);
    let quality = requests.filter(r => r.type === Msg.VideoQuality);
    assert.equal(quality.length, 1);
    assert.deepEqual(quality[0].payload, { quality: "default", ack_requested: true, publisher_id: "pub", slot: "cam", generation: "3", session: "7" });
    await voice.setStreamQuality("pub|cam", "medium");
    quality = requests.filter(r => r.type === Msg.VideoQuality);
    assert.equal(quality.at(-1).payload.quality, "mid");
    assert.equal(voice.confirmed.get("pub|cam"), "mid");
    // Unchanged preference sends nothing.
    await voice.applyQualities();
    assert.equal(requests.filter(r => r.type === Msg.VideoQuality).length, 2);
    voice.close();
});

test("stream quality: not sent to servers without the capability", async () => {
    const requests = [];
    const session = new EventTarget();
    session.capabilities = new Set();
    session.conn = { open: true, on: () => () => {}, send: () => {}, request: async (type, payload) => { requests.push(type); return { action: payload.action, session: "7" }; } };
    const voice = new Voice(session);
    await voice.streams();
    await voice.watch({ publisher_id: "pub", slot: "cam", generation: "3" });
    assert.ok(!requests.includes(Msg.VideoQuality));
    voice.close();
});
