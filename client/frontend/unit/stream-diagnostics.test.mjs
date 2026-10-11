import test from "node:test";
import assert from "node:assert/strict";
import { formatBitrate, summarizeStream } from "../src/connection-stats.js";

test("bandwidth uses decimal bits, handles zero and unavailable measurements", () => {
    assert.equal(formatBitrate(null), "—");
    assert.equal(formatBitrate(0), "0 kbit/s");
    assert.equal(formatBitrate(64000), "64 kbit/s");
    assert.equal(formatBitrate(1500000), "1.50 Mbit/s");
});

test("receiver FPS uses the decoded-frame interval and resets when its SSRC changes", () => {
    const report = (frames, timestamp, ssrc = 1) => new Map([["r", {
        id: "r", type: "inbound-rtp", kind: "video", trackIdentifier: "screen", ssrc,
        framesDecoded: frames, timestamp, bytesReceived: frames * 100, frameWidth: 1920, frameHeight: 1080,
    }]]);
    const first = summarizeStream(report(10, 1000), "screen");
    assert.equal(first.fps, null);
    const second = summarizeStream(report(70, 3000), "screen", first);
    assert.equal(second.fps, 30);
    assert.equal(summarizeStream(report(70, 5000), "screen", second).fps, 0);
    const replacement = summarizeStream(report(100, 5000, 2), "screen", second);
    assert.equal(replacement.fps, null);
    assert.equal(replacement.bitrate, null);
});

test("stream diagnostics isolate the track and reset rates after counter/identity changes", () => {
    const report = (bytes, timestamp, id = "one") => new Map([
        ["codec", { type: "codec", mimeType: "video/VP8" }],
        [id, { id, type: "inbound-rtp", kind: "video", trackIdentifier: "alice|screen", codecId: "codec", bytesReceived: bytes, timestamp, framesDecoded: 10, frameWidth: 1280, frameHeight: 720 }],
        ["other", { id: "other", type: "inbound-rtp", kind: "video", trackIdentifier: "bob|cam", bytesReceived: 9000000, timestamp }],
    ]);
    const first = summarizeStream(report(1000, 1000), "alice|screen");
    assert.equal(first.codec, "VP8");
    assert.equal(first.bitrate, null);
    const second = summarizeStream(report(9000, 2000), "alice|screen", first);
    assert.equal(second.bitrate, 64000);
    assert.equal(second.bytes, 9000);
    assert.equal(summarizeStream(report(9000, 2000, "replacement"), "alice|screen", first).bitrate, null);
    assert.equal(summarizeStream(report(0, 2000), "alice|screen", first).bitrate, null);
    assert.equal(summarizeStream(report(9000, 2000), "missing").codec, null);
    const unlabelled = new Map([["unknown", { type: "inbound-rtp", kind: "video", bytesReceived: 123 }]]);
    assert.equal(summarizeStream(unlabelled, undefined).bytes, null);
    assert.equal(summarizeStream(unlabelled, "").bytes, null);
});

test("stream diagnostics separate received and decoded frames from loss and decode cost", () => {
    const stats = (second) => new Map([["v", {
        id: "v", type: "inbound-rtp", kind: "video", trackIdentifier: "screen", ssrc: 42,
        timestamp: second ? 3000 : 1000, framesReceived: second ? 220 : 100,
        framesDecoded: second ? 211 : 95, framesDropped: second ? 9 : 5,
        packetsReceived: second ? 3000 : 1000, packetsLost: second ? 30 : 10,
        bytesReceived: second ? 4000000 : 2000000,
        totalDecodeTime: second ? 1.58 : 1, jitterBufferDelay: second ? 13.6 : 2,
        jitterBufferEmittedCount: second ? 211 : 95,
        nackCount: second ? 8 : 3, pliCount: second ? 3 : 1,
        decoderImplementation: "test decoder", powerEfficientDecoder: true,
    }]]);
    const first = summarizeStream(stats(false), "screen");
    assert.equal(first.receivedFPS, null);
    assert.equal(first.decodeMS, null);
    assert.equal(first.lossPercent, null);
    const second = summarizeStream(stats(true), "screen", first);
    assert.equal(second.sampleMS, 2000);
    assert.equal(second.receivedFPS, 60);
    assert.equal(second.fps, 58);
    assert.equal(second.droppedFPS, 2);
    assert.ok(Math.abs(second.decodeMS - 5) < 1e-9);
    assert.ok(Math.abs(second.bufferMS - 100) < 1e-9);
    assert.ok(Math.abs(second.lossPercent - 100 * 20 / 2020) < 1e-9);
    assert.equal(second.nacks, 5);
    assert.equal(second.plis, 2);
    assert.equal(second.decoder, "test decoder");
    assert.equal(second.powerEfficientDecoder, true);
});

