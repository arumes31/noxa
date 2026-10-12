import test from "node:test";
import assert from "node:assert/strict";
import { createHardwareRecoveryScheduler, createVideoReceiverRecovery, hardwareVideoDecodeAvailable, resetVideoReceiver } from "../src/video-hardware-recovery.js";

function fixture() {
    let time = 0, watching = true;
    const track = { id: "browser-track", readyState: "live", enabled: true };
    const transceiver = { mid: "2", direction: "recvonly", currentDirection: "recvonly", sender: { track: null }, receiver: { track } };
    const audio = { direction: "sendrecv", currentDirection: "sendrecv", sender: { track: { kind: "audio" } } };
    const pc = { connectionState: "connected", signalingState: "stable", getTransceivers: () => [audio, transceiver],
        remoteDescription: { sdp: "m=video 9 UDP/TLS/RTP/SAVPF 102\r\na=mid:2\r\na=sendonly\r\na=msid:stream publisher|screen\r\n" } };
    const state = { pc, activeTabID: "a", serverGeneration: 1, sessionGeneration: 2, myClientID: "me", myChannelID: 3 };
    const calls = [], watch = (_pc, id, candidate) => watching && id === "publisher|screen" && candidate === track
        ? { key: "publication-1", current: () => watching } : null;
    const scheduler = createHardwareRecoveryScheduler({ now: () => time });
    const options = { state, watch, scheduler, supported: async () => true, capable: async () => true, reset: async () => calls.push("reset") };
    const stats = (changes = {}) => new Map([
        ["codec", { mimeType: "video/H264", sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" }],
        ["video", { id: "video", type: "inbound-rtp", kind: "video", mid: "2", trackIdentifier: track.id, ssrc: 123, codecId: "codec",
            timestamp: time + 1, framesDecoded: time + 10, frameWidth: 1920, frameHeight: 1080, framesPerSecond: 60,
            decoderImplementation: "FFmpegVideoDecoder", powerEfficientDecoder: false, ...changes }],
    ]);
    return { pc, state, track, transceiver, audio, calls, options, stats, time: value => { time = value; }, stop: () => { watching = false; } };
}

test("prolonged supported software decoding retries with a persistent bounded backoff", async () => {
    const f = fixture(), recover = createVideoReceiverRecovery(f.options);
    for (const at of [0, 5000, 10000, 15000, 20000, 25000, 30000, 35000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 1);
    f.time(40000); await recover(f.pc, f.stats()); assert.equal(f.calls.length, 2);
    // Counter/SSRC changes do not refill this publication's retry budget.
    for (let at = 45000; at <= 155000; at += 5000) { f.time(at); await recover(f.pc, f.stats({ ssrc: 456 })); }
    assert.equal(f.calls.length, 2);
    f.time(160000); await recover(f.pc, f.stats({ ssrc: 456 })); assert.equal(f.calls.length, 3);
    for (let at = 165000; at <= 455000; at += 5000) { f.time(at); await recover(f.pc, f.stats({ ssrc: 456 })); }
    assert.equal(f.calls.length, 3);
    f.time(460000); await recover(f.pc, f.stats({ ssrc: 456 })); assert.equal(f.calls.length, 4);
});

test("unsupported, unknown, stale, inactive and unwatched decoders never reset", async () => {
    for (const mode of ["unsupported", "unknown", "stale", "inactive", "stopped", "paused", "unwatched", "disconnected", "wrong-codec"]) {
        const f = fixture();
        if (mode === "unsupported") f.options.capable = async () => false;
        if (mode === "inactive") f.transceiver.currentDirection = "inactive";
        if (mode === "stopped") f.track.readyState = "ended";
        if (mode === "paused") f.track.enabled = false;
        if (mode === "unwatched") f.stop();
        if (mode === "disconnected") f.pc.connectionState = "disconnected";
        const recover = createVideoReceiverRecovery(f.options);
        for (const at of [0, 5000, 10000, 15000, 20000]) {
            f.time(at);
            const stats = f.stats(mode === "unknown" ? { powerEfficientDecoder: undefined } : mode === "stale" ? { timestamp: 1 } : {});
            if (mode === "wrong-codec") stats.get("codec").mimeType = "video/VP8";
            await recover(f.pc, stats);
        }
        assert.deepEqual(f.calls, [], mode);
    }
});

test("actual hardware history permits retry and only sustained recovery resets backoff", async () => {
    const f = fixture(); f.options.capable = async () => assert.fail("hardware history suffices");
    const recover = createVideoReceiverRecovery(f.options);
    await recover(f.pc, f.stats({ decoderImplementation: "D3D11VideoDecoder", powerEfficientDecoder: true }));
    for (const at of [5000, 10000, 15000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 1);
    for (let at = 20000; at <= 80000; at += 5000) {
        f.time(at); await recover(f.pc, f.stats({ decoderImplementation: "D3D11VideoDecoder", powerEfficientDecoder: true }));
    }
    for (const at of [85000, 90000, 95000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 2);
    for (let at = 100000; at <= 125000; at += 5000) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 3, "stable hardware resets the next delay to 30s");
});

test("pending capability checks cannot reset cancelled watches or replacement sessions", async () => {
    for (const cancel of [f => f.stop(), f => { f.state.pc = {}; }, f => { f.state.activeTabID = "b"; }]) {
        const f = fixture(); let finish;
        f.options.capable = () => new Promise(resolve => { finish = resolve; });
        const recover = createVideoReceiverRecovery(f.options);
        await recover(f.pc, f.stats()); f.time(5000); await recover(f.pc, f.stats());
        f.time(10000); const pending = recover(f.pc, f.stats());
        for (let i = 0; i < 20 && !finish; i++) await Promise.resolve();
        cancel(f); finish(true); await pending;
        assert.deepEqual(f.calls, []);
    }
});

test("old servers without receiver recovery support never interrupt software playback", async () => {
    const f = fixture(); delete f.options.supported;
    const recover = createVideoReceiverRecovery(f.options);
    for (const at of [0, 5000, 10000, 15000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.deepEqual(f.calls, []);
});

test("reset rejection consumes the retry budget", async () => {
    const f = fixture(); f.options.reset = async () => { f.calls.push("failed"); throw new Error("negotiation failed"); };
    const recover = createVideoReceiverRecovery(f.options);
    for (let at = 0; at <= 35000; at += 5000) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 1);
    f.time(40000); await recover(f.pc, f.stats()); assert.equal(f.calls.length, 2);
});

test("shared scheduler serializes sending and receiving candidates and bounds failure retries", async () => {
    let at = 0, finish, count = 0;
    const schedule = createHardwareRecoveryScheduler({ now: () => at });
    const candidate = key => ({ key, instance: key, config: "h264-1080", timestamp: at + 1, frames: at + 10,
        software: true, current: () => true, capable: async () => true,
        recover: () => { count++; return new Promise(resolve => { finish = resolve; }); } });
    await schedule("scope", [candidate("receive"), candidate("send")]);
    at = 5000; await schedule("scope", [candidate("receive"), candidate("send")]);
    at = 10000; const pending = schedule("scope", [candidate("receive"), candidate("send")]);
    await Promise.resolve();
    await schedule("scope", [candidate("send")]); assert.equal(count, 1);
    finish(); await pending;
    at = 15000; const second = schedule("scope", [candidate("send")]); await Promise.resolve();
    assert.equal(count, 2); finish(); await second;
});

test("receiver reset requests only its scoped output and waits for a decoded replacement", async () => {
    const f = fixture(), calls = [], replacement = { ...f.transceiver, mid: "3", receiver: { track: { ...f.track, id: "new-track" } } };
    const watched = { id: "publisher|screen", request: { action: "receiver_reset", revision: "7", generation: "9", session: "2", active: true } };
    const result = await resetVideoReceiver({ state: f.state, pc: f.pc, transceiver: f.transceiver, current: () => true, watched }, {
        request: async (tab, control) => {
            calls.push([tab, control]);
            f.pc.getTransceivers = () => [f.audio, f.transceiver, replacement];
            f.pc.remoteDescription.sdp = f.pc.remoteDescription.sdp.replace("a=mid:2", "a=mid:3");
        },
        sample: async () => new Map([["new", { type: "inbound-rtp", kind: "video", mid: "3", framesDecoded: 1 }]]),
    });
    assert.equal(result, true);
    assert.deepEqual(calls, [["a", watched.request]]);
    assert.equal(f.transceiver.direction, "recvonly");
    assert.equal(f.audio.direction, "sendrecv");
});

test("cancelled watches, failed requests and missing replacement frames never resurrect playback", async () => {
    for (const mode of ["cancel", "failure", "timeout"]) {
        const f = fixture(); let current = true, at = 0, count = 0;
        const operation = resetVideoReceiver({ state: f.state, pc: f.pc, transceiver: f.transceiver, current: () => current, watched: { id: "publisher|screen", request: {} } }, {
            request: async () => {
                count++;
                if (mode === "failure") throw new Error("request failed");
                if (mode === "cancel") { current = false; f.track.enabled = false; }
            },
            sample: async () => f.stats(), now: () => at, wait: async ms => { at += ms; },
        });
        if (mode === "failure") await assert.rejects(operation, /request failed/);
        else assert.equal(await operation, false);
        assert.equal(count, 1);
        assert.equal(f.transceiver.direction, "recvonly");
        if (mode === "cancel") assert.equal(f.track.enabled, false);
    }
});

test("nonadvancing frames cannot establish software dwell", async () => {
    const f = fixture(), recover = createVideoReceiverRecovery(f.options);
    for (let at = 0; at <= 30000; at += 5000) { f.time(at); await recover(f.pc, f.stats({ framesDecoded: 12 })); }
    assert.deepEqual(f.calls, []);
});

test("a replaced receiver binding cannot reset the publication's newer decoder", async () => {
    const f = fixture();
    assert.equal(await resetVideoReceiver({ state: f.state, pc: f.pc, transceiver: f.transceiver, current: () => true,
        watched: { bindingCurrent: () => false } }, { request: async () => assert.fail("obsolete binding") }), false);
});

test("one telemetry report cannot trigger sequential sender and receiver resets", async () => {
    let at = 0, count = 0;
    const schedule = createHardwareRecoveryScheduler({ now: () => at });
    const candidate = key => ({ key, instance: key, config: "h264", timestamp: at + 1, frames: at + 10, software: true,
        current: () => true, capable: async () => true, recover: async () => { count++; } });
    for (at = 0; at <= 10000; at += 5000) {
        const report = new Map();
        await schedule("scope", [candidate("sender")], report);
        await schedule("scope", [candidate("receiver")], report);
    }
    assert.equal(count, 1);
});

test("capability check uses the measured codec and geometry and does not invent support", async () => {
    let query;
    const codec = { mimeType: "video/H264", sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" };
    const stat = { frameWidth: 320, frameHeight: 180, framesPerSecond: 57 };
    assert.equal(await hardwareVideoDecodeAvailable(codec, stat, { decodingInfo: async value => { query = value; return { supported: true, powerEfficient: true }; } }), true);
    assert.deepEqual([query.type, query.video.width, query.video.height, query.video.framerate], ["webrtc", 320, 180, 57]);
    assert.equal(await hardwareVideoDecodeAvailable(codec, stat, { decodingInfo: async () => ({ supported: true }) }), false);
    assert.equal(await hardwareVideoDecodeAvailable(codec, stat, { decodingInfo: async () => { throw new Error("unsupported"); } }), false);
});

test("an unresponsive capability bridge releases telemetry and does not duplicate native calls", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const f = fixture(); let calls = 0, finish;
    f.options.supported = () => { calls++; return new Promise(resolve => { finish = resolve; }); };
    const recover = createVideoReceiverRecovery(f.options);
    for (let i = 0; i < 2; i++) {
        const pending = recover(f.pc, f.stats());
        await Promise.resolve(); t.mock.timers.tick(1000); await pending;
    }
    assert.equal(calls, 1);
    f.stop(); finish(true); await Promise.resolve();
    assert.deepEqual(f.calls, []);
});

test("an unresponsive reset bridge releases telemetry without issuing duplicate resets", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const f = fixture(); let count = 0, finish, current = true;
    const context = { state: f.state, pc: f.pc, transceiver: f.transceiver, current: () => current,
        watched: { key: "same-publication", request: {} } };
    const options = { request: () => { count++; return new Promise(resolve => { finish = resolve; }); },
        sample: async () => assert.fail("unacknowledged reset cannot claim new decoder") };
    for (let i = 0; i < 2; i++) {
        const pending = resetVideoReceiver(context, options);
        await Promise.resolve(); t.mock.timers.tick(12000);
        assert.equal(await pending, false);
    }
    assert.equal(count, 1);
    current = false; finish(); await Promise.resolve();
});

test("a hung capability request cannot disable recovery in a new server session", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const f = fixture(); let queries = 0, finishOld;
    f.options.supported = () => ++queries === 1 ? new Promise(resolve => { finishOld = resolve; }) : Promise.resolve(true);
    const recover = createVideoReceiverRecovery(f.options);
    const old = recover(f.pc, f.stats()); await Promise.resolve();
    t.mock.timers.tick(1000); await old;
    f.state.serverGeneration++;
    for (const at of [0, 5000, 10000]) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 1);
    finishOld(true); await Promise.resolve();
    assert.equal(f.calls.length, 1, "old completion cannot initiate recovery");
});


test("known hardware receiver can recover stalled software while complete input advances", async () => {
    const f = fixture();
    f.options.capable = async () => assert.fail("exact hardware history must suffice even with zero decoded FPS");
    const recover = createVideoReceiverRecovery(f.options);
    await recover(f.pc, f.stats({ framesDecoded: 12, framesReceived: 20, packetsReceived: 200,
        powerEfficientDecoder: true, decoderImplementation: "D3D11VideoDecoder" }));
    for (let at = 5000; at <= 40000; at += 5000) {
        f.time(at); await recover(f.pc, f.stats({ framesDecoded: 12, framesPerSecond: 0,
            framesReceived: 20 + at / 5000, packetsReceived: 200 + at / 5000 }));
    }
    assert.equal(f.calls.length, 1);
    f.time(45000); await recover(f.pc, f.stats({ framesDecoded: 12, framesPerSecond: 0, framesReceived: 29, packetsReceived: 209 }));
    assert.equal(f.calls.length, 2, "stalled software keeps the existing 30-second retry budget");
});

test("zero decoded output with retained geometry needs prior hardware and complete incoming frames", async () => {
    for (const priorHardware of [false, true]) {
        const f = fixture(), recover = createVideoReceiverRecovery(f.options);
        if (priorHardware) await recover(f.pc, f.stats({ framesDecoded: 12,
            powerEfficientDecoder: true, decoderImplementation: "D3D11VideoDecoder" }));
        for (const at of [5000, 10000, 15000]) {
            f.time(at); await recover(f.pc, f.stats({ ssrc: 456, framesDecoded: 0, framesPerSecond: 0,
                framesReceived: at / 5000, packetsReceived: at / 5000 }));
        }
        assert.equal(f.calls.length, priorHardware ? 1 : 0);
    }
});

test("stalled software cannot use idle, partial, invalid, replaced or unsupported input evidence", async () => {
    for (const mode of ["packets-only", "frames-only", "missing-frames", "missing-packets", "decreasing-input", "invalid-input",
        "decode-reset", "stale", "long-gap", "geometry", "binding", "no-history", "unknown-decoder", "unwatched", "paused"]) {
        const f = fixture(), recover = createVideoReceiverRecovery(f.options);
        if (mode !== "no-history") await recover(f.pc, f.stats({ framesDecoded: 100, framesReceived: 200, packetsReceived: 300,
            powerEfficientDecoder: true, decoderImplementation: "D3D11VideoDecoder" }));
        if (mode === "unwatched") f.stop();
        if (mode === "paused") f.track.enabled = false;
        for (let i = 1; i <= 5; i++) {
            const at = i * (mode === "long-gap" ? 20000 : 5000);f.time(at);
            const row = { framesDecoded: mode === "decode-reset" ? 100 - i : 100,
                framesReceived: 200 + i, packetsReceived: 300 + i, framesPerSecond: 0 };
            if (mode === "packets-only") row.framesReceived = 200;
            if (mode === "frames-only") row.packetsReceived = 300;
            if (mode === "missing-frames") row.framesReceived = undefined;
            if (mode === "missing-packets") row.packetsReceived = undefined;
            if (mode === "decreasing-input") row.framesReceived = 200 - i;
            if (mode === "invalid-input") row.framesReceived = Number.MAX_SAFE_INTEGER + i;
            if (mode === "stale") row.timestamp = 1;
            if (mode === "geometry") { row.frameWidth = 1280; row.frameHeight = 720; }
            if (mode === "binding") row.ssrc = 456 + i;
            if (mode === "unknown-decoder") row.powerEfficientDecoder = undefined;
            await recover(f.pc, f.stats(row));
        }
        assert.equal(f.calls.length, 0, mode);
    }
});

test("incoming frames cannot reset backoff while hardware decoding itself is stalled", async () => {
    const f = fixture(), recover = createVideoReceiverRecovery(f.options);
    for (let at = 0; at <= 40000; at += 5000) { f.time(at); await recover(f.pc, f.stats()); }
    assert.equal(f.calls.length, 2);
    for (let at = 45000; at <= 110000; at += 5000) {
        f.time(at); await recover(f.pc, f.stats({ framesDecoded: 50000, framesReceived: 60000 + at, packetsReceived: 70000 + at,
            powerEfficientDecoder: true, decoderImplementation: "D3D11VideoDecoder" }));
    }
    for (const at of [115000, 120000, 125000]) {
        f.time(at); await recover(f.pc, f.stats({ framesDecoded: 50000, framesReceived: 60000 + at, packetsReceived: 70000 + at }));
    }
    assert.equal(f.calls.length, 2, "receive activity cannot reset the outstanding 120-second backoff");
});


test("a zero-output receiver without actual geometry cannot reuse earlier hardware support", async () => {
    const f = fixture(), recover = createVideoReceiverRecovery(f.options);
    await recover(f.pc, f.stats({ framesDecoded: 12, powerEfficientDecoder: true, decoderImplementation: "D3D11VideoDecoder" }));
    for (const at of [5000, 10000, 15000, 20000]) {
        f.time(at); await recover(f.pc, f.stats({ ssrc: 456, framesDecoded: 0, framesPerSecond: 0,
            frameWidth: undefined, frameHeight: undefined, framesReceived: at / 5000, packetsReceived: at / 5000 }));
    }
    assert.equal(f.calls.length, 0, "no invented dimensions before a first decoded frame");
});
