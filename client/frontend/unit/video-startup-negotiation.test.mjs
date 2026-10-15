import assert from "node:assert/strict";
import test from "node:test";
import { registerHooks } from "node:module";

registerHooks({ load(url, context, next) {
    if (url.endsWith("/remote-media.js")) return { format: "module", source: "export function reconcileRemoteMedia() {}", shortCircuit: true };
    return next(url, context);
} });
const { negotiateOffer, answerRemoteOffer } = await import("../src/video-negotiation.js");
const { createScreenStartupAttempt } = await import("../src/video-startup-plan.js");
const { rtcStatsEpochNow } = await import("../src/rtc-stats-clock.js");

function mockPerformance(t, value) {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, "performance");
    Object.defineProperty(globalThis, "performance", { configurable: true, value });
    t.after(() => Object.defineProperty(globalThis, "performance", descriptor));
}
const security = "a=ice-ufrag:rem1\r\na=ice-pwd:remotePassword1234567890\r\na=fingerprint:sha-256 " + Array(32).fill("AA").join(":") + "\r\na=setup:passive\r\n";
const answerSDP = "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 102\r\na=mid:2\r\n" + security + "a=recvonly\r\na=rtpmap:102 H264/90000\r\na=fmtp:102 profile-level-id=42001f;packetization-mode=1\r\n";
const localSDP = (sdp, offer = false) => sdp.replace("recvonly", "sendonly").replace("ice-ufrag:rem1", "ice-ufrag:loc1")
    .replace("remotePassword1234567890", "localPassword12345678901").replace(/a=setup:[^\r\n]+/, `a=setup:${offer ? "actpass" : "active"}`);
const remoteOffer = (sdp = answerSDP) => sdp.replace("a=setup:passive", "a=setup:actpass");

function fixture({ native, nativeAnswer, rejectHint = false } = {}) {
    const writes = [], local = [], sent = [], serverAnswers = [];
    const pc = { signalingState: "stable", getSenders: () => [], getTransceivers: () => [], rejectHint,
        remoteDescription: { type: "answer", sdp: answerSDP },
        currentRemoteDescription: { type: "answer", sdp: answerSDP },
        localDescription: { type: "answer", sdp: localSDP(answerSDP) },
        currentLocalDescription: { type: "answer", sdp: localSDP(answerSDP) },
        createOffer: async () => ({ type: "offer", sdp: localSDP(pc.remoteDescription.sdp, true) }),
        createAnswer: async () => ({ type: "answer", sdp: localSDP(pc.remoteDescription.sdp) }),
        setLocalDescription: async value => {
            local.push(value);
            if (value.type === "rollback") {
                pc.localDescription = pc.currentLocalDescription; pc.remoteDescription = pc.currentRemoteDescription;
                pc.signalingState = "stable";
            } else {
                pc.localDescription = value; pc.signalingState = value.type === "offer" ? "have-local-offer" : "stable";
                if (value.type === "answer") { pc.currentLocalDescription = value; pc.currentRemoteDescription = pc.remoteDescription; }
            }
        },
        setRemoteDescription: async value => {
            writes.push(value);
            if (pc.rejectHint && value.sdp.includes("x-google-start-bitrate")) throw Error("unsupported hint");
            pc.remoteDescription = value; pc.signalingState = value.type === "offer" ? "have-remote-offer" : "stable";
            if (value.type === "answer") { pc.currentRemoteDescription = value; pc.currentLocalDescription = pc.localDescription; }
        },
    };
    const state = { pc, serverGeneration: 1, activeTabID: "tab" };
    globalThis.window = { __noxa: { state }, go: { main: { App: {
        WebRTCOfferForTab: async (...args) => { sent.push(args); return await native?.() ?? answerSDP; },
        WebRTCAnswerForTab: async (...args) => { serverAnswers.push(args); await nativeAnswer?.(); },
    } } } };
    const plan = { mid: "2", payloadType: 102, bitrateKbps: 2000, expiresAt: rtcStatsEpochNow() + 1500,
        stillEligible: () => true, stillOwned: () => true };
    return { pc, state, plan, writes, local, sent, serverAnswers,
        run: () => negotiateOffer(pc, 1, undefined, () => true, "tab", plan),
        ordinary: () => negotiateOffer(pc, 1, undefined, () => true, "tab"),
        incoming: sdp => answerRemoteOffer(pc, 1, sdp ?? remoteOffer()) };
}

