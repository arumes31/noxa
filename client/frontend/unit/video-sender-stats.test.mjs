import test from "node:test";
import assert from "node:assert/strict";
import { collectVideoSenders, primaryVideoSender, rememberVideoSenderProfile, videoSenderEncodingSettings, videoSenderSources, videoSenderFrameRateReduced } from "../src/video-sender-stats.js";

const context = { slot: "screen", generation: "42", trackID: "private-track", mid: "2", requestedFPS: 60, settingsFPS: 60 };
const row = changes => ({ id: "out", type: "outbound-rtp", kind: "video", ssrc: 22, rid: "f", mid: "2", mediaSourceId: "source", codecId: "codec",
    timestamp: 1000, framesEncoded: 100, framesSent: 90, packetsSent: 1000, bytesSent: 100000,
    totalEncodeTime: 2, totalPacketSendDelay: 5, frameWidth: 1920, frameHeight: 1080, framesPerSecond: 60,
    retransmittedBytesSent: 1000, keyFramesEncoded: 2, targetBitrate: 8e6,
    qualityLimitationReason: "none", encoderImplementation: "libvpx", powerEfficientEncoder: false, ...changes });
const report = (out, source = {}) => new Map([[out.id, out], ["source", { id: "source", type: "media-source", trackIdentifier: "private-track", timestamp: out.timestamp, frames: 200, ...source }],
    ["codec", { id: "codec", type: "codec", mimeType: "video/VP8" }]]);
const before = report(row(), { frames: 200 });
const after = report(row({ timestamp: 6000, framesEncoded: 160, framesSent: 140, packetsSent: 1500, bytesSent: 1100000,
    totalEncodeTime: 5, totalPacketSendDelay: 10, retransmittedPacketsSent: 12, retransmittedBytesSent: 5000, keyFramesEncoded: 5 }), { frames: 500 });
const collect = (now = after, old = before, source = context, previousSource = context) => collectVideoSenders(now, old, [source], [previousSource])[0];

test("sender stages distinguish requested60, captured60, encoded12, sent10 and browser-reported60", () => {
    const result = collect();
    assert.equal(result.requested_fps, 60); assert.equal(result.settings_fps, 60);
    assert.equal(result.capture_fps, 60); assert.equal(result.encoded_fps, 12); assert.equal(result.sent_fps, 10); assert.equal(result.reported_fps, 60);
    assert.equal(result.sample_ms, 5000); assert.equal(result.bitrate_bps, 1600000);
    assert.equal(result.encode_ms, 50); assert.equal(result.send_delay_ms, 10);
    assert.equal(result.codec, "video/vp8"); assert.equal(result.encoder_implementation, "libvpx"); assert.equal(result.power_efficient, false);
    assert.equal(result.retransmitted_packets, 12); assert.equal(result.retransmitted_bytes, 5000);
    assert.equal(result.target_bitrate_bps, 8e6); assert.equal(result.retransmit_bitrate_bps, 6400); assert.equal(result.retransmit_percent, 0.4);
    assert.equal(result.key_frames, 5); assert.equal(result.key_frames_delta, 3); assert.equal(result.frame_bytes, 16600);
    assert.equal(result.generation, "42"); assert.equal(result.ssrc, 22); assert.equal(result.rid, "f");
    assert.equal(videoSenderFrameRateReduced(result), true);
});

test("first, stale, reset, replacedSSRC and publication samples cannot invent interval rates", () => {
    for (const [now, old, source, oldSource] of [
        [after, null, context, context],
        [report(row({ timestamp: 60001 })), before, context, context],
        [report(row({ timestamp: 1000 })), before, context, context],
        [report(row({ timestamp: 6000, framesEncoded: 1 })), before, context, context],
        [report(row({ timestamp: 6000, ssrc: 23 })), before, context, context],
        [after, before, { ...context, generation: "43" }, context],
        [after, before, { ...context, requestedFPS: 30 }, context],
    ]) {
        const result = collect(now, old, source, oldSource);
        for (const key of ["sample_ms", "capture_fps", "encoded_fps", "sent_fps", "bitrate_bps", "encode_ms", "send_delay_ms", "key_frames_delta", "frame_bytes", "retransmit_bitrate_bps", "retransmit_percent"]) assert.equal(result[key], null, key);
        assert.equal(result.reported_fps, 60, "instantaneous browser field stays separate from intervals");
    }
});

