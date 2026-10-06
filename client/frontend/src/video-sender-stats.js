// Public stream diagnostics contain measured counters only. Never serialize a
// track label, device identifier, source ID, candidate address or raw RTCStats.
const profiles = new WeakMap();
const number = (value, max = Number.MAX_SAFE_INTEGER) => Number.isFinite(value) && value >= 0 && value <= max ? value : null;
const counter = value => Number.isSafeInteger(value) && value >= 0 ? value : null;
const milliseconds = value => number(value) !== null ? number(value * 1000, 60000) : null;
const delta = (now, before, key) => number(now?.[key]) !== null && number(before?.[key]) !== null && now[key] >= before[key] ? now[key] - before[key] : null;
const interval = (now, before) => {
    const elapsed = delta(now, before, "timestamp");
    return elapsed > 0 && elapsed <= 15000 ? elapsed : null;
};
const generation = value => typeof value === "string" && /^[1-9][0-9]{0,19}$/.test(value) && BigInt(value) <= 0xffffffffffffffffn;
const encoders = new Set(["libvpx", "libaom", "openh264", "ExternalEncoder", "MediaFoundationVideoEncoder", "VideoToolbox"]);
const codecs = new Set(["video/vp8", "video/vp9", "video/h264", "video/av1"]);

// Copy only diagnostic parameters; RTCRtpParameters can contain identifiers.
export function videoSenderEncodingSettings(sender) {
    try {
        return sender?.getParameters?.().encodings?.map(encoding => ({ rid: encoding.rid || "",
            active: typeof encoding.active === "boolean" ? encoding.active : null, maxBitrate: number(encoding.maxBitrate, 1e9) }));
    } catch { return undefined; }
}

function senderFeedback(stats, stat, previous, context, sample) {
    const fresh = value => number(value?.timestamp) !== null && number(stat.timestamp) !== null &&
        value.timestamp <= stat.timestamp && stat.timestamp - value.timestamp <= 15000;
    const transport = stats.get(stat.transportId);
    const selected = transport?.type === "transport" ? stats.get(transport.selectedCandidatePairId) : null;
    const pair = selected?.type === "candidate-pair" && fresh(selected) ? selected : null;
    const report = stats.get(stat.remoteId);
    // A report received before this publication's baseline cannot describe its
    // current interval, even when Chromium reuses the sender SSRC after a stop.
    const remote = previous && report?.type === "remote-inbound-rtp" && report.localId === stat.id && report.ssrc === stat.ssrc &&
        fresh(report) && report.timestamp >= previous.timestamp ? report : null;
    const encoding = context.encodings?.find(value => value.rid === (stat.rid || ""));
    const limited = reason => {
        const seconds = delta(stat.qualityLimitationDurations, previous?.qualityLimitationDurations, reason);
        return sample && seconds !== null ? number(seconds * 1000, sample) : null;
    };
    let bandwidth = limited("bandwidth"), cpu = limited("cpu");
    if (bandwidth !== null && cpu !== null && bandwidth + cpu > sample) bandwidth = cpu = null;
    return {
        available_outgoing_bitrate_bps: number(pair?.availableOutgoingBitrate, 1e9),
        transport_rtt_ms: milliseconds(pair?.currentRoundTripTime),
        remote_rtt_ms: milliseconds(remote?.roundTripTime),
        remote_fraction_lost: number(remote?.fractionLost, 1),
        encoding_max_bitrate_bps: number(encoding?.maxBitrate, 1e9),
        encoding_active: typeof encoding?.active === "boolean" ? encoding.active : null,
        bandwidth_limited_ms: bandwidth, cpu_limited_ms: cpu,
    };
}

export function rememberVideoSenderProfile(track, profile) {
    if (track && typeof track === "object") profiles.set(track, { fps: number(profile?.fps, 240) });
}

export function videoSenderSources(state, snapshot) {
    return snapshot.filter(({ publication: p, generation: gen }) => generation(gen) && p.generation === gen && !p.cancelled &&
        p.pc === state.pc && p.scope.tabID === state.activeTabID && p.scope.generation === state.serverGeneration &&
        p.scope.session === state.sessionGeneration && p.track?.readyState === "live")
        .map(({ publication: p, generation: gen }) => {
            const transceiver = state.pc?.getTransceivers?.().find(t => t.sender?.track === p.track);
            const encodings = videoSenderEncodingSettings(transceiver?.sender);
            return { slot: p.slot, generation: gen, trackID: p.track.id, mid: transceiver?.mid,
                requestedFPS: profiles.get(p.track)?.fps ?? null, settingsFPS: number(p.track.getSettings?.().frameRate, 240),
                ...(encodings ? { encodings } : {}) };
        });
}