test("stream detail intervals never bridge a long pause or mix decoder counter resets", () => {
    const stats = (timestamp, frames, ssrc = 1) => new Map([["v", { id: "v", type: "inbound-rtp", kind: "video",
        trackIdentifier: "screen", ssrc, timestamp, framesDecoded: frames, framesReceived: frames,
        totalDecodeTime: frames / 1000, bytesReceived: frames * 100, nackCount: frames, pliCount: frames }]]);
    const first = summarizeStream(stats(1000, 100), "screen");
    for (const next of [stats(62000, 200), stats(2000, 1), stats(2000, 200, 2)]) {
        const summary = summarizeStream(next, "screen", first);
        assert.equal(summary.fps, null);
        assert.equal(summary.decodeMS, null);
        assert.equal(summary.nacks, null);
    }
});

test("copied receiver counters retain RTP identity, units and lifetime without private stats fields", () => {
    const row = { id: "inbound-42", type: "inbound-rtp", kind: "video", trackIdentifier: "screen", ssrc: 42,
        timestamp: 1234.5, framesReceived: 121, framesDecoded: 119, framesDropped: 2, freezeCount: 3,
        totalFreezesDuration: 1.25, bytesReceived: 5000, remoteId: "private", trackTitle: "private", codecId: "private" };
    const summarize = (changes, previous) => summarizeStream(new Map([["row", { ...row, ...changes }],
        ["other", { ...row, id: "other", trackIdentifier: "other-track", freezeCount: 999 }]]), "screen", previous);
    const first = summarize({});
    assert.deepEqual(first.cumulative, [{ stats_id: "inbound-42", ssrc: 42, timestamp_ms: 1234.5,
        frames_received: 121, frames_decoded: 119, frames_dropped: 2, freeze_count: 3, total_freezes_duration_seconds: 1.25 }]);
    assert.equal(first.freezes, null, "initial cumulative counters are available without inventing interval deltas");
    const next = summarize({ timestamp: 4234.5, framesDecoded: 299, freezeCount: 4, totalFreezesDuration: 2 }, first);
    assert.equal(next.freezes, 1);
    assert.equal(next.cumulative[0].freeze_count, 4);
    assert.equal(next.cumulative[0].total_freezes_duration_seconds, 2);
    for (const replacement of [{ id: "replacement" }, { ssrc: 43 }, { framesDecoded: 0, bytesReceived: 0 }]) {
        const reset = summarize({ timestamp: 7234.5, freezeCount: 0, totalFreezesDuration: 0, ...replacement }, next);
        assert.equal(reset.freezes, null, "never bridge replaced RTP stats or reset counters");
        assert.equal(reset.cumulative[0].freeze_count, 0, "a new lifetime retains measured zero");
        assert.equal(reset.cumulative[0].total_freezes_duration_seconds, 0);
    }
});

test("receiver cumulative counters distinguish missing or invalid measurements from zero", () => {
    const row = { id: "v", type: "inbound-rtp", kind: "video", trackIdentifier: "screen" };
    const snapshot = values => summarizeStream(new Map([["v", { ...row, ...values }]]), "screen").cumulative[0];
    const unknown = { stats_id: "v", ssrc: null, timestamp_ms: null, frames_received: null, frames_decoded: null,
        frames_dropped: null, freeze_count: null, total_freezes_duration_seconds: null };
    assert.deepEqual(snapshot({}), unknown);
    for (const value of [null, NaN, Infinity, -1, "0"]) {
        assert.deepEqual(snapshot({ ssrc: value, timestamp: value, framesReceived: value, framesDecoded: value,
            framesDropped: value, freezeCount: value, totalFreezesDuration: value }), unknown);
    }
    const zero = snapshot({ ssrc: 42, timestamp: 0, framesReceived: 0, framesDecoded: 0,
        framesDropped: 0, freezeCount: 0, totalFreezesDuration: 0 });
    assert.equal(zero.timestamp_ms, 0);
    assert.equal(zero.frames_received, 0);
    assert.equal(zero.frames_decoded, 0);
    assert.equal(zero.frames_dropped, 0);
    assert.equal(zero.freeze_count, 0);
    assert.equal(zero.total_freezes_duration_seconds, 0);
    assert.equal(snapshot({ id: "x".repeat(161) }).stats_id, null, "do not truncate IDs into ambiguous identities");
    assert.equal(snapshot({ ssrc: 2 ** 32 }).ssrc, null);
    assert.equal(snapshot({ framesDecoded: 1.5 }).frames_decoded, null);
    assert.equal(snapshot({ freezeCount: Number.MAX_SAFE_INTEGER + 1 }).freeze_count, null);
    assert.deepEqual(summarizeStream(new Map(), "screen").cumulative, []);
});
