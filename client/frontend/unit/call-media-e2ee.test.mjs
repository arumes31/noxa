// (#12) End-to-end encrypted private-call media: call key scopes, the call
// roster, transceiver slot binding and the CallMediaE2EE session lifecycle.
import assert from "node:assert/strict";
import { test } from "node:test";
import { CALL_E2EE_MARKER, CallMediaE2EE, announcesCallE2EE, callCodecPreferences, callRoster, callTransceiverSlots } from "../src/call-media-e2ee.js";
import { MediaKeyEngine, buildMediaKeyBody, callScopeKey, inChannel, mediaScopeOf, normalizeScopeKey, parseMediaKeyControl, recipientsOf, senderPid } from "../src/media-keys.js";

const CALL = "8f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77";
const flush = async (n = 30) => { for (let i = 0; i < n; i++) await new Promise(r => setTimeout(r, 0)); };
const fastTimers = { setTimeout: (fn) => setTimeout(fn, 0), clearTimeout, setInterval: () => 0, clearInterval: () => {} };

function call(participants, extra = {}) {
    return { id: CALL, caller: "alice", revision: 1, participants, ...extra };
}
const alice = { unique_id: "alice", client_id: "c-alice", device_id: "d-alice", state: "accepted" };
const bob = { unique_id: "bob", client_id: "c-bob", device_id: "d-bob", state: "accepted" };
const carol = { unique_id: "carol", client_id: "c-carol", device_id: "d-carol", state: "accepted" };

test("call scope keys map to the call-media wire scope; channels keep theirs", () => {
    assert.deepEqual(mediaScopeOf(callScopeKey(CALL)), { type: "call-media", id: CALL });
    assert.deepEqual(mediaScopeOf(7), { type: "channel-media", id: "7" });
    assert.deepEqual(mediaScopeOf("7"), { type: "channel-media", id: "7" });
    for (const bad of [0, -1, 1.5, "", "07", "call:", "call:../x", "call: x", null, undefined, {}]) assert.equal(mediaScopeOf(bad), null, String(bad));
    assert.equal(normalizeScopeKey("12"), 12);
    assert.equal(normalizeScopeKey(callScopeKey(CALL)), `call:${CALL}`);
});

test("call and channel rosters never mix", () => {
    const callEntry = { call_id: CALL, client_id: "c1", unique_id: "u1", media_device: "d1" };
    const channelEntry = { channel_id: 5, client_id: "c2", unique_id: "u2", media_device: "d2" };
    assert.equal(inChannel(callEntry, callScopeKey(CALL)), true);
    assert.equal(inChannel(callEntry, 5), false);
    assert.equal(inChannel(channelEntry, callScopeKey(CALL)), false);
    assert.equal(inChannel(channelEntry, 5), true);
    assert.equal(inChannel({ ...callEntry, channel_id: 5 }, callScopeKey(CALL)), false, "an entry naming both is neither");
    assert.equal(inChannel({ ...channelEntry, call_id: CALL }, 5), false);
    assert.equal(inChannel({ ...callEntry, call_id: "other" }, callScopeKey(CALL)), false);
});

test("the call roster holds accepted participants with their answering client and device", () => {
    const roster = callRoster(call([alice, bob, { unique_id: "dave", state: "ringing" }, { ...carol, state: "left" }, { unique_id: "erin", client_id: "c-erin", state: "accepted" }, alice]));
    assert.deepEqual(roster.map(entry => [entry.unique_id, entry.client_id, entry.media_device, entry.call_id]), [["alice", "c-alice", "d-alice", CALL], ["bob", "c-bob", "d-bob", CALL]]);
    assert.deepEqual(callRoster(call([alice, bob], { ended_at: 5 })), []);
    assert.deepEqual(callRoster(null), []);
    // The roster feeds the engine's recipient choice unchanged.
    const recipients = recipientsOf(roster, { channelID: callScopeKey(CALL), clientID: "c-alice", device: "d-alice" });
    assert.deepEqual(recipients.map(r => r.device), ["d-bob"]);
});

test("key controls carry and require the call scope", () => {
    const keys = { mic: new Uint8Array(32), cam: new Uint8Array(32), screen: new Uint8Array(32), screenaudio: new Uint8Array(32) };
    const body = buildMediaKeyBody({ channelID: callScopeKey(CALL), epoch: 3, nowSeconds: 100, client: "c-alice", session: "s1", kid: 1, members: "m", count: 2, keys });
    assert.deepEqual(body.scope, { type: "call-media", id: CALL });
    assert.equal(parseMediaKeyControl(body, callScopeKey(CALL)).ok, true);
    assert.equal(parseMediaKeyControl(body, callScopeKey("9f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77")).reason, "scope");
    assert.equal(parseMediaKeyControl(body, 5).reason, "scope");
    assert.equal(parseMediaKeyControl({ ...body, scope: { type: "channel-media", id: CALL } }, callScopeKey(CALL)).reason, "scope");
});

