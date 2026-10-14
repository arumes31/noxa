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

test("unknown-limit, unsupported, replaced and inactive publications cannot pulse", async () => {
    for (const mode of ["limited", "unknown", "unsupported", "replaced", "inactive", "ended", "scope", "codec"]) {
        const f = fixture();
        if (mode === "unsupported") f.options.capable = async () => false;
        if (mode === "replaced") f.publication.generation = "2";
        if (mode === "inactive") f.inactive();
        if (mode === "ended") f.track.readyState = "ended";
        if (mode === "scope") f.state.myChannelID = 5;
        const recover = createVideoSenderRecovery(f.options);
        for (const at of [0, 5000, 10000, 15000]) {
            f.time(at);
            const changes = mode === "limited" ? { qualityLimitationReason: "other" } :
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

test("changing adapted geometry restarts the sustained-software dwell", async () => {
    const f = fixture(), recover = createVideoSenderRecovery(f.options);
    for (const at of [0, 5000]) { f.time(at); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" })); }
    f.time(10000); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth", frameWidth: 1280, frameHeight: 720 }));
    assert.equal(f.calls.length, 0);
    for (const at of [15000, 20000]) { f.time(at); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" })); }
    assert.equal(f.calls.length, 0);
    f.time(25000); await recover(f.pc, f.stats({ qualityLimitationReason: "bandwidth" }));
    assert.equal(f.calls.length, 1);
});

function withCapture(f, changes = {}, sourceChanges = {}) {
    const stats = f.stats({ mediaSourceId: "source", ...changes });
    const row = stats.get("video");
    stats.set("source", { id: "source", type: "media-source", kind: "video", trackIdentifier: f.track.id,
        timestamp: row.timestamp, frames: row.timestamp * 2, ...sourceChanges });
    return stats;
}

test("previous hardware and progressing bound capture recover zero-output software with existing backoff", async () => {
    const f = fixture();
    f.options.capable = async () => { throw new Error("prior hardware should supply eligibility"); };
    const proofs = [];
    f.options.retry = async (slot, track, current, proof) => { assert.ok(current()); proofs.push(proof); };
    const recover = createVideoSenderRecovery(f.options);
    await recover(f.pc, withCapture(f, { powerEfficientEncoder: true }));
    for (let at = 5000; at <= 45000; at += 5000) {
        f.time(at);
        await recover(f.pc, withCapture(f, { id: "replacement", ssrc: 456, framesEncoded: 0, framesPerSecond: 0 }));
        if (at < 15000) assert.equal(proofs.length, 0);
        if (at >= 15000 && at < 45000) assert.equal(proofs.length, 1);
    }
    assert.equal(proofs.length, 2);
    assert.equal(proofs[0].sourceId, "source");
    assert.equal(proofs[0].width, 1920);
});

test("capture cannot authorize software retries without exact prior hardware and fresh owned input", async () => {
    for (const mode of ["no-hardware", "different-geometry", "different-source", "source-switch", "wrong-track", "stale", "stale-outbound", "future", "missing",
        "idle", "counter-reset", "timestamp-reset", "unknown-encoder", "hardware-stall"]) {
        const f = fixture(), recover = createVideoSenderRecovery(f.options);
        if (mode !== "no-hardware") await recover(f.pc, withCapture(f, { powerEfficientEncoder: true,
            ...(mode === "different-geometry" ? { frameWidth: 1280, frameHeight: 720 } : {}) }));
        for (const at of [5000, 10000, 15000]) {
            f.time(at);
            const source = mode === "wrong-track" ? { trackIdentifier: "other" } : mode === "idle" ? { frames: 20 } :
                mode === "counter-reset" && at >= 10000 ? { frames: at - 10000 } :
                mode === "timestamp-reset" && at >= 10000 ? { timestamp: 1 } : {};
            const stats = withCapture(f, { framesEncoded: 10, framesPerSecond: 0,
                ...(mode === "hardware-stall" ? { powerEfficientEncoder: true } : {}),
                ...(mode === "unknown-encoder" ? { encoderImplementation: "unknown" } : {}) }, source);
            if (mode === "different-source" || (mode === "source-switch" && at >= 10000)) {
                stats.get("video").mediaSourceId = "new-source";
                stats.set("new-source", { ...stats.get("source"), id: "new-source" });
            }
            if (mode === "stale") stats.get("source").timestamp -= 1001;
            if (mode === "stale-outbound") stats.get("video").timestamp = stats.get("source").timestamp = 5001;
            if (mode === "future") stats.get("source").timestamp++;
            if (mode === "missing") stats.delete("source");
            await recover(f.pc, stats);
        }
        assert.equal(f.calls.length, 0, mode);
    }
});

test("capture-only hardware activity cannot refill the sender retry budget", async () => {
    const f = fixture(), recover = createVideoSenderRecovery(f.options);
    await recover(f.pc, withCapture(f, { powerEfficientEncoder: true }));
    for (let at = 5000; at <= 45000; at += 5000) {
        f.time(at); await recover(f.pc, withCapture(f, { framesEncoded: 10 }));
    }
    assert.equal(f.calls.length, 2);
    for (let at = 50000; at <= 115000; at += 5000) {
        f.time(at); await recover(f.pc, withCapture(f, { framesEncoded: 10, powerEfficientEncoder: true }));
    }
    for (let at = 120000; at <= 160000; at += 5000) {
        f.time(at); await recover(f.pc, withCapture(f, { framesEncoded: 10 }));
    }
    assert.equal(f.calls.length, 2, "hardware input without output must not reset the 120-second backoff");
    f.time(165000); await recover(f.pc, withCapture(f, { framesEncoded: 10 }));
    assert.equal(f.calls.length, 3);
});


test("browser-adapted software can retry at its observed supported geometry", async () => {
    for (const qualityLimitationReason of ["none", "cpu", "bandwidth"]) {
        const f = fixture(), recover = createVideoSenderRecovery(f.options);
        for (const at of [0, 5000, 10000]) {
            f.time(at);
            await recover(f.pc, f.stats({ frameWidth: 1280, frameHeight: 720, qualityLimitationReason }));
        }
        assert.equal(f.calls.length, 1, qualityLimitationReason);
    }
});


test("sender recovery supplies the actual peer as scheduler ownership", async () => {
    const f = fixture(); let owner;
    f.options.scheduler = async (_scope, _candidates, _sample, pc) => { owner = pc; };
    await createVideoSenderRecovery(f.options)(f.pc, f.stats());
    assert.equal(owner, f.pc);
});


test("admitted HEVC software encoding uses the same bounded source recovery and rejects other profiles", async () => {
    for (const admitted of [true, false]) {
        const f = fixture();
        f.options.withdraw = async () => { f.calls.push("withdraw"); return true; };
        const recover = createVideoSenderRecovery(f.options);
        for (const at of [0, 5000, 10000, 15000, 20000]) {
            f.time(at); const stats = f.stats({ encoderImplementation: "FFmpeg" });
            Object.assign(stats.get("codec"), { mimeType: "video/H265", clockRate: 90000,
                sdpFmtpLine: `profile-id=${admitted ? 1 : 2};tier-flag=0;level-id=153;tx-mode=SRST` });
            await recover(f.pc, stats);
        }
        assert.equal(f.calls.length, admitted ? 1 : 0);
    }
});