test("missing fields remain unknown while genuine stationary counters report zeroFPS", () => {
    const result = collect(report(row({ timestamp: 6000, framesPerSecond: undefined }), { frames: 200 }));
    assert.equal(result.encoded_fps, 0); assert.equal(result.sent_fps, 0); assert.equal(result.capture_fps, 0);
    assert.equal(result.encode_ms, null); assert.equal(result.send_delay_ms, null); assert.equal(result.reported_fps, null);
    const missing = collect(report(row({ timestamp: 6000, framesSent: undefined, totalEncodeTime: undefined })));
    assert.equal(missing.sent_fps, null); assert.equal(missing.encode_ms, null);
    assert.equal(videoSenderFrameRateReduced(missing), false);
});

test("negative/nonfinite/oversized data and arbitrary implementation strings are not exported", () => {
    const result = collect(report(row({ timestamp: 6000, framesPerSecond: Infinity, frameWidth: 99999, frameHeight: -1,
        encoderImplementation: "Private GPU 192.0.2.3", powerEfficientEncoder: "yes", qualityLimitationReason: "private-reason" })));
    assert.equal(result.reported_fps, null); assert.equal(result.width, null); assert.equal(result.height, null);
    assert.equal(result.encoder_implementation, "unknown"); assert.equal(result.power_efficient, null); assert.equal(result.quality_reason, "unknown");
    assert.doesNotMatch(JSON.stringify(result), /Private|192\.0|private-track|source|mediaSource|device/i);
});

test("only mapped acknowledged video publications emit at most8 sender rows", () => {
    assert.equal(collectVideoSenders(after, before, []).length, 0);
    for (const gen of ["0", "01", "-1", "18446744073709551616", "private"]) assert.equal(collectVideoSenders(after, before, [{ ...context, generation: gen }]).length, 0);
    for (const change of [{ kind: "audio" }, { active: false }, { ssrc: 0 }, { ssrc: 0x100000000 }, { rid: "private" }]) {
        assert.equal(collectVideoSenders(report(row(change)), before, [context]).length, 0);
    }
    const many = new Map(after);
    for (let i = 1; i < 20; i++) many.set(`s${i}`, row({ id: `s${i}`, ssrc: i + 30 }));
    assert.equal(collectVideoSenders(many, before, [context]).length, 8);
});

test("duplicate outbound SSRC rows prefer the latest mapped sample and remain valid telemetry", () => {
    const newer = row({ id: "newer", timestamp: 7000, framesPerSecond: 12 });
    for (const entries of [[...after, [newer.id, newer]], [[newer.id, newer], ...after]]) {
        const result = collectVideoSenders(new Map(entries), before, [context], [context]);
        assert.equal(result.length, 1); assert.equal(result[0].reported_fps, 12);
        assert.equal(result[0].sample_ms, null, "a new stats identity needs a fresh baseline");
    }
});

test("source frame measurement requires its own matching counter baseline", () => {
    assert.equal(collect(report(row({ timestamp: 6000 }), { frames: 100 })).capture_fps, null);
    assert.equal(collect(report(row({ timestamp: 6000 }), { frames: undefined })).capture_fps, null);
    assert.equal(collect(report(row({ timestamp: 6000 }), { frames: 500, trackIdentifier: "replacement" })), undefined,
        "a reusedMID must not assign the old source to a replacement capture");
});

