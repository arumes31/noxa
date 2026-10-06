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
