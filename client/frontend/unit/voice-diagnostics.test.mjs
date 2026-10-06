import test from "node:test";
import assert from "node:assert/strict";
import { collectVoiceTelemetry, startVoiceDiagnostics } from "../src/voice-diagnostics.js";

const settle = () => new Promise(resolve => setImmediate(resolve));
const snapshot = (...rows) => new Map(rows.map(row => [row.id, row]));
const near = (actual, expected) => assert.ok(Math.abs(actual - expected) < 0.00001, `${actual} should equal ${expected}`);

function receiver(overrides = {}) {
    return {
        id: "receiver-a", type: "inbound-rtp", kind: "audio", trackIdentifier: "browser-track-a", codecId: "opus",
        timestamp: 2000, packetsReceived: 1000, packetsLost: 102, bytesReceived: 50000,
        totalSamplesReceived: 20000, concealedSamples: 1050, concealmentEvents: 12,
        jitterBufferDelay: 24, jitterBufferTargetDelay: 35, jitterBufferEmittedCount: 1100,
        jitter: 0.008, audioLevel: 0.3, ...overrides,
    };
}

function previous(overrides = {}) {
    return receiver({ timestamp: 1000, packetsReceived: 900, packetsLost: 100, bytesReceived: 40000,
        totalSamplesReceived: 10000, concealedSamples: 1000, concealmentEvents: 10,
        jitterBufferDelay: 20, jitterBufferTargetDelay: 30, jitterBufferEmittedCount: 1000, ...overrides });
}

function stateFixture() {
    return {
        activeTabID: "server-a", serverGeneration: 4, myChannelID: 7, muted: false, deafened: false,
        settings: { volume: 125, voice_limiter: true, gain_normalize: false },
        trackUsers: new Map([["browser-track-a", { client_id: "c-alice", nickname: "Alice", unique_id: "private-identity" }]]),
        pc: { connectionState: "connected", getStats: async () => snapshot(receiver()) },
    };
}

test("receiver telemetry attributes every inbound audio track and excludes video and sender stats", () => {
    const state = stateFixture();
    const stats = snapshot(receiver(),
        receiver({ id: "receiver-b", trackIdentifier: "c-bob|screenaudio", kind: undefined, mediaType: "audio" }),
        receiver({ id: "unlabelled", trackIdentifier: undefined }),
        receiver({ id: "camera", kind: "video", trackIdentifier: "c-bob|cam" }),
        receiver({ id: "sender", type: "outbound-rtp" }),
        { id: "opus", type: "codec", mimeType: "audio/opus", sdpFmtpLine: "private SDP" });
    const report = collectVoiceTelemetry(state, stats, snapshot(previous()), { state: "running", latencyMS: 32 });
    assert.equal(report.channel_id, 7);
    assert.equal(report.connection_state, "connected");
    assert.equal(report.output_state, "running");
    assert.equal(report.output_latency_ms, 32);
    assert.equal(report.volume, 125);
    assert.equal(report.voice_limiter, true);
    assert.equal(report.gain_normalize, false);
    assert.deepEqual(report.tracks.map(row => [row.track_id, row.publisher_id]), [
        ["browser-track-a", "c-alice"], ["c-bob|screenaudio", "c-bob"], ["unlabelled", ""],
    ]);
    assert.equal(report.tracks[0].codec, "audio/opus");
});

test("loss, concealment and jitter buffers describe the recent interval instead of lifetime averages", () => {
    const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(receiver()), snapshot(previous()), {});
    assert.equal(track.sample_ms, 1000);
    near(track.loss_percent, 100 * 2 / 102);
    near(track.concealment_percent, 0.5);
    near(track.buffer_ms, 40);
    near(track.buffer_target_ms, 50);
    near(track.jitter_ms, 8);
    assert.equal(track.packets_received, 1000);
    assert.equal(track.packets_lost, 102);
    assert.equal(track.bytes_received, 50000);
    assert.equal(track.total_samples, 20000);
    assert.equal(track.concealed_samples, 1050);
    assert.equal(track.concealment_events, 12);
    assert.equal(track.audio_level, 0.3);
});

