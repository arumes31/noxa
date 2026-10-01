import { test } from "node:test";
import assert from "node:assert/strict";
import { formatBytes, formatDuration, summarizeMedia, summarizeVideoProcessing } from "../src/connection-stats.js";

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
