import assert from "node:assert/strict";
import { test } from "node:test";
import { MediaSession, MediaSessionError, mediaErrorReason, remoteTrackIDsByMid, trackPid } from "../src/media-session.js";
import { mediaE2EESupport } from "../src/media-e2ee.js";

test("remote track IDs map to publication pids per MID; inactive sections bind nothing", () => {
    const sdp = ["v=0", "m=audio 9 UDP 111", "a=mid:0", "a=msid:noxa-c1 c1", "m=video 9 UDP 96", "a=mid:1", "a=msid:noxa-c2|screen c2|screen",
        "m=video 0 UDP 96", "a=mid:2", "a=msid:noxa-c3|cam c3|cam", "m=audio 9 UDP 111", "a=mid:3", "a=inactive", "a=msid:noxa-c4 c4"].join("\r\n");
    const ids = remoteTrackIDsByMid(sdp);
    assert.deepEqual([...ids], [["0", "c1"], ["1", "c2|screen"], ["2", ""], ["3", ""]]);
    assert.equal(trackPid("c1"), "c1|mic");
    assert.equal(trackPid("c2|screen"), "c2|screen");
    assert.equal(trackPid(""), "");
});

test("every failure maps to one explicit reason (client and server codes)", () => {
    assert.equal(mediaErrorReason(new MediaSessionError("keyless_guest")), "keyless_guest");
    assert.equal(mediaErrorReason(new Error("media_e2ee_client_outdated: channel voice is end-to-end encrypted; update noXa to join voice")), "client_outdated");
    assert.equal(mediaErrorReason("server_recording_disabled_e2ee: no"), "recording");
    assert.equal(mediaErrorReason(new Error("webrtc offer failed")), "");
});

test("runtimes without encoded transforms are unsupported, never plaintext", () => {
    assert.deepEqual(mediaE2EESupport({ Worker: function () {}, crypto: { subtle: {} } }), { ok: false, reason: "transform" });
    assert.deepEqual(mediaE2EESupport({ crypto: { subtle: {} } }), { ok: false, reason: "worker" });
    assert.equal(mediaE2EESupport({ Worker: function () {}, crypto: { subtle: {} }, RTCRtpScriptTransform: function () {} }).api, "script");
});

test("a session refuses to start without server support, a device or a secure session", async () => {
    for (const [status, reason] of [
        [{ supported: false, reason: "server_outdated" }, "server_outdated"],
        [{ supported: true, reason: "keyless_guest" }, "keyless_guest"],
        [{ supported: true, principal: "u", device: "", clientID: "c" }, "no_device"],
        [{ supported: true, principal: "u", device: "d", clientID: "c", reason: "secure_unavailable" }, "secure_unavailable"],
    ]) {
        const session = new MediaSession({ adapter: { status: async () => status, send: async () => ({ ok: true }) } });
        await assert.rejects(session.start(1), error => error instanceof MediaSessionError && error.reason === reason);
    }
    const session = new MediaSession({ adapter: { status: async () => ({ supported: true, principal: "u", device: "d", clientID: "c" }), send: async () => ({ ok: true }) }, scope: { crypto: { subtle: {} } } });
    await assert.rejects(session.start(1), error => error.reason === "unsupported_runtime");
});
