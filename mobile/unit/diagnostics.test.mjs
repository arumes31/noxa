import assert from "node:assert/strict";
import { test } from "node:test";

import { kbps, measureThroughput, PingMonitor, pingQuality, pingStats, pushSeries, sparklinePoints, videoStats, voiceStats } from "../src/core/diagnostics.js";
import { Msg } from "../src/core/frame.js";

test("ping statistics: mean, jitter, loss", () => {
    const stats = pingStats([10, 20, null, 30, 20]);
    assert.equal(stats.count, 5);
    assert.equal(stats.mean, 20);
    assert.equal(stats.min, 10);
    assert.equal(stats.max, 30);
    // |20-10| + |30-20| + |20-30| over 3 differences
    assert.equal(stats.jitter, 10);
    assert.equal(stats.loss, 0.2);
    assert.equal(stats.last, 20);
    assert.deepEqual(pingStats([]), { count: 0, mean: null, min: null, max: null, jitter: null, loss: 0, last: null });
    assert.equal(pingStats([null, null]).loss, 1);
    assert.equal(pingStats([42]).jitter, 0);
});

test("ping quality buckets", () => {
    assert.equal(pingQuality(40), "good");
    assert.equal(pingQuality(40, 0.05), "fair");
    assert.equal(pingQuality(200), "fair");
    assert.equal(pingQuality(400), "poor");
    assert.equal(pingQuality(50, 0, 100), "poor");
    assert.equal(pingQuality(null), "unknown");
    assert.equal(pingQuality(null, 1), "poor");
});

test("the monitor keeps a window of samples from Msg.Ping round trips", async () => {
    const sent = [];
    const conn = { open: true, request: async (type, payload, reply) => { sent.push([type, reply]); } };
    const monitor = new PingMonitor(() => conn, { interval: 100000, window: 3 });
    for (let i = 0; i < 5; i++) await monitor.tick();
    assert.equal(monitor.samples.length, 3);
    assert.deepEqual(sent[0], [Msg.Ping, Msg.Pong]);
    conn.request = async () => { throw new Error("timeout"); };
    await monitor.tick();
    assert.equal(monitor.samples.at(-1), null);
    assert.ok(monitor.stats.loss > 0);
    const stop = monitor.start();
    stop();
    stop();
});

test("throughput probe fetches until enough bytes arrived", async () => {
    const urls = [];
    let clock = 0;
    const fetchImpl = async url => { urls.push(url); clock += 100; return { ok: true, arrayBuffer: async () => new ArrayBuffer(100 * 1024) }; };
    const result = await measureThroughput(["/app/assets/a.js", "/app/assets/b.js"], { minBytes: 250 * 1024, fetchImpl, now: () => clock, cacheStorage: null });
    assert.equal(result.bytes, 300 * 1024);
    assert.equal(urls.length, 3);
    assert.ok(urls.every(u => /noxa-probe=/.test(u)));
    assert.equal(result.kbps, kbps(300 * 1024, 300));
    await assert.rejects(measureThroughput([], { fetchImpl }));
});

test("voice stats from RTCStatsReport entries", () => {
    const first = [
        { id: "c1", type: "codec", mimeType: "audio/opus" },
        { id: "in", type: "inbound-rtp", kind: "audio", codecId: "c1", jitter: 0.012, packetsLost: 0, packetsReceived: 100, bytesReceived: 10000, timestamp: 1000 },
        { id: "out", type: "outbound-rtp", kind: "audio", codecId: "c1", bytesSent: 8000, timestamp: 1000 },
        { id: "p", type: "candidate-pair", state: "succeeded", nominated: true, currentRoundTripTime: 0.045, timestamp: 1000 },
    ];
    const a = voiceStats(first);
    assert.equal(a.codec, "opus");
    assert.equal(a.rtt, 45);
    assert.equal(a.jitter, 12);
    assert.equal(a.packetLoss, 0);
    assert.equal(a.bitrateIn, null);
    const second = first.map(s => s.id === "in" ? { ...s, packetsLost: 5, packetsReceived: 195, bytesReceived: 14000, timestamp: 2000 }
        : s.id === "out" ? { ...s, bytesSent: 12000, timestamp: 2000 } : s);
    const b = voiceStats(new Map(second.map(s => [s.id, s])), a.snapshot);
    assert.equal(b.packetLoss, 5); // 5 lost of 100 in the interval
    assert.equal(b.bitrateIn, 32); // 4000 bytes in one second
    assert.equal(b.bitrateOut, 32);
});

test("video stats per inbound stream", () => {
    const stats = [
        { id: "v", type: "codec", mimeType: "video/VP8" },
        { id: "s1", type: "inbound-rtp", kind: "video", codecId: "v", frameWidth: 1280, frameHeight: 720, framesPerSecond: 30, freezeCount: 2, bytesReceived: 0, timestamp: 0 },
    ];
    const first = videoStats(stats);
    const { width, height, fps, bitrate, freezes, codec } = first.streams[0];
    assert.deepEqual({ width, height, fps, bitrate, freezes, codec }, { width: 1280, height: 720, fps: 30, bitrate: null, freezes: 2, codec: "VP8" });
    const next = videoStats([stats[0], { ...stats[1], bytesReceived: 125000, timestamp: 1000 }], first.snapshot);
    assert.equal(next.streams[0].bitrate, 1000);
});

test("sparkline series and points", () => {
    const series = [];
    for (let i = 0; i < 70; i++) pushSeries(series, i, 60);
    assert.equal(series.length, 60);
    assert.equal(series[0], 10);
    pushSeries(series, NaN, 60);
    assert.equal(series.at(-1), null);
    assert.equal(sparklinePoints([]), "");
    assert.equal(sparklinePoints([0, 10], 100, 20), "0.0,19.0 100.0,1.0");
});
