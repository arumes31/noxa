// Bounded receiver telemetry. Only explicit quality fields leave the client;
// raw RTCStats, device identifiers, addresses and media content stay local.
const number = (value, min = 0, max = Number.MAX_SAFE_INTEGER) => Number.isFinite(value) && value >= min && value <= max ? value : null;
const milliseconds = value => Number.isFinite(value) ? number(value * 1000, 0, 60000) : null;
const delta = (now, before, field, minimum = 0) => number(now?.[field], minimum) !== null && number(before?.[field], minimum) !== null && now[field] >= before[field] ? now[field] - before[field] : null;
const ratio = (part, total, scale = 100) => part !== null && total !== null && total > 0 ? number(part / total * scale, 0, scale === 100 ? 100 : 60000) : null;

// This grades observed playback, not a promised end-to-end quality score.
// Unknown metrics cannot make a track look healthy; silent concealment alone
// does not establish lost speech. Thresholds flag actionable degradation.
export function voicePlaybackHealth(report, ageMS = 0) {
    if (!report) return { quality: "unknown", reasons: [] };
    if (ageMS > 15000) return { quality: "stale", reasons: [] };
    if (report.deafened || report.volume === 0) return { quality: "inactive", reasons: [] };
    if (report.connection_state !== "connected") return { quality: "unknown", reasons: ["connection"] };
    if (["suspended", "interrupted", "closed"].includes(report.output_state)) return { quality: "poor", reasons: ["output"] };
    const reasons = new Set();
    let severity = 0, complete = report.output_state === "running" && !report.truncated, count = 0;
    for (const track of report.tracks || []) {
        if (!(track.sample_ms > 0)) { complete = false; continue; }
        count++;
        const measurements = [["loss", track.loss_percent, 1, 3], ["discard", track.discard_percent, 1, 3],
            ["concealment", track.non_silent_concealment_percent, 0.5, 2], ["buffer", track.buffer_ms, 150, 300]];
        for (const [reason, value, fair, poor] of measurements) {
            if (!Number.isFinite(value)) { complete = false; continue; }
            if (value >= fair) { reasons.add(reason); severity = Math.max(severity, value >= poor ? 2 : 1); }
        }
    }
    return { quality: severity === 2 ? "poor" : severity === 1 ? "fair" : count && complete ? "good" : "unknown", reasons: [...reasons] };
}

export function selectedVoiceTransport(stats) {
    const allow = (value, values) => values.includes(value) ? value : "unknown";
    for (const stat of stats.values()) {
        if (stat.type !== "transport" || !stat.selectedCandidatePairId) continue;
        const pair = stats.get(stat.selectedCandidatePairId);
        if (!pair) continue;
        const local = stats.get(pair.localCandidateId), remote = stats.get(pair.remoteCandidateId);
        return { protocol: allow(local?.protocol, ["udp", "tcp"]),
            local_candidate: allow(local?.candidateType, ["host", "srflx", "prflx", "relay"]),
            remote_candidate: allow(remote?.candidateType, ["host", "srflx", "prflx", "relay"]),
            relay_protocol: allow(local?.relayProtocol, ["udp", "tcp", "tls"]) };
    }
    return null;
}

function senderTelemetry(stats, previousStats) {
    const senders = [];
    for (const stat of stats.values()) {
        if (stat.type !== "outbound-rtp" || (stat.kind || stat.mediaType) !== "audio" || !Number.isInteger(stat.ssrc) || stat.ssrc < 1 || stat.ssrc > 0xffffffff) continue;
        let previous = previousStats?.get(stat.id);
        if (previous?.ssrc !== stat.ssrc) previous = null;
        const elapsed = delta(stat, previous, "timestamp");
        const sample = elapsed > 0 && elapsed <= 60000 ? elapsed : null;
        if (sample === null) previous = null;
        const packets = delta(stat, previous, "packetsSent"), bytes = delta(stat, previous, "bytesSent");
        senders.push({ ssrc: stat.ssrc, sample_ms: sample,
            packets_sent: number(stat.packetsSent), bytes_sent: number(stat.bytesSent),
            packets_per_second: packets !== null && sample ? number(packets * 1000 / sample, 0, 1000000) : null,
            bitrate_bps: bytes !== null && sample ? number(bytes * 8000 / sample, 0, 1000000000) : null });
        if (senders.length === 8) break;
    }
    return senders;
}