test("silence concealment and adaptive playback describe the same recent sample interval", () => {
    const before = previous({ silentConcealedSamples: 800, jitterBufferMinimumDelay: 20,
        removedSamplesForAcceleration: 4000, insertedSamplesForDeceleration: 100 });
    const now = receiver({ silentConcealedSamples: 840, jitterBufferMinimumDelay: 23,
        removedSamplesForAcceleration: 4100, insertedSamplesForDeceleration: 120 });
    const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(now), snapshot(before), {});
    assert.equal(track.silent_concealed_samples, 840);
    near(track.silent_concealment_percent, 0.4);
    near(track.non_silent_concealment_percent, 0.1);
    near(track.buffer_minimum_ms, 30);
    assert.equal(track.accelerated_samples, 4100);
    assert.equal(track.decelerated_samples, 120);
    near(track.acceleration_percent, 1);
    near(track.deceleration_percent, 0.2);
});

test("adaptive playback and silence fields stay unknown on missing, reset or replaced stats", () => {
    const before = previous({ silentConcealedSamples: 800, jitterBufferMinimumDelay: 20,
        removedSamplesForAcceleration: 4000, insertedSamplesForDeceleration: 100, ssrc: 12 });
    const now = receiver({ silentConcealedSamples: 840, jitterBufferMinimumDelay: 23,
        removedSamplesForAcceleration: 4100, insertedSamplesForDeceleration: 120, ssrc: 12 });
    for (const [current, old] of [
        [now, undefined],
        [now, snapshot({ ...before, ssrc: 13 })],
        [{ ...now, timestamp: before.timestamp }, snapshot(before)],
        [{ ...now, silentConcealedSamples: 799, jitterBufferMinimumDelay: 19,
            removedSamplesForAcceleration: 3999, insertedSamplesForDeceleration: 99 }, snapshot(before)],
        [receiver(), snapshot(previous())],
    ]) {
        const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(current), old, {});
        for (const field of ["silent_concealment_percent", "non_silent_concealment_percent", "buffer_minimum_ms",
            "acceleration_percent", "deceleration_percent"]) assert.equal(track[field], null, field);
    }
});

test("inconsistent silent concealment subsets cannot claim healthy speech or silence", () => {
    for (const [concealed, silent] of [[1050, 1100], [1010, 840], [1050, NaN]]) {
        const { tracks: [track] } = collectVoiceTelemetry(stateFixture(),
            snapshot(receiver({ concealedSamples: concealed, silentConcealedSamples: silent })),
            snapshot(previous({ silentConcealedSamples: 800 })), {});
        assert.equal(track.silent_concealment_percent, null);
        assert.equal(track.non_silent_concealment_percent, null);
    }
});

test("nonfinite or negative adaptive counters remain unknown rather than entering a report", () => {
    for (const value of [undefined, NaN, Infinity, -1]) {
        const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(receiver({
            silentConcealedSamples: value, removedSamplesForAcceleration: value,
            insertedSamplesForDeceleration: value, jitterBufferMinimumDelay: value,
        })), snapshot(previous()), {});
        for (const field of ["silent_concealed_samples", "accelerated_samples", "decelerated_samples",
            "silent_concealment_percent", "non_silent_concealment_percent", "buffer_minimum_ms", "acceleration_percent", "deceleration_percent"]) {
            assert.equal(track[field], null, field);
        }
    }
});

test("malformed adaptive counter baselines cannot produce a positive interval rate", () => {
    for (const [oldValue, value] of [[-2, -1], [Number.MAX_SAFE_INTEGER + 1, Number.MAX_SAFE_INTEGER + 3]]) {
        const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(receiver({
            removedSamplesForAcceleration: value, insertedSamplesForDeceleration: value, jitterBufferMinimumDelay: value,
        })), snapshot(previous({ removedSamplesForAcceleration: oldValue,
            insertedSamplesForDeceleration: oldValue, jitterBufferMinimumDelay: oldValue })), {});
        for (const field of ["accelerated_samples", "decelerated_samples", "acceleration_percent", "deceleration_percent", "buffer_minimum_ms"]) {
            assert.equal(track[field], null, field);
        }
    }
});

