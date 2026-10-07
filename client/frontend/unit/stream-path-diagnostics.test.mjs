import test from "node:test";
import assert from "node:assert/strict";
import { streamPathMeasurements } from "../src/stream-path-diagnostics.js";
import { summarizeStreamHealth } from "../src/stream-health.js";

const sample = () => ({ publisher_id: "alice", slot: "screen", generation: "9007199254740993", session: "11",
    sender_report: { age_ms: 1000, stale: false, rows: [
        { ssrc: 12, slot: "screen", generation: "9007199254740993", encoded_fps: 12 },
        { ssrc: 13, slot: "screen", generation: "9007199254740993", encoded_fps: 60 },
    ] },
    layers: [ { ssrc: 12, rid: "h", ingress: { age_ms: 500, fps: 12 } }, { ssrc: 13, rid: "f", ingress: { age_ms: 500, fps: 60 } } ],
    forwarding: { source_ssrc: 12, stage: { age_ms: 500, fps: 8 } },
});

test("stream path follows actual forwarded SSRC, not the requested quality", () => {
    const view = streamPathMeasurements(sample(), 0, "high");
    assert.equal(view.sender.encoded_fps, 12);
    assert.equal(view.ingress.fps, 12);
    assert.equal(view.forwarding.fps, 8);
});

test("stale, mismatched and replacement publication measurements stay unavailable", () => {
    const remote = sample();
    assert.equal(streamPathMeasurements(remote, 14500).sender, null);
    assert.equal(streamPathMeasurements(remote, 15000).ingress, null);
    remote.sender_report.rows[0].generation = "9007199254740994";
    assert.equal(streamPathMeasurements(remote).sender, null);
    remote.forwarding.source_ssrc = 99;
    assert.equal(streamPathMeasurements(remote).ingress, null);
    assert.equal(streamPathMeasurements(null).sender, null);
});

const sender = overrides => ({ sample_ms: 2000, encoded_fps: 30, sent_fps: 30, capture_fps: 30,
    encode_ms: 10, cpu_limited_ms: 0, bandwidth_limited_ms: 0, remote_fraction_lost: 0,
    encoding_active: true, ...overrides });
const receiver = overrides => ({ sampleMS: 3000, receivedFPS: 30, fps: 30, droppedFPS: 0,
    decodeMS: 5, lossPercent: 0, freezes: 0, nacks: 0, ...overrides });
const health = options => summarizeStreamHealth({ senderAgeMS: 0, receiverAgeMS: 0, ...options });

test("health requires recent, complete intervals and does not treat missing values as zero", () => {
    assert.equal(health({}).state, "unknown");
    assert.equal(health({ starting: true }).state, "starting");
    for (const age of [null, -1, NaN, 15001]) assert.equal(health({ sender: sender(), senderAgeMS: age }).state, "unknown");
    for (const duration of [null, 500, 15001]) assert.equal(health({ sender: sender({ sample_ms: duration }) }).state, "unknown");
    assert.equal(health({ sender: sender({ cpu_limited_ms: null }) }).state, "unknown");
    assert.equal(health({ receiver: receiver({ lossPercent: null }) }).state, "unknown");
    assert.equal(health({ receiver: receiver(), receiverAgeMS: 15001 }).state, "unknown");
    assert.equal(health({ receiver: receiver({ rows: [{ timestamp: 1000 }, { timestamp: 2000 }] }) }).state, "unknown");
});

test("encoding and network pressure need measured evidence, not just a low rate or a selected target", () => {
    assert.equal(health({ sender: sender({ encoded_fps: 10, sent_fps: 10, cpu_limited_ms: 1500, encode_ms: 80 }) }).state, "encoding");
    assert.equal(health({ sender: sender({ bandwidth_limited_ms: 1500 }) }).state, "network");
    assert.equal(health({ sender: sender({ sent_fps: 1, requested_fps: 60 }) }).state, "unknown");
    assert.equal(health({ sender: sender({ quality_reason: "cpu", cpu_limited_ms: null }) }).state, "unknown");
    assert.equal(health({ sender: sender({ remote_fraction_lost: 0.3, retransmit_percent: null }) }).state, "unknown");
    assert.equal(health({ sender: sender({ remote_fraction_lost: 0.08, retransmit_percent: 10 }) }).state, "network");
});

test("severe low sender output stays visible without inventing a CPU or network cause", () => {
    const collapsed = sender({ capture_fps: 32, encoded_fps: 0.6, sent_fps: 0.6, quality_reason: "none" });
    const result = health({ sender: collapsed, receiver: receiver() });
    assert.equal(result.state, "lowOutput");
    assert.deepEqual(result.measurements.slice(0, 3).map(value => value.value), [32, 0.6, 0.6]);
    assert.equal(health({ sender: { ...collapsed, capture_fps: null } }).state, "unknown");
    assert.equal(health({ sender: { ...collapsed, cpu_limited_ms: 1600 } }).state, "encoding");
    assert.equal(health({ sender: { ...collapsed, bandwidth_limited_ms: 1600 } }).state, "network");
    assert.equal(health({ sender: { ...collapsed, cpu_limited_ms: 1600, encoded_fps: 0, sent_fps: 0, encode_ms: null } }).state, "encoding");
});

test("receiver decoding pressure needs slow decode plus dropped frames and little measured loss", () => {
    assert.equal(health({ receiver: receiver({ fps: 18, droppedFPS: 10, decodeMS: 50 }) }).state, "decoding");
    assert.equal(health({ receiver: receiver({ fps: 18, droppedFPS: 10, decodeMS: 50, lossPercent: null }) }).state, "unknown");
    assert.equal(health({ receiver: receiver({ lossPercent: 8, nacks: 4 }) }).state, "network");
    assert.equal(health({ receiver: receiver({ receivedFPS: 0.5, lossPercent: 50, nacks: 1 }) }).state, "unknown");
});

test("health never subtracts rates from different stream stages or diagnoses static content as failure", () => {
    assert.equal(health({ sender: sender({ sent_fps: 60, encoded_fps: 60 }), receiver: receiver() }).state, "normal");
    assert.equal(health({ sender: sender(), receiver: receiver({ fps: 0, receivedFPS: 0 }) }).state, "unknown");
    assert.equal(health({ sender: sender({ capture_fps: 1, encoded_fps: 1, sent_fps: 1 }), screen: true }).state, "lowActivity");
    assert.equal(health({ sender: sender({ capture_fps: null, encoded_fps: 1, sent_fps: 1 }), screen: true }).state, "unknown");
    assert.equal(health({ waiting: true, sender: sender({ cpu_limited_ms: 1500 }) }).state, "waiting");
    assert.equal(health({ sender: sender({ encoding_active: false }) }).state, "unknown");
});