test("retry and keyframe accounting rejects missing, reset and inconsistent subsets", () => {
    for (const change of [{ retransmittedBytesSent: undefined }, { retransmittedBytesSent: 999 }, { retransmittedBytesSent: 2000000 },
        { retransmittedBytesSent: 1050000 }, { retransmittedBytesSent: NaN }]) {
        const result = collect(report({ ...after.get("out"), ...change }));
        for (const key of ["retransmit_bitrate_bps", "retransmit_percent", "frame_bytes"]) assert.equal(result[key], null, key);
        assert.equal(result.bitrate_bps, 1600000, "total payload remains measured; retries must not be added again");
    }
    for (const value of [undefined, -1, 1, 161, Infinity]) assert.equal(collect(report({ ...after.get("out"), keyFramesEncoded: value })).key_frames_delta, null);
    assert.equal(collect(report({ ...after.get("out"), targetBitrate: 1e10 })).target_bitrate_bps, null);
    const quiet = collect(report(row({ timestamp: 6000 })));
    assert.equal(quiet.retransmit_bitrate_bps, 0); assert.equal(quiet.retransmit_percent, null); assert.equal(quiet.frame_bytes, null); assert.equal(quiet.key_frames_delta, 0);
});

test("primary sender is full resolution, independent of iteration order, warning avoids guessed causes", () => {
    const full = collect(), low = { ...full, width: 480, height: 270, rid: "q", sent_fps: 60 };
    assert.equal(primaryVideoSender([low, full]), full); assert.equal(primaryVideoSender([full, low]), full);
    assert.equal(primaryVideoSender([]), null);
    assert.equal(videoSenderFrameRateReduced({ ...full, sent_fps: 58 }), false);
    assert.equal(videoSenderFrameRateReduced({ ...full, sample_ms: 200 }), false);
    assert.equal(videoSenderFrameRateReduced({ ...full, requested_fps: null }), false);
});

test("runtime source metadata is restricted to current acknowledged capture and desiredFPS", () => {
    const track = { id: "private-track", readyState: "live", label: "private-window", getSettings: () => ({ frameRate: 30, deviceId: "secret-device" }) };
    rememberVideoSenderProfile(track, { fps: 60, title: "private-window" });
    const pc = { getTransceivers: () => [{ mid: "2", sender: { track } }] };
    const state = { pc, activeTabID: "a", serverGeneration: 3, sessionGeneration: 4 };
    const entry = { generation: "42", publication: { pc, track, slot: "screen", generation: "42", scope: { tabID: "a", generation: 3, session: 4 } } };
    assert.deepEqual(videoSenderSources(state, [entry]), [{ ...context, settingsFPS: 30 }]);
    pc.getTransceivers = () => [{ mid: "2", sender: { track, getParameters: () => ({ encodings: [{ rid: "f", active: true, maxBitrate: 50000000 }] }) } }];
    assert.deepEqual(videoSenderSources(state, [entry])[0].encodings, [{ rid: "f", active: true, maxBitrate: 50000000 }]);
    for (const other of [{ ...state, activeTabID: "b" }, { ...state, pc: {} }, { ...state, sessionGeneration: 5 }]) assert.deepEqual(videoSenderSources(other, [entry]), []);
    track.readyState = "ended"; assert.deepEqual(videoSenderSources(state, [entry]), []);
    track.readyState = "live"; entry.publication.generation = "43"; assert.deepEqual(videoSenderSources(state, [entry]), []);
});

const feedbackReport = (changes = {}) => {
    const result = report({ ...after.get("out"), transportId: "transport", remoteId: "remote", qualityLimitationDurations: { bandwidth: 14, cpu: 2 }, ...changes });
    result.set("transport", { id: "transport", type: "transport", selectedCandidatePairId: "pair" });
    result.set("pair", { id: "pair", type: "candidate-pair", timestamp: 6000, availableOutgoingBitrate: 8e6, currentRoundTripTime: 0.04, localCandidateId: "private-address" });
    result.set("remote", { id: "remote", type: "remote-inbound-rtp", kind: "video", localId: "out", ssrc: 22, timestamp: 5800, roundTripTime: 0.08, fractionLost: 0.02 });
    return result;
};
const feedbackBefore = report({ ...before.get("out"), qualityLimitationDurations: { bandwidth: 12, cpu: 2 } });