test("owner diagnostics label recent buffering and separate silence from non-silent concealment", async () => {
    const { renderReceiverDiagnostics } = await import("../src/voice-diagnostics-ui.js");
    const content = { innerHTML: "" };
    const section = { querySelector: () => content };
    const overlay = { querySelector: () => section };
    renderReceiverDiagnostics(overlay, { nickname: "Receiver", client_report: { age_ms: 0, report: {
        output_state: "running", tracks: [{ publisher_id: "publisher", sample_ms: 5000,
            buffer_ms: 501, buffer_target_ms: 400, buffer_minimum_ms: 399,
            concealment_percent: 1.5, silent_concealment_percent: 1.2, non_silent_concealment_percent: 0.3,
            acceleration_percent: 2.1, deceleration_percent: 0,
        }],
    } } }, [{ client_id: "publisher", nickname: "Alice <admin>" }]);
    assert.match(content.innerHTML, /Alice &lt;admin&gt;/);
    assert.match(content.innerHTML, /501\.0 ms \/ 400\.0 ms \/ 399\.0 ms/);
    assert.match(content.innerHTML, /1\.5% \/ 0\.3% \/ 1\.2%/);
    assert.match(content.innerHTML, /2\.1% \/ 0\.0%/);
    assert.match(content.innerHTML, /5\.0 s/);
    assert.match(content.innerHTML, /end-to-end latency/);
});

test("first, idle, replaced and reset receiver samples do not claim healthy zero loss or concealment", () => {
    const cases = [
        [receiver(), undefined],
        [receiver(), snapshot(receiver({ timestamp: 1000 }))],
        [receiver(), snapshot(previous({ id: "replaced-receiver" }))],
        [receiver(), snapshot(previous({ trackIdentifier: "old-publisher-track" }))],
        [receiver({ packetsReceived: 0, packetsLost: 0, totalSamplesReceived: 0, concealedSamples: 0,
            jitterBufferDelay: 0, jitterBufferTargetDelay: 0, jitterBufferEmittedCount: 0 }), snapshot(previous())],
    ];
    for (const [current, before] of cases) {
        const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(current), before, {});
        for (const key of ["loss_percent", "concealment_percent", "buffer_ms", "buffer_target_ms"]) {
            assert.equal(track[key], null, `${key} must be unknown for ${JSON.stringify(current)}`);
        }
    }
});

test("invalid timestamps cannot produce interval measurements", () => {
    for (const timestamp of [undefined, NaN, Infinity, 1000, 999, 62000]) {
        const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(receiver({ timestamp })), snapshot(previous()), {});
        for (const key of ["sample_ms", "loss_percent", "concealment_percent", "buffer_ms", "buffer_target_ms"]) {
            assert.equal(track[key], null, `${key} with timestamp ${timestamp}`);
        }
    }
});

test("late-packet corrections never create negative loss percentages", () => {
    const { tracks: [track] } = collectVoiceTelemetry(stateFixture(), snapshot(receiver({ packetsLost: 99 })), snapshot(previous()), {});
    assert.ok(track.loss_percent === null || track.loss_percent === 0);
    assert.equal(track.packets_lost, 99);
});

test("missing and nonfinite receiver measurements stay explicitly unknown", () => {
    const unknown = receiver({ packetsReceived: NaN, packetsLost: Infinity, bytesReceived: undefined,
        totalSamplesReceived: Infinity, concealedSamples: NaN, concealmentEvents: undefined,
        jitterBufferDelay: Infinity, jitterBufferTargetDelay: NaN, jitterBufferEmittedCount: undefined,
        jitter: NaN, audioLevel: Infinity });
    const report = collectVoiceTelemetry(stateFixture(), snapshot(unknown), snapshot(previous()), { state: "running", latencyMS: NaN });
    for (const key of ["packets_received", "packets_lost", "bytes_received", "total_samples", "concealed_samples", "concealment_events",
        "jitter_ms", "loss_percent", "concealment_percent", "buffer_ms", "buffer_target_ms", "audio_level"]) {
        assert.equal(report.tracks[0][key], null, key);
    }
    assert.equal(report.output_latency_ms, null);
    assert.equal(report.rtt_ms, null);
});

