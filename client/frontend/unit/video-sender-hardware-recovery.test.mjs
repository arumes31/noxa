import test from "node:test";
import assert from "node:assert/strict";
import { createHardwareRecoveryScheduler } from "../src/video-hardware-recovery.js";
import { createVideoSenderRecovery, hardwareVideoEncodeAvailable } from "../src/video-sender-hardware-recovery.js";

function fixture() {
    let time = 0, active = true;
    const track = { id: "screen-track", readyState: "live", enabled: true, getSettings: () => ({ width: 1920, height: 1080 }) };
    const transceiver = { mid: "2", sender: { track, getParameters: () => ({ encodings: [{ active: true, scaleResolutionDownBy: 1 }] }) } };
    const pc = { connectionState: "connected", signalingState: "stable", getTransceivers: () => [transceiver] };
    const state = { pc, activeTabID: "a", serverGeneration: 1, sessionGeneration: 2, myClientID: "me", myChannelID: 3 };
    const publication = { pc, track, slot: "screen", generation: "1", scope: { tabID: "a", generation: 1, session: 2, clientID: "me", channelID: 3 } };
    const calls = [];
    const options = { state, snapshot: () => [{ publication, generation: "1" }], uploadActive: () => active,
        scheduler: createHardwareRecoveryScheduler({ now: () => time }), capable: async () => true,
        retry: async (slot, source, current) => { assert.equal(current(), true); calls.push([slot, source]); } };
    const stats = (changes = {}) => new Map([
        ["codec", { mimeType: "video/H264", sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" }],
        ["video", { id: "video", type: "outbound-rtp", kind: "video", mid: "2", ssrc: 123, codecId: "codec",
            timestamp: time + 1, framesEncoded: time + 10, frameWidth: 1920, frameHeight: 1080, framesPerSecond: 60,
            encoderImplementation: "OpenH264", powerEfficientEncoder: false, qualityLimitationReason: "none", ...changes }],
    ]);
    return { pc, state, track, transceiver, publication, calls, options, stats, time: value => { time = value; }, inactive: () => { active = false; } };
}

test("supported sustained software encoding retries once per backoff, not per stats poll or SSRC", async () => {
    const f = fixture(), recover = createVideoSenderRecovery(f.options);
    for (let at = 0; at <= 35000; at += 5000) {
        f.time(at);
        await recover(f.pc, f.stats());
        await recover(f.pc, f.stats());
    }
    assert.deepEqual(f.calls, [["screen", f.track]]);
    f.time(40000); await recover(f.pc, f.stats());
    assert.equal(f.calls.length, 2);
    for (let at = 45000; at <= 155000; at += 5000) { f.time(at); await recover(f.pc, f.stats({ ssrc: 456 })); }
    assert.equal(f.calls.length, 2);
});

test("adapted, unknown-limit, unsupported, replaced and inactive publications cannot pulse", async () => {
    for (const mode of ["adapted", "adapted-bandwidth", "limited", "unknown", "unsupported", "replaced", "inactive", "ended", "scope", "codec"]) {
        const f = fixture();
        if (mode === "unsupported") f.options.capable = async () => false;
        if (mode === "replaced") f.publication.generation = "2";
        if (mode === "inactive") f.inactive();
        if (mode === "ended") f.track.readyState = "ended";
        if (mode === "scope") f.state.myChannelID = 5;
        const recover = createVideoSenderRecovery(f.options);
        for (const at of [0, 5000, 10000, 15000]) {
            f.time(at);
            const changes = mode === "adapted" || mode === "adapted-bandwidth" ? { frameWidth: 1280, frameHeight: 720, qualityLimitationReason: mode === "adapted-bandwidth" ? "bandwidth" : "none" } : mode === "limited" ? { qualityLimitationReason: "other" } :
                mode === "unknown" ? { powerEfficientEncoder: undefined } : {};
            const stats = f.stats(changes);
            if (mode === "codec") stats.get("codec").mimeType = "video/VP8";
            await recover(f.pc, stats);
        }
        assert.equal(f.calls.length, 0, mode);
    }
});

test("a capability answer from the previous channel cannot restart its sender", async () => {
    const f = fixture();
    f.options.capable = async () => { f.state.myChannelID = 4; return true; };
    const recover = createVideoSenderRecovery(f.options);
    for (const at of [0, 5000, 10000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 0);
});

test("persistent CPU-limited software encoding remains eligible at unadapted source geometry", async () => {
    const f = fixture(), recover = createVideoSenderRecovery(f.options);
    for (const at of [0, 5000, 10000]) { f.time(at); await recover(f.pc, f.stats({ qualityLimitationReason: "cpu" })); }
    assert.equal(f.calls.length, 1);
});

test("measured hardware is retained as evidence for the same codec and geometry", async () => {
    const f = fixture();
    let queries = 0;
    f.options.capable = async () => { queries++; return false; };
    const recover = createVideoSenderRecovery(f.options);
    await recover(f.pc, f.stats({ powerEfficientEncoder: true, encoderImplementation: "MediaFoundationVideoEncodeAccelerator (NVIDIA H.264 Encoder MFT)" }));
    for (const at of [5000, 10000, 15000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 1);
    assert.equal(queries, 0);
});

test("encoding capability checks require power efficiency and deduplicate identical probes", async () => {
    let calls = 0;
    const capabilities = { encodingInfo: async () => { calls++; return { supported: true, powerEfficient: true }; } };
    const codec = { mimeType: "video/H264", sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" };
    const stat = { frameWidth: 1920, frameHeight: 1080, framesPerSecond: 60 };
    assert.deepEqual(await Promise.all([hardwareVideoEncodeAvailable(codec, stat, capabilities), hardwareVideoEncodeAvailable(codec, stat, capabilities)]), [true, true]);
    assert.equal(calls, 1);
    assert.equal(await hardwareVideoEncodeAvailable(codec, stat, { encodingInfo: async () => ({ supported: true, powerEfficient: false }) }), false);
});

test("timed-out native encoding queries remain coalesced and cannot grow beyond the cache bound", async () => {
    let calls = 0;
    const capabilities = { encodingInfo: () => { calls++; return new Promise(() => {}); } };
    const codec = { mimeType: "video/H264", sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" };
    const stat = { frameWidth: 1920, frameHeight: 1080, framesPerSecond: 60 };
    const first = await Promise.all(Array.from({ length: 20 }, (_, i) => hardwareVideoEncodeAvailable(codec, { ...stat, frameWidth: 1920 + i * 4 }, capabilities)));
    assert.equal(first.every(value => value === false), true);
    assert.equal(calls, 16);
    assert.equal(await hardwareVideoEncodeAvailable(codec, stat, capabilities), false);
    assert.equal(calls, 16, "retry reuses the original unresolved native promise");
});


test("unadapted bandwidth-limited software retries with the existing backoff", async () => {
    const f = fixture(), recover = createVideoSenderRecovery(f.options);
    for (let at = 0; at <= 35000; at += 5000) {
        f.time(at); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" }));
    }
    assert.equal(f.calls.length, 1);
    f.time(40000); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" }));
    assert.equal(f.calls.length, 2);
});

test("bandwidth adaptation interrupts dwell until exact source geometry returns", async () => {
    const f = fixture(), recover = createVideoSenderRecovery(f.options);
    for (const at of [0, 5000]) { f.time(at); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" })); }
    f.time(10000); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth", frameWidth: 1280, frameHeight: 720 }));
    assert.equal(f.calls.length, 0);
    for (const at of [15000, 20000]) { f.time(at); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" })); }
    assert.equal(f.calls.length, 0);
    f.time(25000); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" }));
    assert.equal(f.calls.length, 1);
});