export function collectVideoSenders(stats, previousStats, sources = [], previousSources = []) {
    const candidates = new Map();
    for (const stat of stats.values()) {
        if (stat.type !== "outbound-rtp" || (stat.kind || stat.mediaType) !== "video" || stat.active === false ||
            !Number.isInteger(stat.ssrc) || stat.ssrc < 1 || stat.ssrc > 0xffffffff || ![undefined, "", "q", "h", "f"].includes(stat.rid)) continue;
        const source = stats.get(stat.mediaSourceId);
        const context = source?.trackIdentifier ? sources.find(s => source.trackIdentifier === s.trackID) :
            sources.find(s => s.mid != null && stat.mid === s.mid) || (sources.length === 1 && sources[0].senderScoped ? sources[0] : null);
        if (!context || !["cam", "screen"].includes(context.slot) || !generation(context.generation)) continue;
        const existing = candidates.get(stat.ssrc);
        if (!existing || stat.timestamp > existing.stat.timestamp) candidates.set(stat.ssrc, { stat, source, context });
    }
    const rows = [];
    for (const { stat, source, context } of candidates.values()) {
        const oldContext = previousSources.find(s => s.slot === context.slot && s.generation === context.generation && s.trackID === context.trackID &&
            s.requestedFPS === context.requestedFPS && s.settingsFPS === context.settingsFPS);
        let previous = oldContext && previousStats?.get(stat.id);
        if (previous?.ssrc !== stat.ssrc || previous?.rid !== stat.rid || previous?.mediaSourceId !== stat.mediaSourceId ||
            ["framesEncoded", "framesSent", "packetsSent", "bytesSent"].some(key => number(stat[key]) !== null && number(previous?.[key]) !== null && stat[key] < previous[key])) previous = null;
        const sample = interval(stat, previous);
        if (sample === null) previous = null;
        const rate = (key, scale = 1000, max = 240) => {
            const part = delta(stat, previous, key);
            return part !== null && sample ? number(part * scale / sample, max) : null;
        };
        const average = (key, count) => {
            const part = delta(stat, previous, key), total = delta(stat, previous, count);
            return part !== null && total > 0 ? number(part * 1000 / total, 60000) : null;
        };
        const oldSource = previous && previousStats?.get(stat.mediaSourceId);
        const sourceSample = source?.trackIdentifier === oldSource?.trackIdentifier ? interval(source, oldSource) : null;
        const captured = sourceSample && delta(source, oldSource, "frames");
        const validRetransmits = value => counter(value?.retransmittedBytesSent) !== null && counter(value?.bytesSent) !== null && value.retransmittedBytesSent <= value.bytesSent;
        const sentBytes = delta(stat, previous, "bytesSent"), retryBytes = delta(stat, previous, "retransmittedBytesSent");
        const retrySubset = validRetransmits(stat) && validRetransmits(previous) && sentBytes !== null && retryBytes !== null && retryBytes <= sentBytes;
        const encodedFrames = delta(stat, previous, "framesEncoded");
        const validKeyframes = value => counter(value?.keyFramesEncoded) !== null && counter(value?.framesEncoded) !== null && value.keyFramesEncoded <= value.framesEncoded;
        const keyframes = validKeyframes(stat) && validKeyframes(previous) ? delta(stat, previous, "keyFramesEncoded") : null;
        const codec = String(stats.get(stat.codecId)?.mimeType || "").toLowerCase();
        rows.push({ ssrc: stat.ssrc, rid: stat.rid || "", slot: context.slot, generation: context.generation, sample_ms: sample,
            ...senderFeedback(stats, stat, previous, context, sample),
            requested_fps: number(context.requestedFPS, 240), settings_fps: number(context.settingsFPS, 240),
            capture_fps: sourceSample && captured !== null ? number(captured * 1000 / sourceSample, 240) : null,
            encoded_fps: rate("framesEncoded"), sent_fps: rate("framesSent"), reported_fps: number(stat.framesPerSecond, 240),
            width: number(stat.frameWidth, 16384), height: number(stat.frameHeight, 16384),
            bitrate_bps: rate("bytesSent", 8000, 1e9), encode_ms: average("totalEncodeTime", "framesEncoded"),
            target_bitrate_bps: number(stat.targetBitrate, 1e9),
            retransmit_bitrate_bps: retrySubset && sample ? number(retryBytes * 8000 / sample, 1e9) : null,
            retransmit_percent: retrySubset && sentBytes > 0 ? number(retryBytes * 100 / sentBytes, 100) : null,
            frame_bytes: retrySubset && encodedFrames > 0 ? number((sentBytes - retryBytes) / encodedFrames, 1e9) : null,
            key_frames: validKeyframes(stat) ? counter(stat.keyFramesEncoded) : null,
            key_frames_delta: keyframes !== null && encodedFrames !== null && keyframes <= encodedFrames ? keyframes : null,
            send_delay_ms: average("totalPacketSendDelay", "packetsSent"),
            quality_reason: ["none", "cpu", "bandwidth", "other"].includes(stat.qualityLimitationReason) ? stat.qualityLimitationReason : "unknown",
            codec: codecs.has(codec) ? codec : "unknown",
            encoder_implementation: encoders.has(stat.encoderImplementation) ? stat.encoderImplementation : "unknown",
            power_efficient: typeof stat.powerEfficientEncoder === "boolean" ? stat.powerEfficientEncoder : null,
            frames_encoded: counter(stat.framesEncoded), frames_sent: counter(stat.framesSent), packets_sent: counter(stat.packetsSent),
            bytes_sent: counter(stat.bytesSent),
            retransmitted_packets: counter(stat.retransmittedPacketsSent) !== null && counter(stat.packetsSent) !== null && stat.retransmittedPacketsSent <= stat.packetsSent ? stat.retransmittedPacketsSent : null,
            retransmitted_bytes: validRetransmits(stat) ? stat.retransmittedBytesSent : null });
        if (rows.length === 8) break;
    }
    return rows;
}

export function primaryVideoSender(rows) {
    return [...rows].sort((a, b) => (b.width || 0) * (b.height || 0) - (a.width || 0) * (a.height || 0))[0] || null;
}

export function videoSenderFrameRateReduced(sample) {
    const expected = sample?.requested_fps;
    return expected > 0 && sample?.sample_ms >= 1000 && Number.isFinite(sample.sent_fps) && sample.sent_fps < expected * 0.75;
}