export function collectVoiceTelemetry(state, stats, previousStats, output = {}) {
    const tracks = [];
    let rtt = null;
    const inbound = new Map();
    for (const stat of stats.values()) {
        if (stat.type === "transport" && stat.selectedCandidatePairId) rtt = milliseconds(stats.get(stat.selectedCandidatePairId)?.currentRoundTripTime);
        if (stat.type !== "inbound-rtp" || (stat.kind || stat.mediaType) !== "audio") continue;
        const key = String(stat.trackIdentifier || stat.id).slice(0, 160);
        const old = inbound.get(key);
        if (!old || stat.timestamp > old.timestamp || (stat.timestamp === old.timestamp && (stat.packetsReceived || 0) > (old.packetsReceived || 0))) inbound.set(key, stat);
    }
    for (const stat of [...inbound.values()].slice(0, 64)) {
        const trackID = String(stat.trackIdentifier || stat.id).slice(0, 160);
        const publisher = state.trackUsers?.get(stat.trackIdentifier)?.client_id || (/^c-[a-zA-Z0-9-]+(?:\||$)/.test(trackID) ? trackID.split("|")[0] : "");
        let previous = previousStats?.get(stat.id);
        if (previous?.trackIdentifier !== stat.trackIdentifier || previous?.ssrc !== stat.ssrc) previous = null;
        const elapsed = delta(stat, previous, "timestamp");
        const sample = elapsed > 0 && elapsed <= 60000 ? elapsed : null;
        if (sample === null) previous = null;
        const received = delta(stat, previous, "packetsReceived"), lost = delta(stat, previous, "packetsLost", -Number.MAX_SAFE_INTEGER);
        const validDiscarded = number(stat.packetsDiscarded) !== null && number(stat.packetsReceived) !== null && stat.packetsDiscarded <= stat.packetsReceived;
        const validPreviousDiscarded = number(previous?.packetsDiscarded) !== null && number(previous?.packetsReceived) !== null && previous.packetsDiscarded <= previous.packetsReceived;
        const discarded = validDiscarded && validPreviousDiscarded ? delta(stat, previous, "packetsDiscarded") : null;
        const emitted = delta(stat, previous, "jitterBufferEmittedCount");
        const samples = delta(stat, previous, "totalSamplesReceived");
        const concealed = delta(stat, previous, "concealedSamples"), silent = delta(stat, previous, "silentConcealedSamples");
        // Silent concealment is a subset, not additional lost speech. A reset
        // or inconsistent subset cannot establish a recent non-silent rate.
        const validSilent = number(stat.silentConcealedSamples) !== null && number(stat.concealedSamples) !== null && stat.silentConcealedSamples <= stat.concealedSamples;
        const validPreviousSilent = number(previous?.silentConcealedSamples) !== null && number(previous?.concealedSamples) !== null && previous.silentConcealedSamples <= previous.concealedSamples;
        const splitConcealment = validSilent && validPreviousSilent && concealed !== null && silent !== null && silent <= concealed;
        tracks.push({
            track_id: trackID, publisher_id: String(publisher).slice(0, 80), codec: String(stats.get(stat.codecId)?.mimeType || "").slice(0, 40),
            ssrc: Number.isInteger(stat.ssrc) ? number(stat.ssrc, 1, 0xffffffff) : null,
            sample_ms: sample,
            packets_received: number(stat.packetsReceived), packets_lost: number(stat.packetsLost, -Number.MAX_SAFE_INTEGER), bytes_received: number(stat.bytesReceived),
            packets_discarded: validDiscarded ? number(stat.packetsDiscarded) : null, discard_percent: ratio(discarded, received),
            total_samples: number(stat.totalSamplesReceived), concealed_samples: number(stat.concealedSamples), concealment_events: number(stat.concealmentEvents),
            silent_concealed_samples: validSilent ? number(stat.silentConcealedSamples) : null,
            accelerated_samples: number(stat.removedSamplesForAcceleration), decelerated_samples: number(stat.insertedSamplesForDeceleration),
            jitter_ms: milliseconds(stat.jitter), audio_level: number(stat.audioLevel, 0, 1),
            loss_percent: ratio(lost, received !== null && lost !== null ? received + lost : null),
            concealment_percent: ratio(concealed, samples),
            silent_concealment_percent: splitConcealment ? ratio(silent, samples) : null,
            non_silent_concealment_percent: splitConcealment ? ratio(concealed - silent, samples) : null,
            acceleration_percent: ratio(delta(stat, previous, "removedSamplesForAcceleration"), samples),
            deceleration_percent: ratio(delta(stat, previous, "insertedSamplesForDeceleration"), samples),
            buffer_ms: ratio(delta(stat, previous, "jitterBufferDelay"), emitted, 1000),
            buffer_target_ms: ratio(delta(stat, previous, "jitterBufferTargetDelay"), emitted, 1000),
            buffer_minimum_ms: ratio(delta(stat, previous, "jitterBufferMinimumDelay"), emitted, 1000),
        });
    }
    return {
        channel_id: state.myChannelID, client_version: "", connection_state: state.pc?.connectionState || "closed",
        output_state: output.state || "unavailable", output_latency_ms: number(output.latencyMS, 0, 60000), rtt_ms: rtt,
        muted: !!state.muted, deafened: !!state.deafened, volume: Math.round(number(state.settings?.volume, 0, 200) ?? 100),
        voice_limiter: state.settings?.voice_limiter !== false, gain_normalize: !!state.settings?.gain_normalize, tracks,
        transport: selectedVoiceTransport(stats), senders: senderTelemetry(stats, previousStats),
    };
}

export function startVoiceDiagnostics({ state, output, bridge, intervalMS = 5000 }) {
    let stopped = false, pending = false, previous = null, previousScope = "", previousPeer = null, sessionID = "";
    const scope = () => JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]);
    const poll = async () => {
        if (stopped || pending) return;
        const pc = state.pc, tabID = state.activeTabID, key = scope();
        if (!pc || !tabID || !state.myChannelID || pc.connectionState === "closed") { previous = null; previousPeer = null; state.voiceTelemetry = null; return; }
        if (pc !== previousPeer || key !== previousScope) { previous = null; sessionID = crypto.randomUUID(); }
        pending = true;
        const current = () => !stopped && state.pc === pc && scope() === key;
        try {
            const stats = await pc.getStats();
            if (!current()) return;
            const report = collectVoiceTelemetry(state, stats, previous, output?.() || {});
            report.session_id = sessionID;
            previous = stats; previousScope = key; previousPeer = pc;
            state.voiceTelemetry = { report, at: Date.now(), scope: key, peer: pc };
            await bridge(tabID, report);
        } catch {
            // Diagnostics are best-effort; a failed sample never interrupts voice.
            previous = null;
        } finally { pending = false; }
    };
    void poll();
    const timer = setInterval(() => { void poll(); }, intervalMS);
    return () => { stopped = true; clearInterval(timer); previous = null; state.voiceTelemetry = null; };
}
