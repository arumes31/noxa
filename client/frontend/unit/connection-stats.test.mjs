import { test } from "node:test";
import assert from "node:assert/strict";
import { formatBytes, formatDuration, summarizeMedia, summarizeVideoProcessing } from "../src/connection-stats.js";
import { receiverVideoStat, receiverVideoFlow, summarizeStream } from "../src/connection-stats.js";

test("reused receiver selects camera MID over a larger frozen screen counter in either order", () => {
    const track = { id: "same-track", readyState: "live" };
    const tr = { mid: "cam", currentDirection: "recvonly", receiver: { track } };
    const camera = { id: "camera", type: "inbound-rtp", kind: "video", mid: "cam", trackIdentifier: track.id,
        ssrc: 2, timestamp: 1000, framesDecoded: 14501, bytesReceived: 10000, frameWidth: 640, frameHeight: 360 };
    const retired = { ...camera, id: "screen", mid: "screen", ssrc: 1, framesDecoded: 47131, frameWidth: 960, frameHeight: 540 };
    for (const rows of [[camera, retired], [retired, camera]]) {
        let stats = report(...rows), flow = receiverVideoFlow(null, receiverVideoStat(stats, tr));
        let summary = summarizeStream(stats, track.id, null, tr);
        assert.equal(summary.width, 640);
        assert.equal(summary.frames, 14501);
        for (let i = 1; i <= 4; i++) {
            stats = report(...rows.map(r => r === camera ? { ...r, timestamp: 1000 + 3000 * i, framesDecoded: 14501 + 90 * i, bytesReceived: 10000 + 1000 * i } : r));
            flow = receiverVideoFlow(flow, receiverVideoStat(stats, tr));
            summary = summarizeStream(stats, track.id, summary, tr);
            assert.equal(flow.flowing, true, "retired higher counter must not hide advancing camera");
            assert.equal(summary.fps, 30);
        }
    }
});

test("receiver selection rejects mismatched, ambiguous and inactive bindings without counter heuristics", () => {
    const tr = { mid: "cam", receiver: { track: { id: "track" } } };
    const row = { id: "a", type: "inbound-rtp", kind: "video", trackIdentifier: "track", mid: "cam" };
    assert.equal(receiverVideoStat(report(row), tr), row);
    const missing = { ...row, mid: undefined };
    assert.equal(receiverVideoStat(report(missing), tr), missing);
    for (const rows of [[{ ...row, mid: "old" }], [{ ...row, trackIdentifier: "other" }],
        [row, { ...row, id: "b" }], [row, { ...missing, id: "b" }]]) {
        assert.equal(receiverVideoStat(report(...rows), tr), null);
        assert.equal(summarizeStream(report(...rows), "track", null, tr).frames, null);
    }
    assert.equal(receiverVideoStat(report(row), { ...tr, mid: null }), null);
    assert.equal(receiverVideoStat(report(row), { ...tr, stopped: true }), null);
    assert.equal(receiverVideoStat(report(row), { ...tr, currentDirection: "inactive" }), null);
});

test("camera flow stalls only on fresh same-identity counters and resets for replacement", () => {
    const row = { id: "a", ssrc: 1, mid: "m", trackIdentifier: "track", codecId: "c", transportId: "t", timestamp: 1000, framesDecoded: 20 };
    let flow = receiverVideoFlow(null, row);
    for (let i = 0; i < 5; i++) flow = receiverVideoFlow(flow, row);
    assert.equal(flow.stalls, 0, "cached stats cannot manufacture a stall");
    flow = receiverVideoFlow(flow, { ...row, timestamp: 4000 });
    flow = receiverVideoFlow(flow, { ...row, timestamp: 7000 });
    assert.equal(flow.flowing, false, "real same-stream camera stoppage still hides stale picture");
    for (const change of [{ id: "b" }, { ssrc: 2 }, { mid: "new" }, { codecId: "new" }, { transportId: "new" }, { framesDecoded: 1 }]) {
        assert.equal(receiverVideoFlow(flow, { ...row, timestamp: 10000, ...change }).flowing, true);
    }
    assert.equal(receiverVideoFlow(flow, null), null);
    assert.equal(receiverVideoFlow(flow, row, true), null, "screens and paused tiles stay exempt");
});

test("receiver diagnostics cannot bridge codec, transport or MID replacement", () => {
    const track = { id: "track" }, tr = { mid: "m", receiver: { track } };
    const row = { id: "a", type: "inbound-rtp", kind: "video", trackIdentifier: track.id, mid: "m", ssrc: 1,
        codecId: "codec", transportId: "transport", timestamp: 1000, framesDecoded: 20, bytesReceived: 1000 };
    const before = summarizeStream(report(row), track.id, null, tr);
    for (const change of [{ codecId: "new" }, { transportId: "new" }, { mid: "new" }]) {
        const next = { ...row, timestamp: 4000, framesDecoded: 110, bytesReceived: 2000, ...change };
        const after = summarizeStream(report(next), track.id, before, { ...tr, mid: next.mid });
        assert.equal(after.fps, null);
        assert.equal(after.bitrate, null);
    }
});

