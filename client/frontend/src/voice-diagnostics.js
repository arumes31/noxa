import { collectVideoSenders, videoSenderSources } from "./video-sender-stats.js";
import { publicationSnapshot } from "./stream-publication.js";
import { samplePeerStats } from "./peer-stats.js";
import { parseTrackID } from "./media-track-id.js";

// Bounded media telemetry. Only explicit quality fields leave the client;
// raw RTCStats, device identifiers, addresses and media content stay local.
const number = (value, min = 0, max = Number.MAX_SAFE_INTEGER) => Number.isFinite(value) && value >= min && value <= max ? value : null;
const milliseconds = value => Number.isFinite(value) ? number(value * 1000, 0, 60000) : null;
const delta = (now, before, field, minimum = 0) => number(now?.[field], minimum) !== null && number(before?.[field], minimum) !== null && now[field] >= before[field] ? now[field] - before[field] : null;
const ratio = (part, total, scale = 100) => part !== null && total !== null && total > 0 ? number(part / total * scale, 0, scale === 100 ? 100 : 60000) : null;
const speechActivity = new WeakMap();
const voiceScope = state => JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]);

// Remember both speech starts and stops: polling only the final speaking flag
// would lose a short phrase that ended between two diagnostic samples.
export function noteVoiceActivity(state, clientID, at = performance.timeOrigin + performance.now()) {
    if (!state.pc || state.replayingTabID || !clientID || !Number.isFinite(at)) return;
    const scope = voiceScope(state);
    let activity = speechActivity.get(state.pc);
    if (activity?.scope !== scope) {
        activity = { scope, publishers: new Map() };
        speechActivity.set(state.pc, activity);
    }
    activity.publishers.delete(clientID);
    activity.publishers.set(clientID, at);
    while (activity.publishers.size > 256) activity.publishers.delete(activity.publishers.keys().next().value);
}

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
    let severity = 0, complete = report.output_state === "running" && !report.truncated, count = 0, idle = 0;
    for (const track of report.tracks || []) {
        if (!(track.sample_ms > 0)) { complete = false; continue; }
        count++;
        const quiet = track.audio_active === false;
        if (quiet) idle++;
        const measurements = [["loss", track.loss_percent, 1, 3], ["discard", track.discard_percent, 1, 3],
            ["concealment", track.non_silent_concealment_percent, 0.5, 2], ["buffer", track.buffer_ms, 150, 300]];
        for (const [reason, value, fair, poor] of measurements) {
            // DTX/comfort-noise playout still accumulates buffer and concealment
            // counters. They do not establish damaged speech in a quiet interval.
            // Actual network loss/discards remain visible even while idle.
            if (quiet && (reason === "buffer" || reason === "concealment")) continue;
            if (!Number.isFinite(value)) { complete = false; continue; }
            if (value >= fair) { reasons.add(reason); severity = Math.max(severity, value >= poor ? 2 : 1); }
        }
    }
    const quality = severity === 2 ? "poor" : severity === 1 ? "fair" : count && complete ? (idle === count ? "idle" : "good") : "unknown";
    return { quality, reasons: quality === "idle" ? ["idle"] : [...reasons] };
}

function intervalAudioActivity(state, stat, previous, publisher, microphone) {
    if (!previous) return null;
    const client = microphone && state.clients?.find(candidate => candidate.client_id === publisher);
    const activity = speechActivity.get(state.pc);
    const recentSpeech = microphone && activity?.scope === voiceScope(state) && activity.publishers.get(publisher) >= previous.timestamp;
    if (client?.is_speaking || recentSpeech) return true;
    // W3C RTCStats defines interval RMS as sqrt(delta energy / delta duration).
    // Unlike instantaneous audioLevel, this retains speech earlier in the poll.
    // Positive energy is useful evidence, but Chromium's muted HTML sink with
    // WebAudio playback can report ZERO energy even during decoded speech.
    // Therefore zero energy alone must never establish an idle interval.
    const energy = delta(stat, previous, "totalAudioEnergy");
    const duration = delta(stat, previous, "totalSamplesDuration");
    if (energy !== null && duration > 0 && energy <= duration && energy / duration > 0.000001) return true;
    return client?.is_speaking === false ? false : null;
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

export function collectVoiceTelemetry(state, stats, previousStats, output = {}, videoSources = [], previousVideoSources = []) {
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
        const owner = state.trackUsers?.get(stat.trackIdentifier);
        const publisher = owner?.client_id || (/^c-[a-zA-Z0-9-]+(?:\||$)/.test(trackID) ? trackID.split("|")[0] : "");
        const microphone = !parseTrackID(owner?.track_id || trackID).slot;
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
            audio_active: intervalAudioActivity(state, stat, previous, publisher, microphone),
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
        video_senders: collectVideoSenders(stats, previousStats, videoSources, previousVideoSources),
    };
}

export function startVoiceDiagnostics({ state, output, bridge, intervalMS = 5000 }) {
    let stopped = false, pending = false, previous = null, previousSources = [], previousScope = "", previousPeer = null, sessionID = "";
    let previousPublications = [];
    const scope = () => voiceScope(state);
    const poll = async () => {
        if (stopped || pending) return;
        const pc = state.pc, tabID = state.activeTabID, key = scope();
        if (!pc || !tabID || !state.myChannelID || pc.connectionState === "closed") { previous = null; previousPeer = null; state.voiceTelemetry = null; return; }
        if (pc !== previousPeer || key !== previousScope) { previous = null; sessionID = crypto.randomUUID(); }
        pending = true;
        const current = () => !stopped && state.pc === pc && scope() === key;
        try {
            const publications = publicationSnapshot();
            const samePublications = publications.length === previousPublications.length && publications.every((entry, index) =>
                entry.publication === previousPublications[index].publication && entry.generation === previousPublications[index].generation);
            const stats = await samplePeerStats(pc, { fresh: pc !== previousPeer || key !== previousScope || !samePublications });
            if (!current()) return;
            const sources = videoSenderSources(state, publications);
            const report = collectVoiceTelemetry(state, stats, previous, output?.() || {}, sources, previousSources);
            report.session_id = sessionID;
            previous = stats; previousSources = sources; previousScope = key; previousPeer = pc;
            previousPublications = publications;
            state.voiceTelemetry = { report, at: Date.now(), scope: key, peer: pc };
            await bridge(tabID, report);
        } catch {
            // Diagnostics are best-effort; a failed sample never interrupts voice.
            previous = null;
        } finally { pending = false; }
    };
    void poll();
    const timer = setInterval(() => { void poll(); }, intervalMS);
    return () => {
        stopped = true; clearInterval(timer); previous = null; previousPeer = null;
        previousSources = []; previousPublications = []; state.voiceTelemetry = null;
    };
}