function fakeMedia() {
    const keys = new Map();
    return {
        keys,
        async setKey(pid, epoch, kid, key) { keys.set(`${pid}#${epoch}`, [...key]); },
        async activate() {}, async retire() {}, async retireOne() {}, async forget() {},
    };
}

test("a call engine keys the accepted peers and rekeys with a fresh epoch when one leaves", async () => {
    let clock = 1_700_000_000_000;
    const sent = [];
    const engine = new MediaKeyEngine({ media: fakeMedia(), timers: fastTimers, sendTimeoutMs: Infinity, now: () => clock++,
        send: async control => { sent.push(control); return { ok: true }; } });
    engine.setRoster(callRoster(call([alice, bob, carol])));
    engine.start({ channelID: callScopeKey(CALL), clientID: "c-alice", principal: "alice", device: "d-alice" });
    await flush();
    assert.deepEqual(sent.map(c => c.device).sort(), ["d-bob", "d-carol"]);
    assert.ok(sent.every(c => c.body.scope.type === "call-media" && c.body.scope.id === CALL));
    const firstEpoch = sent[0].body.epoch;
    const firstKey = sent[0].body.data.keys.mic;
    sent.length = 0;
    engine.setRoster(callRoster(call([alice, bob, { ...carol, state: "left" }])));
    await flush(80);
    assert.ok(sent.length >= 1, "a removal rekeys");
    assert.deepEqual([...new Set(sent.map(c => c.device))], ["d-bob"], "the departed device never gets the new epoch");
    assert.ok(sent[0].body.epoch > firstEpoch);
    assert.notEqual(sent[0].body.data.keys.mic, firstKey, "a removal starts a fresh, unrelated key");
    engine.stop();
});

test("only VP8, H.264 and repair codecs are offered on call video", () => {
    const codecs = ["video/VP9", "video/H264", "video/rtx", "video/AV1", "video/VP8", "video/red", "video/H265"].map(mimeType => ({ mimeType }));
    assert.deepEqual(callCodecPreferences({ codecs }).map(c => c.mimeType), ["video/VP8", "video/H264", "video/rtx", "video/red"]);
    assert.deepEqual(callCodecPreferences({ codecs: [{ mimeType: "video/AV1" }] }), [], "nothing to prefer leaves the browser's choice (the cipher drops it)");
    assert.deepEqual(callCodecPreferences(null), []);
});

test("data channel state announces call encryption", () => {
    assert.equal(announcesCallE2EE({ camera: false, screen: false, e2ee: CALL_E2EE_MARKER }), true);
    assert.equal(announcesCallE2EE({ camera: false, screen: false }), false);
    assert.equal(announcesCallE2EE({ camera: false, screen: false, e2ee: "noxa-media-e2ee-v0" }), false);
    assert.equal(announcesCallE2EE(null), false);
});

function fakeTransceiver(kind) {
    const prefs = [];
    return { receiver: { track: { kind } }, sender: { track: null }, setCodecPreferences: list => prefs.push(list), prefs };
}

test("transceivers map to the fixed call slot order", () => {
    const pc = { getTransceivers: () => [fakeTransceiver("audio"), fakeTransceiver("video"), fakeTransceiver("video"), fakeTransceiver("audio"), fakeTransceiver("video")] };
    assert.deepEqual(callTransceiverSlots(pc).map(entry => entry.slot), ["mic", "cam", "screen", "screenaudio"], "extra transceivers stay unbound");
});

function fakeSession(log) {
    return options => {
        const s = {
            options, active: false, roster: null, received: [],
            setRoster(roster) { s.roster = roster; log.push(["roster", roster.map(e => e.unique_id)]); },
            async start(scope) { log.push(["start", scope]); s.active = true; },
            receive(control) { s.received.push(control); return { ok: true }; },
            peerConfig: config => ({ ...config, transformed: true }),
            protectPeer: pc => { log.push(["protect"]); return pc; },
            syncPeer: () => log.push(["sync"]),
            endpointID: () => "ep",
            bindSender: async (sender, slot) => log.push(["send", slot]),
            bindReceiver: async (receiver, client, slot) => log.push(["recv", client, slot]),
            releasePeer: async () => log.push(["release"]),
            stop() { s.active = false; log.push(["stop"]); },
        };
        log.push(["session", options.hardwareVideoRequired]);
        return s;
    };
}

