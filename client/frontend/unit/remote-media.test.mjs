import { test } from "node:test";
import assert from "node:assert/strict";
import { startRemoteMedia, reconcileRemoteMedia } from "../src/remote-media.js";
import { remoteTrackIDs, remoteTrackID } from "../src/media-track-id.js";

const description = (id, direction = "sendonly") => `v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=mid:2\r\na=${direction}\r\na=msid:noxa-stream ${id}\r\n`;
function fixture() {
    const track = Object.assign(new EventTarget(), { id: "browser-generated-id", kind: "video", readyState: "live" });
    const transceiver = { mid: "2", receiver: { track } };
    const pc = { remoteDescription: { sdp: description("alice|screen") }, getTransceivers: () => [transceiver] };
    const changes = [];
    const stop = startRemoteMedia(pc, () => true, (track, id) => changes.push(["add", id, track.id]), (track, id) => changes.push(["remove", id, track.id]));
    return { track, transceiver, pc, changes, stop };
}

test("receiver reuse follows negotiated MSID even when its browser track ID stays unchanged", () => {
    const f = fixture();
    f.pc.ontrack({ track: f.track, transceiver: f.transceiver });
    f.pc.remoteDescription.sdp = description("bob|cam");
    reconcileRemoteMedia(f.pc);
    assert.deepEqual(f.changes, [["add", "alice|screen", "browser-generated-id"], ["remove", "alice|screen", "browser-generated-id"], ["add", "bob|cam", "browser-generated-id"]]);
    reconcileRemoteMedia(f.pc); assert.equal(f.changes.length, 3);
    f.stop();
});

test("inactive negotiated receivers cannot retain a previous publisher or fall back to its track ID", () => {
    const f = fixture(); reconcileRemoteMedia(f.pc);
    f.pc.remoteDescription.sdp = description("alice|screen", "inactive");
    reconcileRemoteMedia(f.pc);
    assert.deepEqual(f.changes.map(row => row.slice(0, 2)), [["add", "alice|screen"], ["remove", "alice|screen"]]);
    f.stop();
});

test("ended events and disposal cannot reattach obsolete bindings", () => {
    const f = fixture(); reconcileRemoteMedia(f.pc);
    f.track.readyState = "ended"; f.track.dispatchEvent(new Event("ended"));
    assert.equal(f.changes.at(-1)[0], "remove");
    const count = f.changes.length;
    f.stop(); reconcileRemoteMedia(f.pc);
    assert.equal(f.changes.length, count);
});

test("negotiated identity excludes rejected, receiving-only and unknown sections", () => {
    for (const direction of ["inactive", "recvonly"]) assert.equal(remoteTrackIDs(description("alice|cam", direction)).get("2"), "");
    assert.equal(remoteTrackIDs(description("alice|cam").replace("video 9", "video 0")).get("2"), "");
    const f = fixture();
    assert.equal(remoteTrackID(f.pc, f.track, { mid: "unknown" }), "");
    assert.equal(remoteTrackID({ remoteDescription: null }, f.track), f.track.id);
    f.stop();
});

test("replacing a track removes its listener and a removed receiver is detached", () => {
    const f = fixture(); reconcileRemoteMedia(f.pc);
    const replacement = Object.assign(new EventTarget(), { id: "replacement", kind: "video", readyState: "live" });
    f.transceiver.receiver.track = replacement;
    reconcileRemoteMedia(f.pc);
    assert.equal(f.changes.at(-1)[2], "replacement");
    const count = f.changes.length;
    f.track.dispatchEvent(new Event("ended"));
    assert.equal(f.changes.length, count);
    f.pc.getTransceivers = () => [];
    reconcileRemoteMedia(f.pc);
    assert.deepEqual(f.changes.at(-1), ["remove", "alice|screen", "replacement"]);
    f.stop();
});

test("a superseded session ignores queued track events and SDP changes", () => {
    const f = fixture(); f.stop();
    const stop = startRemoteMedia(f.pc, () => false, () => assert.fail("stale attachment"), () => {});
    f.pc.ontrack({ track: f.track, transceiver: f.transceiver });
    reconcileRemoteMedia(f.pc);
    stop();
});

test("publisher swaps release all old controls before attaching any new receiver", () => {
    const track = id => Object.assign(new EventTarget(), { id, kind: "audio", readyState: "live" });
    const receivers = [{ mid: "1", receiver: { track: track("first") } }, { mid: "2", receiver: { track: track("second") } }];
    const sdp = (first, second) => description(first).replace("mid:2", "mid:1") + description(second);
    const pc = { remoteDescription: { sdp: sdp("alice|screenaudio", "bob|screenaudio") }, getTransceivers: () => receivers };
    const playback = new Map();
    const stop = startRemoteMedia(pc, () => true, (track, id) => playback.set(id, track.id), (_track, id) => playback.delete(id));
    reconcileRemoteMedia(pc);
    pc.remoteDescription.sdp = sdp("bob|screenaudio", "alice|screenaudio");
    pc.ontrack({ track: receivers[0].receiver.track, transceiver: receivers[0] });
    assert.deepEqual([...playback], [["bob|screenaudio", "first"], ["alice|screenaudio", "second"]]);
    stop(); assert.equal(playback.size, 0);
});