test("first-video hint changes only the browser answer, not the server offer", async () => {
    const f = fixture(); await f.run();
    assert.equal(f.sent.length, 1);
    assert.equal(f.sent[0][1].includes("x-google-start-bitrate"), false);
    assert.equal(f.writes[0].sdp, answerSDP.replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=2000"));
    assert.equal(f.pc.signalingState, "stable");
});

test("fractional observation ownership uses the stats clock despite wall-clock changes", async t => {
    const origin = 1792020000000;
    let wall = origin + 10;
    mockPerformance(t, { timeOrigin: origin, now: () => 10.75 });
    t.mock.method(Date, "now", () => wall);
    for (const jump of [-3600000, 3600000]) {
        wall = origin + 10;
        const f = fixture({ native: () => { wall += jump; } });
        f.plan.expiresAt = origin + 10.25 + 1500;
        await f.run();
        assert.equal(f.writes.length, 1);
        assert.ok(f.writes[0].sdp.includes("x-google-start-bitrate=2000"));
        assert.equal(f.sent[0][1].includes("x-google-start-bitrate"), false);
    }
});

test("future observations and an unavailable stats clock cannot authorize ownership", async t => {
    await t.test("future", async child => {
        const origin = 1792020000000;
        mockPerformance(child, { timeOrigin: origin, now: () => 10.75 });
        const f = fixture();
        f.plan.expiresAt = origin + 11 + 1500;
        await f.run();
        assert.deepEqual(f.writes, [{ type: "answer", sdp: answerSDP }]);
    });
    await t.test("unavailable", async child => {
        const f = fixture();
        mockPerformance(child, undefined);
        await f.run();
        assert.deepEqual(f.writes, [{ type: "answer", sdp: answerSDP }]);
        assert.equal(f.pc.signalingState, "stable", "clock failure must not block ordinary streaming");
    });
});

test("demand or policy changes while the server answers finish the unmodified round", async () => {
    let eligible = true;
    const f = fixture({ native: () => { eligible = false; } });
    f.plan.stillEligible = () => eligible;
    await f.run();
    assert.deepEqual(f.writes, [{ type: "answer", sdp: answerSDP }]);
    assert.equal(f.local.some(value => value.type === "rollback"), false);
});

test("runtime rejection of an optional hint falls back to the already accepted original answer", async () => {
    const f = fixture({ rejectHint: true }); await f.run();
    assert.equal(f.writes.length, 2);
    assert.deepEqual(f.writes[1], { type: "answer", sdp: answerSDP });
    assert.equal(f.local.some(value => value.type === "rollback"), false);
    assert.equal(f.sent.length, 1);
});

test("a retired server session never receives the old hinted or plain answer", async () => {
    const f = fixture({ native: () => { f.state.serverGeneration++; } });
    await assert.rejects(f.run(), /server session changed/);
    assert.equal(f.writes.length, 0);
    assert.equal(f.local.at(-1).type, "rollback");
});


test("a delayed answer finishes plain without resampling or restoring the attempt budget", async t => {
    const origin = 1792020000000;
    let now = origin + 0.25, reads = 0;
    mockPerformance(t, { timeOrigin: origin, now: () => now - origin });
    // Wall time stays fixed while the actual observation expires in flight.
    t.mock.method(Date, "now", () => origin);
    const f = fixture({ native: () => { now += 1500.25; } });
    f.pc.connectionState = "connected";
    const track = { readyState: "live", enabled: true };
    const parameters = { encodings: [{ active: false, maxBitrate: 8000000 }] };
    const transceiver = { mid: "2", sender: { track, getParameters: () => parameters } };
    const stats = new Map([
        ["v", { type: "outbound-rtp", kind: "video", mid: "2", timestamp: now, codecId: "codec", transportId: "transport" }],
        ["codec", { type: "codec", mimeType: "video/H264", payloadType: 102 }],
        ["transport", { type: "transport", dtlsState: "connected", selectedCandidatePairId: "pair", timestamp: now }],
        ["pair", { type: "candidate-pair", state: "succeeded", timestamp: now, availableOutgoingBitrate: 41600 }],
    ]);
    const attempt = createScreenStartupAttempt({ sample: async () => { reads++; return stats; } });
    const input = { pc: f.pc, transceiver, track, profile: { width: 1920, height: 1080, fps: 60 },
        settings: { width: 1920, height: 1080, frameRate: 60 }, limits: {}, current: () => true, otherVideoActive: () => false };
    Object.assign(f.plan, await attempt(input));
    assert.equal(f.plan.expiresAt, now + 1500);
    await f.run();
    assert.deepEqual(f.writes, [{ type: "answer", sdp: answerSDP }]);
    assert.equal(f.local.some(value => value.type === "rollback"), false);
    assert.equal(f.pc.signalingState, "stable");
    assert.equal(await attempt(input), null);
    assert.equal(reads, 1);
    assert.equal(f.sent.length, 1);
});

test("missing or invalid observation deadlines cannot authorize an optional hint", async () => {
    for (const expiresAt of [undefined, NaN, Infinity, rtcStatsEpochNow() - 1]) {
        const f = fixture(); f.plan.expiresAt = expiresAt;
        await f.run();
        assert.deepEqual(f.writes, [{ type: "answer", sdp: answerSDP }]);
    }
});


test("voice-peer offers and later answers retain one owned codec hint while all server copies stay clean", async () => {
    const f = fixture(); await f.run();
    // Fresh-sample authorization expires and demand becomes paused; ownership remains.
    f.plan.expiresAt = 0; f.plan.stillEligible = () => false;
    await f.incoming();
    await f.ordinary();
    assert.equal(f.writes.length, 3);
    assert.ok(f.writes.every(value => value.sdp.match(/x-google-start-bitrate=2000/g)?.length === 1));
    assert.ok(f.sent.every(args => !args[1].includes("x-google-start-bitrate")));
    assert.ok(f.serverAnswers.every(args => !args[1].includes("x-google-start-bitrate")));
    assert.ok(f.local.filter(value => value.type === "answer").every(value => value.sdp.includes("x-google-start-bitrate=2000")));
});

test("an answer delivery retry retains the committed owned parameters and exactly the same sanitized wire copy", async () => {
    let calls = 0;
    const f = fixture({ nativeAnswer: () => { if (++calls === 1) throw Error("temporary native write"); } });
    await f.run(); await f.incoming();
    assert.equal(f.writes.length, 2, "the accepted remote offer is never reapplied on a native retry");
    assert.equal(f.serverAnswers.length, 2);
    assert.deepEqual(f.serverAnswers[0], f.serverAnswers[1]);
    assert.equal(f.serverAnswers[0][1].includes("x-google-start-bitrate"), false);
    await f.ordinary();
    assert.equal(f.writes.at(-1).sdp.includes("x-google-start-bitrate=2000"), true);
});

test("cap, profile or publication invalidation retires carry but keeps exact echo sanitation", async () => {
    const f = fixture(); let owned = true; f.plan.stillOwned = () => owned;
    await f.run(); owned = false;
    await f.ordinary();
    assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false);
    assert.equal(f.writes.at(-1).sdp, answerSDP);
    owned = true; f.plan.expiresAt = rtcStatsEpochNow() + 1500;
    await f.run(); await f.incoming();
    assert.equal(f.writes.at(-1).sdp.includes("x-google-start-bitrate"), false, "retirement cannot be undone by a new seed object");
});

