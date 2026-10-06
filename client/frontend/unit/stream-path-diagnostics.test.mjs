import test from "node:test";
import assert from "node:assert/strict";
import { streamPathMeasurements } from "../src/stream-path-diagnostics.js";

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