test("RTT comes from the transport's selected candidate pair, not an unrelated nominated pair", () => {
    const stats = snapshot(
        { id: "wrong-pair", type: "candidate-pair", state: "succeeded", nominated: true, currentRoundTripTime: 0.9 },
        { id: "selected-pair", type: "candidate-pair", state: "succeeded", currentRoundTripTime: 0.031 },
        { id: "transport", type: "transport", selectedCandidatePairId: "selected-pair" });
    assert.equal(collectVoiceTelemetry(stateFixture(), stats, undefined, {}).rtt_ms, 31);
    stats.delete("selected-pair");
    assert.equal(collectVoiceTelemetry(stateFixture(), stats, undefined, {}).rtt_ms, null);
});

test("diagnostics report playback controls without exposing device IDs, addresses, SDP or raw stats", () => {
    const state = stateFixture();
    state.muted = true; state.deafened = true;
    state.settings = { volume: 0, voice_limiter: false, gain_normalize: true, output_device: "private-device-id" };
    const stats = snapshot(receiver({ remoteId: "private-remote", arbitrarySecret: "private-stat" }),
        { id: "local", type: "local-candidate", address: "192.0.2.99", port: 49999 });
    const report = collectVoiceTelemetry(state, stats, undefined, { state: "suspended", latencyMS: 12, deviceId: "private-output" });
    assert.equal(report.muted, true);
    assert.equal(report.deafened, true);
    assert.equal(report.volume, 0);
    assert.equal(report.voice_limiter, false);
    assert.equal(report.gain_normalize, true);
    assert.equal(report.output_state, "suspended");
    assert.ok(!report.client_version);
    assert.doesNotMatch(JSON.stringify(report), /private-|192\.0\.2\.99|49999|arbitrarySecret|remoteId|deviceId|sdp/i);
});

test("reports are capped at 64 distinct receiver tracks", () => {
    const rows = Array.from({ length: 80 }, (_, i) => receiver({ id: `receiver-${i}`, trackIdentifier: `c-${i}` }));
    const report = collectVoiceTelemetry(stateFixture(), snapshot(...rows), undefined, {});
    assert.equal(report.tracks.length, 64);
    assert.equal(new Set(report.tracks.map(row => row.track_id)).size, 64);
});

test("SSRC replacement produces one current receiver per track, not an invalid duplicate report", () => {
    const older = receiver({ id: "old-ssrc", timestamp: 1000, packetsReceived: 9000, bytesReceived: 900000 });
    const newer = receiver({ id: "new-ssrc", timestamp: 2000, packetsReceived: 10, bytesReceived: 1000 });
    for (const rows of [[older, newer], [newer, older]]) {
        const report = collectVoiceTelemetry(stateFixture(), snapshot(...rows), undefined, {});
        assert.equal(report.tracks.length, 1);
        assert.equal(report.tracks[0].track_id, "browser-track-a");
        assert.equal(report.tracks[0].packets_received, 10);
    }
    const inactive = receiver({ id: "inactive-ssrc", timestamp: 2000, packetsReceived: 0, bytesReceived: 0 });
    for (const rows of [[inactive, newer], [newer, inactive]]) {
        const report = collectVoiceTelemetry(stateFixture(), snapshot(...rows), undefined, {});
        assert.equal(report.tracks.length, 1);
        assert.equal(report.tracks[0].packets_received, 10);
    }
});