test("a failed round does not retire the browser's unchanged committed owner", async () => {
    let fail = false;
    const f = fixture({ native: () => { if (fail) throw Error("native unavailable"); } });
    await f.run(); fail = true;
    await assert.rejects(f.ordinary(), /native unavailable/);
    fail = false;
    await f.incoming();
    assert.equal(f.writes.at(-1).sdp.includes("x-google-start-bitrate=2000"), true);
});

test("a rejected carry commits plain SDP and never reintroduces the removed hint", async () => {
    const f = fixture(); await f.run(); f.pc.rejectHint = true;
    await f.incoming();
    assert.equal(f.writes.at(-1).sdp, remoteOffer());
    f.pc.rejectHint = false;
    await f.ordinary(); await f.incoming();
    assert.equal(f.writes.at(-1).sdp.includes("x-google-start-bitrate"), false);
});

test("accepted codec or transport changes retire ownership without touching the new parameters", async () => {
    for (const change of [
        sdp => sdp.replace("profile-level-id=42001f", "profile-level-id=42e01f"),
        sdp => sdp.replaceAll("102", "104"),
        sdp => sdp.replaceAll("H264", "H265"),
        sdp => sdp.replace("ice-ufrag:rem1", "ice-ufrag:rem2"),
        sdp => sdp.replace("AA:AA", "BB:BB"),
    ]) {
        let changed = false;
        const f = fixture({ native: () => changed ? change(answerSDP) : answerSDP });
        await f.run(); changed = true; await f.ordinary();
        assert.equal(f.writes.at(-1).sdp, change(answerSDP));
        changed = false; await f.ordinary();
        assert.equal(f.writes.at(-1).sdp, answerSDP, "restoring a former codec/transport cannot reseed");
    }
});

