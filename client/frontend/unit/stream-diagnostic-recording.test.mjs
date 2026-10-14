import test from "node:test";
import { summarizeStream } from "../src/connection-stats.js";
import assert from "node:assert/strict";
import { createStreamDiagnosticRecording, createDiagnosticArchive, diagnosticSample, receiverRecordingSample } from "../src/stream-diagnostic-recording.js";

function fixture(options = {}) {
    let time = 100, live = true, pending = null;
    const recorder = createStreamDiagnosticRecording({ direction: "sent", cadenceMS: 2000,
        captureScope: () => () => live, now: () => time, utc: () => 1700000000000 + time,
        schedule: fn => { pending = fn; return 1; }, cancel: () => { pending = null; }, ...options });
    return { recorder, advance: ms => { time += ms; const fn = pending; pending = null; fn?.(); },
        stale: () => { live = false; }, timer: () => pending };
}
const sample = (frames = 10, extra = {}) => diagnosticSample("sent", [{ width: 1920, height: 1080,
    codec: "H264", implementation: "NVIDIA", frames_encoded: frames, bytes_sent: frames * 100,
    power_efficient: true, ...extra }]);

test("manual start accepts only 5/30 minutes and deadline stops even without polls", () => {
    const f = fixture(); assert.equal(f.timer(), null); assert.equal(f.recorder.snapshot(), null);
    for (const duration of [0, 1, 6, 31, Infinity]) assert.equal(f.recorder.start(duration), false);
    assert.equal(f.recorder.start(), true); assert.equal(f.recorder.start(), false);
    f.advance(300000); const r = f.recorder.snapshot();
    assert.equal(r.stop_reason, "duration"); assert.equal(r.sample_count, 0);
    assert.equal(r.trailing_gap_ms, 300000); assert.equal(f.timer(), null);
    assert.equal(r.ended_monotonic_ms, r.deadline_monotonic_ms);
    assert.equal(f.recorder.add(sample(), "epoch"), false);
    assert.equal(f.recorder.start(30), true); f.advance(1800000);
    assert.equal(f.recorder.snapshot().stop_reason, "duration");
});

test("scope watchdog interrupts a hung poll and refuses stale start", () => {
    const f = fixture(); f.recorder.start(); f.stale(); f.advance(1000);
    assert.equal(f.recorder.snapshot().stop_reason, "scope_changed");
    assert.equal(f.recorder.add(sample(), "epoch"), false); assert.equal(f.recorder.start(), false);
});

test("samples and exports are immutable; manual stop preserves record until explicit restart", () => {
    const f = fixture(); f.recorder.start(); const row = sample(); f.recorder.add(row, "epoch");
    row.rows[0].width = 1; const a = f.recorder.snapshot(); a.samples[0].rows[0].width = 2;
    assert.equal(f.recorder.snapshot().samples[0].rows[0].width, 1920);
    f.recorder.stop(); f.advance(1000000);
    assert.equal(f.recorder.snapshot().stop_reason, "manual"); assert.equal(f.recorder.snapshot().sample_count, 1);
    f.recorder.start(); assert.equal(f.recorder.snapshot().sample_count, 0);
});

test("changed epoch and counter reset stop before admitting the incompatible row", () => {
    for (const [key, count, reason] of [["new", 20, "epoch_changed"], ["same", 1, "counter_reset"]]) {
        const f = fixture(); f.recorder.start(); f.recorder.add(sample(), "same"); f.advance(2000);
        assert.equal(f.recorder.add(sample(count), key), false);
        assert.equal(f.recorder.snapshot().stop_reason, reason); assert.equal(f.recorder.snapshot().sample_count, 1);
    }
});

test("sender keyframe counters survive export and missing samples cannot hide their reset", () => {
    for (const field of ["pli_count", "fir_count", "huge_frames_sent", "qp_sum"]) {
        for (const next of [7, 8, 0]) {
            const f = fixture(); f.recorder.start();
            f.recorder.add(sample(10, { [field]: 7 }), "same");
            f.advance(2000); f.recorder.add(sample(20), "same");
            f.advance(2000); const admitted = f.recorder.add(sample(30, { [field]: next }), "same");
            const record = JSON.parse(JSON.stringify(f.recorder.snapshot()));
            assert.equal(record.samples[0].rows[0][field], 7);
            assert.equal(record.samples[1].rows[0][field], null, "unavailable is not zero");
            assert.equal(admitted, next >= 7);
            assert.equal(record.stop_reason, next < 7 ? "counter_reset" : null);
            assert.equal(record.sample_count, next < 7 ? 2 : 3);
            if (admitted) assert.equal(record.samples[2].rows[0][field], next);
        }
        assert.equal(sample(10, { [field]: 0 }).rows[0][field], 0, "genuine zero survives projection");
    }
});