const report = (...rows) => new Map(rows.map((r) => [r.id, r]));
test("video processing reports actual local codecs and independent encoder/decoder efficiency", () => {
    const stats = summarizeVideoProcessing(report(
        { id: "vp8", type: "codec", mimeType: "video/VP8" },
        { id: "h264", type: "codec", mimeType: "video/H264" },
        { id: "send", type: "outbound-rtp", kind: "video", framesEncoded: 10, codecId: "h264", encoderImplementation: "ExternalEncoder", powerEfficientEncoder: true },
        { id: "receive", type: "inbound-rtp", kind: "video", framesDecoded: 20, codecId: "vp8", decoderImplementation: "libvpx", powerEfficientDecoder: false },
        { id: "remote", type: "remote-inbound-rtp", kind: "video", framesDecoded: 20, decoderImplementation: "Ignore" },
        { id: "audio", type: "inbound-rtp", kind: "audio", framesDecoded: 20 },
        { id: "idle", type: "outbound-rtp", kind: "video", framesEncoded: 0 },
        { id: "inactive", type: "outbound-rtp", kind: "video", active: false, framesEncoded: 50 },
    ));
    assert.deepEqual(stats, {
        encoders: [{ codec: "H264", implementation: "ExternalEncoder", powerEfficient: true }],
        decoders: [{ codec: "VP8", implementation: "libvpx", powerEfficient: false }],
    });
});
test("missing processor information stays unknown and mixed implementations remain distinct", () => {
    assert.deepEqual(summarizeVideoProcessing(null), { encoders: [], decoders: [] });
    const row = { type: "inbound-rtp", mediaType: "video", framesDecoded: 1, codecId: "absent" };
    const stats = summarizeVideoProcessing(report(
        { ...row, id: "a" }, { ...row, id: "b", decoderImplementation: "D3D11VideoDecoder" },
        { ...row, id: "c", powerEfficientDecoder: "true" },
    ));
    assert.equal(stats.decoders.length, 3);
    assert.deepEqual(stats.decoders[0], { codec: null, implementation: null, powerEfficient: null });
    assert.equal(stats.decoders[1].powerEfficient, null, "implementation name must not invent an efficiency flag");
    assert.equal(stats.decoders[2].powerEfficient, null);
});
test("unavailable statistics stay distinct from measured zero", () => {
    for (const value of [undefined, null, -1, NaN, Infinity]) {
        assert.equal(formatBytes(value), "—");
        assert.equal(formatDuration(value), "—");
    }
    assert.equal(formatBytes(0), "0 B");
    assert.equal(formatBytes(2048), "2.00 KiB");
    assert.equal(formatDuration(90061), "1d 1h 1m 1s");
    assert.equal(summarizeMedia(report()).loss, null);
});
test("aggregates local media streams without doubling remote or transport counters", () => {
    const stats = summarizeMedia(report(
        { id: "a", type: "inbound-rtp", kind: "audio", bytesReceived: 100, packetsReceived: 90, packetsLost: 10, jitter: .01 },
        { id: "b", type: "inbound-rtp", kind: "audio", bytesReceived: 200, packetsReceived: 100, packetsLost: -1, jitter: .02 },
        { id: "c", type: "outbound-rtp", bytesSent: 400, packetsSent: 40 },
        { id: "d", type: "remote-inbound-rtp", bytesReceived: 10000 },
        { id: "e", type: "transport", bytesReceived: 10000 },
    ));
    assert.equal(stats.inBytes, 300);
    assert.equal(stats.outBytes, 400);
    assert.equal(stats.inPackets, 190);
    assert.equal(stats.outPackets, 40);
    assert.equal(stats.loss, 5);
    assert.equal(stats.jitter, 20);
});
test("rates use report timestamps and reset across stream replacement or counter resets", () => {
    const stream = { id: "a", type: "inbound-rtp", bytesReceived: 1000, timestamp: 1000 };
    const first = summarizeMedia(report(stream));
    assert.equal(first.inRate, null);
    assert.equal(summarizeMedia(report({ ...stream, bytesReceived: 3048, timestamp: 3000 }), first).inRate, 1024);
    for (const change of [{ id: "new" }, { bytesReceived: 0 }, { timestamp: 0 }, { timestamp: NaN }, { bytesReceived: undefined }]) {
        assert.equal(summarizeMedia(report({ ...stream, ...change }), first).inRate, null);
    }
    assert.equal(summarizeMedia(report({ ...stream, packetsReceived: 0, packetsLost: 0, kind: "audio" })).loss, null);
});