test("failed changed-codec SRD preserves the old owner after rollback", async () => {
    let changed = false;
    const f = fixture({ native: () => changed ? answerSDP.replace("42001f", "42e01f") : answerSDP });
    await f.run();
    const apply = f.pc.setRemoteDescription;
    f.pc.setRemoteDescription = value => value.sdp.includes("42e01f") ? Promise.reject(Error("invalid new codec")) : apply(value);
    changed = true; await assert.rejects(f.ordinary(), /invalid new codec/);
    changed = false; await f.incoming();
    assert.equal(f.writes.at(-1).sdp.includes("x-google-start-bitrate=2000"), true);
});


test("an owned hint echoed with the original local codec format is sanitized without changing format negotiation", async () => {
    const f = fixture(), create = f.pc.createOffer;
    f.pc.createOffer = async () => {
        const offer = await create();
        offer.sdp = offer.sdp.replace("packetization-mode=1", "packetization-mode=1;level-asymmetry-allowed=1");
        return offer;
    };
    await f.run(); await f.ordinary();
    assert.ok(f.sent.at(-1)[1].includes("level-asymmetry-allowed=1"));
    assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false);
    assert.ok(f.local.at(-1).sdp.includes("x-google-start-bitrate=2000"));
});

test("local ICE restart retires the old transport's hint even if the answer credentials remain unchanged", async () => {
    const f = fixture(); await f.run();
    const create = f.pc.createOffer;
    f.pc.createOffer = async () => {
        const offer = await create();
        offer.sdp = offer.sdp.replace("ice-ufrag:loc1", "ice-ufrag:loc2");
        return offer;
    };
    await f.ordinary();
    assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false);
    assert.equal(f.writes.at(-1).sdp, answerSDP);
    f.pc.createOffer = create;
    await f.ordinary();
    assert.equal(f.writes.at(-1).sdp, answerSDP);
});

test("missing or ambiguous security cannot authorize the initial owned hint", async () => {
    for (const modify of [
        sdp => sdp.replace(/a=fingerprint:[^\r\n]+\r\n/, ""),
        sdp => sdp.replace("a=ice-ufrag:rem1", "a=ice-ufrag:rem1\r\na=ice-ufrag:rem2"),
        sdp => sdp.replace("remotePassword1234567890", "short"),
        sdp => sdp.replace("a=setup:passive", "a=setup:actpass"),
    ]) {
        const changed = modify(answerSDP), f = fixture({ native: () => changed });
        f.pc.currentRemoteDescription = { type: "answer", sdp: changed };
        await f.run();
        assert.equal(f.writes.at(-1).sdp, changed);
    }
});

test("foreign hints are never overwritten or removed from server copies", async () => {
    const foreign = answerSDP.replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=800");
    const f = fixture({ native: () => foreign }); await f.run();
    assert.equal(f.writes.at(-1).sdp, foreign);
    await f.ordinary();
    assert.ok(f.sent.at(-1)[1].includes("x-google-start-bitrate=800"));
});


test("local fmtp changes retire carry and sanitize the browser echo even after retirement", async () => {
    const f = fixture(); await f.run();
    const create = f.pc.createOffer;
    f.pc.createOffer = async () => {
        const offer = await create();
        offer.sdp = offer.sdp.replace("profile-level-id=42001f", "profile-level-id=42e01f");
        return offer;
    };
    await f.ordinary();
    assert.ok(f.sent.at(-1)[1].includes("profile-level-id=42e01f"));
    assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false);
    assert.equal(f.writes.at(-1).sdp, answerSDP, "changed local format cannot carry the hint even when remote format stays unchanged");
    f.pc.createOffer = async () => ({ type: "offer", sdp: localSDP(answerSDP, true)
        .replace("42001f", "42e01f").replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=2000") });
    await f.ordinary();
    assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false, "historical browser echo remains sanitized");
    assert.equal(f.writes.at(-1).sdp, answerSDP);
});