test("resource limits interrupt, never silently truncate or claim duration completion", () => {
    for (const limits of [{ maxSamples: 1 }, { maxBytes: 1 }]) {
        const f = fixture(limits); f.recorder.start(); f.recorder.add(sample(), "epoch"); f.advance(2000);
        f.recorder.add(sample(20), "epoch"); const r = f.recorder.snapshot();
        assert.equal(r.stop_reason, "resource_limit"); assert.equal(r.state, "interrupted");
        assert.ok(r.sample_count <= (limits.maxSamples ?? 1000)); assert.ok(r.sample_bytes <= (limits.maxBytes ?? 4194304));
    }
});

test("failed/missing polls and gaps stay explicit; hardware and geometry changes remain outcomes", () => {
    const f = fixture(); f.recorder.start(); f.recorder.add(sample(), "epoch"); f.advance(7000);
    f.recorder.fail(); f.recorder.add(null, "epoch");
    f.recorder.add(sample(20, { width: 640, height: 360, implementation: "OpenH264", power_efficient: false }), "epoch");
    const r = f.recorder.snapshot(); assert.equal(r.failures, 2); assert.equal(r.gaps, 1); assert.equal(r.largest_gap_ms, 7000);
    assert.equal(r.state, "recording"); assert.equal(r.samples[1].rows[0].implementation, "software");
});

test("projection excludes arbitrary strings, identifiers, network paths and frame arrays", () => {
    const r = diagnosticSample("received", [{ codec: "H264", implementation: "FFmpeg (fallback from: ExternalDecoder (D3D11VideoDecoder))",
        width: NaN, power_efficient: false, address: "secret", stats_id: "secret", label: "secret" }], {
        sample_ms: 1, callback_interval_ms: { count: 2, p95: 20, values: [1,2] }, private: "secret", frames: [1,2] });
    assert.equal(r.rows[0].width, null); assert.equal(r.rows[0].implementation, "software");
    assert.equal(r.presentation.window_ms, 30000); assert.equal(r.presentation.callback_interval_ms.p95, 20);
    assert.doesNotMatch(JSON.stringify(r), /secret|values|address|stats_id/);
    assert.equal(diagnosticSample("sent", Array(9).fill({})), null);
});

test("receiver cumulative freeze duration survives projection with unavailable metrics distinct from zero", () => {
    const r = receiverRecordingSample({ cumulative: [{ frames_decoded: 100, freeze_count: 0, total_freezes_duration_seconds: 0 }],
        codec: "H264", width: 1920, height: 1080, fps: 57, decoder: "ExternalDecoder (D3D11VideoDecoder)", powerEfficientDecoder: true });
    assert.equal(r.rows[0].freeze_count, 0); assert.equal(r.rows[0].total_freezes_duration_seconds, 0);
    assert.equal(r.rows[0].received_fps, null); assert.equal(r.rows[0].implementation, "d3d11");
});


test("actual sender MIME schema is normalized and aggregated receiver rows are unsupported", () => {
    const actualSender = { codec: "video/h264", encoder_implementation: "nvidia", power_efficient: true, width: 1920, height: 1080 };
    const r = diagnosticSample("sent", [{ ...actualSender, implementation: actualSender.encoder_implementation }]);
    assert.equal(r.rows[0].codec, "H264"); assert.equal(r.rows[0].implementation, "nvidia");
    assert.equal(receiverRecordingSample({ cumulative: [{}, {}], frames: 200 }), null);
});


test("missing counters cannot hide a reset and row cardinality cannot silently change", () => {
    for (const count of [1, 20]) {
        const f = fixture(); f.recorder.start(); f.recorder.add(sample(), "same");
        f.advance(2000); f.recorder.add(sample(null, { bytes_sent: null }), "same");
        f.advance(2000); f.recorder.add(sample(count), "same");
        assert.equal(f.recorder.snapshot().stop_reason, count === 1 ? "counter_reset" : null);
    }
    const f = fixture(); f.recorder.start(); f.recorder.add(sample(), "same");
    f.recorder.add(diagnosticSample("sent", [{}, {}]), "same");
    assert.equal(f.recorder.snapshot().stop_reason, "epoch_changed");
});


test("archive survives recorder teardown, expires, and clearing releases scheduled retention", () => {
    let time = 0, id = 0; const timers = new Map();
    const archive = createDiagnosticArchive({ now: () => time, schedule: fn => { timers.set(++id, fn); return id; }, cancel: timer => timers.delete(timer) });
    const token = {}, f = fixture({ onFinish: record => archive.save(token, record) });
    f.recorder.start(); f.recorder.add(sample(), "epoch"); f.recorder.dispose();
    assert.equal(archive.read(token).stop_reason, "scope_changed"); assert.equal(archive.read(token).sample_count, 1);
    const copy = archive.read(token); copy.samples[0].rows[0].width = 1;
    assert.equal(archive.read(token).samples[0].rows[0].width, 1920);
    assert.equal(timers.size, 1); time = 1800000;
    assert.equal(archive.read(token), null); assert.equal(timers.size, 0);
    archive.save(token, copy); archive.clear(); assert.equal(archive.entries().length, 0); assert.equal(timers.size, 0);
});