test("polls immediately and periodically, then stops without another report", async t => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    const state = stateFixture(), sent = [];
    const stop = startVoiceDiagnostics({ state, output: () => ({ state: "running", latencyMS: 20 }),
        bridge: async (tab, report) => sent.push({ tab, report }), intervalMS: 1000 });
    t.after(stop);
    await settle();
    assert.equal(sent.length, 1);
    assert.equal(sent[0].tab, "server-a");
    assert.equal(sent[0].report.channel_id, 7);
    assert.equal(sent[0].report.output_latency_ms, 20);
    t.mock.timers.tick(1000); await settle();
    assert.equal(sent.length, 2);
    stop(); t.mock.timers.tick(10000); await settle();
    assert.equal(sent.length, 2);
});

test("an unresolved stats poll does not overlap interval polls and stopping discards its result", async t => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    const state = stateFixture(); let finish, requests = 0;
    state.pc.getStats = () => { requests++; return new Promise(resolve => { finish = resolve; }); };
    const sent = [];
    const stop = startVoiceDiagnostics({ state, output: () => ({}), bridge: async (...args) => sent.push(args), intervalMS: 1000 });
    t.after(stop);
    t.mock.timers.tick(5000); await settle();
    assert.equal(requests, 1);
    stop(); finish(snapshot(receiver())); await settle();
    assert.equal(sent.length, 0);
});

test("delayed stats from a previous server, reconnect, channel or peer connection are discarded", async t => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    for (const change of [
        state => { state.activeTabID = "server-b"; },
        state => { state.serverGeneration++; },
        state => { state.myChannelID = 8; },
        state => { state.pc = { ...state.pc }; },
    ]) {
        const state = stateFixture(), sent = []; let finish;
        state.pc.getStats = () => new Promise(resolve => { finish = resolve; });
        const stop = startVoiceDiagnostics({ state, output: () => ({}), bridge: async (...args) => sent.push(args), intervalMS: 1000 });
        change(state); finish(snapshot(receiver())); await settle(); stop();
        assert.equal(sent.length, 0);
    }
});

test("polling resumes after a scope change with a fresh baseline", async t => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    const state = stateFixture(), sent = [];
    state.pc.getStats = async () => snapshot(previous());
    const stop = startVoiceDiagnostics({ state, output: () => ({}), bridge: async (tab, report) => sent.push({ tab, report }), intervalMS: 1000 });
    t.after(stop); await settle();
    state.myChannelID = 8;
    state.pc.getStats = async () => snapshot(receiver());
    t.mock.timers.tick(1000); await settle();
    assert.equal(sent.length, 2);
    assert.equal(sent[1].report.channel_id, 8);
    assert.equal(sent[1].report.tracks[0].loss_percent, null);
    assert.equal(sent[1].report.tracks[0].sample_ms, null);
});

test("there is no reporting while outside a voice channel or without a peer connection", async t => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    const state = stateFixture(), sent = [];
    state.myChannelID = 0;
    const stop = startVoiceDiagnostics({ state, output: () => ({}), bridge: async (...args) => sent.push(args), intervalMS: 1000 });
    t.after(stop); await settle();
    assert.equal(sent.length, 0);
    state.myChannelID = 7; state.pc = null;
    t.mock.timers.tick(1000); await settle();
    assert.equal(sent.length, 0);
});

test("getStats and bridge failures do not stop later diagnostic reports", async t => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    const state = stateFixture(), sent = []; let calls = 0, rejected = false;
    state.pc.getStats = async () => { if (++calls === 1) throw new Error("temporary stats failure"); return snapshot(receiver()); };
    const stop = startVoiceDiagnostics({ state, output: () => ({}), bridge: async (tab, report) => {
        if (!rejected) { rejected = true; throw new Error("temporary bridge failure"); }
        sent.push({ tab, report });
    }, intervalMS: 1000 });
    t.after(stop); await settle();
    t.mock.timers.tick(1000); await settle();
    t.mock.timers.tick(1000); await settle();
    assert.equal(sent.length, 1);
    assert.equal(sent[0].tab, "server-a");
});