const zeroKeys = { mic: new Uint8Array(32), cam: new Uint8Array(32), screen: new Uint8Array(32), screenaudio: new Uint8Array(32) };
function control(id = CALL, epoch = 2) {
    const body = buildMediaKeyBody({ channelID: callScopeKey(id), epoch, nowSeconds: Math.floor(Date.now() / 1000), client: "c-bob", session: "s1", kid: 1, members: "m", count: 2, keys: zeroKeys });
    return { sender: "bob", senderDevice: "d-bob", kind: body.kind, scope: body.scope, epoch: body.epoch, expiresAt: body.expires_at, data: body.data };
}

test("CallMediaE2EE starts one session per call, holds early keys and routes only its own call", async () => {
    const log = [];
    const e2ee = new CallMediaE2EE({ adapter: {}, sessionFactory: fakeSession(log) });
    assert.equal(e2ee.receive({ ...control(), scope: { type: "channel-media", id: "5" } }).reason, "scope");
    assert.equal(e2ee.receive(control()).reason, "pending", "a key before the session starts is held");
    const session = await e2ee.start(call([alice, bob]));
    assert.deepEqual(log.slice(0, 3), [["session", false], ["roster", ["alice", "bob"]], ["start", `call:${CALL}`]]);
    assert.equal(session.received.length, 1, "held keys are drained into the started session");
    assert.equal(await e2ee.start(call([alice, bob, carol])), session, "the same call keeps its session");
    assert.deepEqual(log.at(-1), ["roster", ["alice", "bob", "carol"]]);
    assert.equal(e2ee.receive(control("9f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77")).reason, "scope", "another call's key is refused");
    assert.equal(e2ee.receive(control()).ok, true);
    assert.deepEqual(e2ee.peerConfig({ iceTransportPolicy: "relay" }), { iceTransportPolicy: "relay", transformed: true });
    e2ee.stop();
    assert.equal(log.at(-1)[0], "stop");
    assert.throws(() => e2ee.peerConfig({}), /closed/);
});

test("bindPeer binds senders to my slot keys and receivers to the peer's", async () => {
    const log = [];
    const e2ee = new CallMediaE2EE({ adapter: {}, sessionFactory: fakeSession(log), scope: { RTCRtpReceiver: { getCapabilities: () => ({ codecs: [{ mimeType: "video/VP9" }, { mimeType: "video/VP8" }] }) } } });
    await e2ee.start(call([alice, bob]));
    const transceivers = [fakeTransceiver("audio"), fakeTransceiver("video"), fakeTransceiver("video"), fakeTransceiver("audio")];
    const pc = { getTransceivers: () => transceivers };
    log.length = 0;
    await e2ee.bindPeer(pc, "c-bob");
    assert.deepEqual(log.filter(([op]) => op === "send").map(([, slot]) => slot), ["mic", "cam", "screen", "screenaudio"]);
    assert.deepEqual(log.filter(([op]) => op === "recv").map(([, client, slot]) => `${client}|${slot}`), ["c-bob|mic", "c-bob|cam", "c-bob|screen", "c-bob|screenaudio"]);
    assert.deepEqual(transceivers[1].prefs[0].map(c => c.mimeType), ["video/VP8"], "video transceivers prefer admitted codecs");
    assert.equal(transceivers[0].prefs.length, 0);
    await assert.rejects(e2ee.bindPeer(pc, ""), /no client/);
    assert.equal(senderPid("cam"), "self|cam");
});

test("a failed start leaves no session behind", async () => {
    const e2ee = new CallMediaE2EE({ adapter: {}, sessionFactory: () => ({ setRoster() {}, async start() { throw Object.assign(new Error("no_device"), { name: "MediaSessionError", reason: "no_device" }); }, stop() {} }) });
    await assert.rejects(e2ee.start(call([alice, bob])), /no_device/);
    assert.equal(e2ee.session, null);
    assert.equal(e2ee.receive(control()).reason, "pending");
});

test("releasePeer always returns a promise, also after the session stopped", async () => {
    const log = [];
    const e2ee = new CallMediaE2EE({ adapter: {}, sessionFactory: options => ({ ...fakeSession(log)(options), releasePeer: () => undefined }) });
    await e2ee.start(call([alice, bob]));
    await e2ee.releasePeer({});
    e2ee.stop();
    await e2ee.releasePeer({});
});