test("archive evicts oldest at count/byte bounds and rejects active records", () => {
    const archive = createDiagnosticArchive({ maxRecords: 2, maxBytes: 10000, schedule: () => 1, cancel: () => {} });
    const tokens = [{}, {}, {}], f = fixture(); f.recorder.start();
    assert.equal(archive.save(tokens[0], f.recorder.snapshot()), false);
    f.recorder.add(sample(), "epoch"); f.recorder.stop(); const record = f.recorder.snapshot();
    for (const token of tokens) assert.equal(archive.save(token, record), true);
    assert.equal(archive.read(tokens[0]), null); assert.equal(archive.entries().length, 2);
    const tiny = createDiagnosticArchive({ maxBytes: 1 }); assert.equal(tiny.save({}, record), false);
    assert.equal(tiny.entries().length, 0); archive.clear();
});


function receiverSummary(timestamp, counts = {}, previous = null) {
    const track = { id: "receiver-track" }, transceiver = { mid: "screen", currentDirection: "recvonly", receiver: { track } };
    const row = { id: "inbound", type: "inbound-rtp", kind: "video", trackIdentifier: track.id, mid: "screen", ssrc: 42,
        codecId: "h264", transportId: "transport", timestamp, framesDecoded: timestamp, bytesReceived: timestamp * 100,
        frameWidth: 1920, frameHeight: 1080, ...counts };
    const stats = new Map([[row.id, row], ["h264", { mimeType: "video/H264" }]]);
    return summarizeStream(stats, track.id, previous, transceiver);
}

test("receiver repairs survive summarize-to-record export with cumulative and interval semantics distinct", () => {
    const before = receiverSummary(1000, { keyFramesDecoded: 10, retransmittedPacketsReceived: 100,
        packetsReceived: 1000, nackCount: 20, pliCount: 5, firCount: 2 });
    const after = receiverSummary(4000, { keyFramesDecoded: 12, retransmittedPacketsReceived: 108,
        packetsReceived: 1300, nackCount: 28, pliCount: 6, firCount: 3 }, before);
    const settled = receiverSummary(7000, { keyFramesDecoded: 12, retransmittedPacketsReceived: 108,
        packetsReceived: 1600, nackCount: 28, pliCount: 6, firCount: 3 }, after);
    const f = fixture({ direction: "received" }); f.recorder.start();
    for (const value of [before, after, settled]) {
        assert.equal(f.recorder.add(receiverRecordingSample(value), "receiver-epoch"), true);
        f.advance(3000);
    }
    const record = JSON.parse(JSON.stringify(f.recorder.snapshot()));
    const initial = record.samples[0].rows[0], active = record.samples[1].rows[0], idle = record.samples[2].rows[0];
    assert.equal(initial.key_frames_decoded_delta, null, "first interval is unknown");
    assert.deepEqual([active.key_frames_decoded, active.retransmitted_packets_received, active.packets_received,
        active.nack_count, active.pli_count, active.fir_count], [12, 108, 1300, 28, 6, 3]);
    assert.deepEqual([active.key_frames_decoded_delta, active.retransmitted_packets_received_delta,
        active.nacks, active.plis, active.fir_count_delta], [2, 8, 8, 1, 1]);
    assert.deepEqual([idle.key_frames_decoded_delta, idle.retransmitted_packets_received_delta,
        idle.nacks, idle.plis, idle.fir_count_delta], [0, 0, 0, 0, 0]);
    assert.equal(record.state, "recording", "decreasing interval deltas do not end counter lifetime");
});

test("receiver cumulative repair resets are detected across unknown samples without inventing zeros", () => {
    const fields = { keyFramesDecoded: "key_frames_decoded", retransmittedPacketsReceived: "retransmitted_packets_received",
        packetsReceived: "packets_received", nackCount: "nack_count", pliCount: "pli_count", firCount: "fir_count" };
    for (const [input, output] of Object.entries(fields)) {
        for (const next of [0, 7, 8]) {
            const f = fixture({ direction: "received" }); f.recorder.start();
            f.recorder.add(receiverRecordingSample(receiverSummary(1000, { [input]: 7 })), "same");
            f.advance(3000);
            f.recorder.add(receiverRecordingSample(receiverSummary(4000)), "same");
            f.advance(3000);
            const admitted = f.recorder.add(receiverRecordingSample(receiverSummary(7000, { [input]: next })), "same");
            const result = f.recorder.snapshot();
            assert.equal(result.samples[0].rows[0][output], 7, output);
            assert.equal(result.samples[1].rows[0][output], null, output);
            assert.equal(admitted, next >= 7, output);
            assert.equal(result.stop_reason, next < 7 ? "counter_reset" : null, output);
        }
        assert.equal(receiverRecordingSample(receiverSummary(1000, { [input]: 0 })).rows[0][output], 0);
        for (const invalid of [-1, 0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
            assert.equal(receiverRecordingSample(receiverSummary(1000, { [input]: invalid })).rows[0][output], null, output);
        }
    }
});