test("accepted foreign same-value hints with a different format supersede historical sanitation provenance", async () => {
    let foreign = false;
    const foreignSDP = answerSDP.replace("42001f", "42e01f").replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=2000");
    const f = fixture({ native: () => foreign ? foreignSDP : answerSDP });
    await f.run(); foreign = true; await f.ordinary();
    assert.equal(f.writes.at(-1).sdp, foreignSDP);
    await f.ordinary();
    assert.ok(f.sent.at(-1)[1].includes("x-google-start-bitrate=2000"), "foreign accepted hints must not be stripped");
});

test("rejected foreign SDP preserves committed ownership and sanitation provenance", async () => {
    let foreign = false;
    const foreignSDP = answerSDP.replace("42001f", "42e01f").replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=800");
    const f = fixture({ native: () => foreign ? foreignSDP : answerSDP }); await f.run();
    const apply = f.pc.setRemoteDescription;
    f.pc.setRemoteDescription = value => value.sdp.includes("42e01f") ? Promise.reject(Error("foreign rejected")) : apply(value);
    foreign = true; await assert.rejects(f.ordinary(), /foreign rejected/);
    foreign = false; await f.ordinary();
    assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false);
    assert.ok(f.writes.at(-1).sdp.includes("x-google-start-bitrate=2000"));
});


test("accepted foreign same-value same-format hints retain their own wire provenance", async () => {
    let foreign = false;
    const foreignSDP = answerSDP.replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=2000");
    const f = fixture({ native: () => foreign ? foreignSDP : answerSDP });
    await f.run(); foreign = true;
    await f.ordinary(); await f.ordinary();
    assert.ok(f.sent.at(-1)[1].includes("x-google-start-bitrate=2000"));
    foreign = false; await f.ordinary(); await f.ordinary();
    assert.equal(f.writes.at(-1).sdp, answerSDP, "foreign ownership cannot later reseed our hint");
});

test("failed remote-offer answer creation or application preserves committed ownership after rollback", async () => {
    for (const failure of ["create", "apply"]) {
        const f = fixture(); await f.run();
        const create = f.pc.createAnswer, apply = f.pc.setLocalDescription;
        if (failure === "create") f.pc.createAnswer = async () => { throw Error("answer creation failed"); };
        else f.pc.setLocalDescription = async value => {
            if (value.type === "answer") throw Error("answer application failed");
            return apply(value);
        };
        await assert.rejects(f.incoming(remoteOffer(answerSDP.replace("42001f", "42e01f"))), /answer .* failed/);
        await f.pc.setLocalDescription({ type: "rollback" });
        f.pc.createAnswer = create; f.pc.setLocalDescription = apply;
        await f.ordinary();
        assert.ok(f.writes.at(-1).sdp.includes("x-google-start-bitrate=2000"));
        assert.equal(f.sent.at(-1)[1].includes("x-google-start-bitrate"), false);
    }
});


test("an accepted server offer with an identical foreign hint preserves the generated answer and later wire copies", async () => {
    const f = fixture(); await f.run();
    const foreign = remoteOffer().replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=2000");
    await f.incoming(foreign);
    assert.ok(f.serverAnswers.at(-1)[1].includes("x-google-start-bitrate=2000"));
    await f.ordinary();
    assert.ok(f.sent.at(-1)[1].includes("x-google-start-bitrate=2000"));
    assert.equal(f.writes.at(-1).sdp, answerSDP);
});

test("a rolled-back foreign pending offer does not erase the old sanitation provenance", async () => {
    const f = fixture(); await f.run();
    const create = f.pc.createAnswer;
    f.pc.createAnswer = async () => { throw Error("answer creation failed"); };
    const foreign = remoteOffer().replace("packetization-mode=1", "packetization-mode=1;x-google-start-bitrate=2000");
    await assert.rejects(f.incoming(foreign), /answer creation failed/);
    await f.pc.setLocalDescription({ type: "rollback" });
    f.pc.createAnswer = create;
    await f.incoming();
    assert.ok(f.writes.at(-1).sdp.includes("x-google-start-bitrate=2000"));
    assert.equal(f.serverAnswers.at(-1)[1].includes("x-google-start-bitrate"), false);
});
