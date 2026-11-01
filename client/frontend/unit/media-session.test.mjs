import assert from "node:assert/strict";
import { test } from "node:test";
import { MediaSession, MediaSessionError, mediaErrorReason, remoteTrackIDsByMid, trackPid, keyframeTarget } from "../src/media-session.js";
import { buildMediaKeyBody } from "../src/media-keys.js";
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

test("attach-before-answer: a receiver without a transform aborts the negotiation (fail closed)", async () => {
    const session = new MediaSession({ adapter: { status: async () => ({}), send: async () => ({ ok: true }) } });
    const pc = { remoteDescription: { sdp: "" }, getTransceivers: () => [{ mid: "0", receiver: { track: { kind: "audio", readyState: "live" } } }] };
    await assert.rejects(session.syncReceivers(pc), e => e instanceof MediaSessionError && e.reason === "closed");
    const bound = [];
    session.media = { syncPeer() {}, endpointID: () => "", bind: async (r, pid) => bound.push(pid) };
    await assert.rejects(session.syncReceivers(pc), e => e instanceof MediaSessionError && e.reason === "unavailable");
    assert.deepEqual(bound, [], "nothing is bound or answered after an unprotected receiver");
    session.media.endpointID = () => "rx-1";
    await session.syncReceivers(pc);
    assert.deepEqual(bound, [""]);
});

test("keyframe requests name only a video publication of a receiver binding", () => {
    assert.deepEqual(keyframeTarget("c1|cam"), { publisher: "c1", slot: "cam" });
    assert.deepEqual(keyframeTarget("c1|screen"), { publisher: "c1", slot: "screen" });
    for (const pid of ["", "c1", "c1|mic", "c1|screenaudio", "|cam"]) assert.equal(keyframeTarget(pid), null, pid);
});

test("a worker failure during startup keeps the original error after session cleanup", async () => {
 class FailedWorker {postMessage(){queueMicrotask(()=>this.onerror?.());}terminate(){}}
 const scope={Worker:FailedWorker,crypto:{subtle:{}},RTCRtpScriptTransform:function(){}};
 let errors=0;
 const session=new MediaSession({scope,workerURL:"worker-test.js",onError:()=>errors++,adapter:{status:async()=>({supported:true,principal:"u",device:"d",clientID:"c"}),send:async()=>({ok:true})}});
 await assert.rejects(session.start(1),e=>e instanceof MediaSessionError && e.reason==="worker" && !/null/.test(e.message));
 assert.equal(errors,1);assert.equal(session.media,null);
});

test("keys admitted before identity/worker startup install only for the bound roster", async t => {
    let release; const identity = new Promise(resolve => { release = resolve; });
    const messages = [];
    class Worker {
        postMessage(message) { messages.push(message); queueMicrotask(() => this.onmessage?.({ data: { seq: message.seq, ok: true } })); }
        terminate() {}
    }
    const session = new MediaSession({ workerURL: "test-worker", scope: { Worker, crypto, RTCRtpScriptTransform: function () {} },
        adapter: { status: () => identity, send: async () => ({ ok: true }) } });
    t.after(() => session.stop());
    session.setRoster([{ client_id: "startup-peer", unique_id: "startup-peer-user", media_device: "startup-peer-device", channel_id: 5 }]);
    const starting = session.start(5);
    const body = buildMediaKeyBody({ channelID: 5, epoch: Date.now(), nowSeconds: Math.floor(Date.now()/1000), client: "startup-peer", session: "session", kid: 0, members: "digest", count: 2,
        keys: Object.fromEntries(["mic", "cam", "screen", "screenaudio"].map(s => [s, new Uint8Array(32).fill(5)])) });
    const control = { ...body, expiresAt: body.expires_at, sender: "startup-peer-user", senderDevice: "startup-peer-device" };
    assert.equal(session.receive({ ...control, scope: { type: "channel-media", id: "9" } }).reason, "scope");
    assert.equal(session.receive({ ...control, sender: "unbound-user", epoch: control.epoch + 1 }).reason, "pending");
    assert.equal(session.receive(control).reason, "pending");
    assert.equal(messages.length, 0, "no worker or keys before identity lookup");
    release({ supported: true, principal: "startup-self-user", device: "startup-self-device", clientID: "startup-self" });
    await starting;
    for (let i = 0; i < 50 && messages.filter(m => m.op === "key" && m.pid.startsWith("startup-peer|")).length < 4; i++) await new Promise(setImmediate);
    assert.equal(messages.filter(m => m.op === "key" && m.pid.startsWith("startup-peer|")).length, 4);
    assert.ok(messages.filter(m => m.op === "key" && m.pid.startsWith("startup-peer|")).every(m => m.epoch === control.epoch), "startup buffering never bypasses the authenticated roster binding");
    session.stop(); assert.equal(session.receive(control).reason, "inactive");
});