test("sender diagnostics distinguish shared transport budget, remote feedback and applied encoding cap", () => {
    const source = { ...context, encodings: [{ rid: "f", active: true, maxBitrate: 50e6 }, { rid: "q", active: false, maxBitrate: 1e6 }] };
    const result = collect(feedbackReport(), feedbackBefore, source, source);
    assert.equal(result.available_outgoing_bitrate_bps, 8e6); assert.equal(result.transport_rtt_ms, 40);
    assert.equal(result.remote_rtt_ms, 80); assert.equal(result.remote_fraction_lost, 0.02);
    assert.equal(result.encoding_max_bitrate_bps, 50e6); assert.equal(result.encoding_active, true);
    assert.equal(result.bandwidth_limited_ms, 2000); assert.equal(result.cpu_limited_ms, 0);
    assert.doesNotMatch(JSON.stringify(result), /private-address|localCandidateId|remoteId|selectedCandidatePairId/);
});

test("feedback diagnostics reject unrelated, stale, future and invalid browser measurements", () => {
    for (const change of [{ localId: "other" }, { ssrc: 23 }, { type: "inbound-rtp" }, { timestamp: 500 }, { timestamp: 6001 }, { roundTripTime: Infinity, fractionLost: -1 }, { roundTripTime: null, fractionLost: null }]) {
        const stats = feedbackReport(); Object.assign(stats.get("remote"), change);
        const result = collect(stats, feedbackBefore);
        assert.equal(result.remote_rtt_ms, null); assert.equal(result.remote_fraction_lost, null);
    }
    for (const change of [{ timestamp: -10000 }, { timestamp: 6001 }, { type: "local-candidate" }, { availableOutgoingBitrate: Infinity, currentRoundTripTime: -1 }, { availableOutgoingBitrate: null, currentRoundTripTime: null }]) {
        const stats = feedbackReport(); Object.assign(stats.get("pair"), change);
        const result = collect(stats, feedbackBefore);
        assert.equal(result.available_outgoing_bitrate_bps, null); assert.equal(result.transport_rtt_ms, null);
    }
    const wrongTransport = feedbackReport({ transportId: "unknown" });
    assert.equal(collect(wrongTransport, feedbackBefore).available_outgoing_bitrate_bps, null);
    assert.equal(collect(feedbackReport({ remoteId: "unknown" }), feedbackBefore).remote_rtt_ms, null);
});

test("actual sender settings are copied narrowly and preserve explicit false/zero", () => {
    const parameters = { encodings: [{ active: false, maxBitrate: 0, ssrc: 123, privateId: "secret" }] };
    const copied = videoSenderEncodingSettings({ getParameters: () => parameters });
    parameters.encodings[0].active = true;
    assert.deepEqual(copied, [{ rid: "", active: false, maxBitrate: 0 }]);
    const result = collect(report({ ...after.get("out"), rid: undefined }), before, { ...context, encodings: copied });
    assert.equal(result.encoding_active, false); assert.equal(result.encoding_max_bitrate_bps, 0);
    assert.equal(videoSenderEncodingSettings({ getParameters: () => { throw new Error("closed"); } }), undefined);
    assert.equal(videoSenderEncodingSettings({}), undefined);
});

test("new, reset and invalid limitation counters stay unknown rather than inventing duration", () => {
    for (const [stats, old, source, oldSource] of [
        [feedbackReport(), null, context, context],
        [feedbackReport(), feedbackBefore, { ...context, generation: "43" }, context],
        [feedbackReport({ qualityLimitationDurations: { bandwidth: 11, cpu: 1 } }), feedbackBefore, context, context],
        [feedbackReport({ qualityLimitationDurations: { bandwidth: 20, cpu: Infinity } }), feedbackBefore, context, context],
    ]) {
        const result = collect(stats, old, source, oldSource);
        assert.equal(result.bandwidth_limited_ms, null); assert.equal(result.cpu_limited_ms, null);
    }
    const missing = collect();
    for (const key of ["available_outgoing_bitrate_bps", "transport_rtt_ms", "remote_rtt_ms", "remote_fraction_lost", "encoding_active", "encoding_max_bitrate_bps", "bandwidth_limited_ms", "cpu_limited_ms"]) assert.equal(missing[key], null, key);
    assert.equal(collect(feedbackReport(), null).remote_rtt_ms, null, "old receiver report must not be relabeled on publication start");
});
