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
