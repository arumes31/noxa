// Counters are RTP payload totals, not wire totals; remote reports must not be
// counted again. See https://www.w3.org/TR/webrtc-stats/#rtpstatshierarchy.
export const measured = (value) => Number.isFinite(value) && value >= 0;

export function formatBitrate(bitsPerSecond) {
    if (!measured(bitsPerSecond)) return "—";
    return bitsPerSecond >= 1000000
        ? `${(bitsPerSecond / 1000000).toFixed(2)} Mbit/s`
        : `${Math.round(bitsPerSecond / 1000)} kbit/s`;
}

// Only the selected receiver's RTP payload is included, never other streams,
// remote reports, candidate addresses or credentials.
export function summarizeStream(report, trackID, previous) {
    const rows = report && typeof trackID === "string" && trackID.length ? [...report.values()].filter(row => row.type === "inbound-rtp" &&
        (row.kind || row.mediaType) === "video" && row.trackIdentifier === trackID) : [];
    const codecs = [...new Set(rows.map(row => report.get(row.codecId)?.mimeType?.replace(/^video\//i, "")).filter(Boolean))];
    const before = streamInterval(rows, previous?.rows) ? previous.rows : undefined;
    const bytesPerSecond = rate(rows, before, "bytesReceived");
    const sampleMS = before ? Math.max(...rows.map(row => row.timestamp - before.find(old => old.id === row.id).timestamp)) : null;
    const received = change(rows, before, "packetsReceived"), lost = change(rows, before, "packetsLost");
    const decoder = [...new Set(rows.map(row => typeof row.decoderImplementation === "string" ? row.decoderImplementation.trim().slice(0, 160) : "").filter(Boolean))].join(" / ") || null;
    return {
        rows, codec: codecs.join(" / ") || null,
        bitrate: measured(bytesPerSecond) ? bytesPerSecond * 8 : null,
        bytes: sum(rows, "bytesReceived"), frames: sum(rows, "framesDecoded"), fps: rate(rows, before, "framesDecoded"),
        width: rows.length === 1 && measured(rows[0].frameWidth) ? rows[0].frameWidth : null,
        height: rows.length === 1 && measured(rows[0].frameHeight) ? rows[0].frameHeight : null,
        packetsLost: sum(rows, "packetsLost"),
        sampleMS, receivedFPS: rate(rows, before, "framesReceived"), droppedFPS: rate(rows, before, "framesDropped"),
        framesReceived: sum(rows, "framesReceived"), framesDropped: sum(rows, "framesDropped"),
        decodeMS: intervalRatio(rows, before, "totalDecodeTime", "framesDecoded", 1000),
        bufferMS: intervalRatio(rows, before, "jitterBufferDelay", "jitterBufferEmittedCount", 1000),
        processingMS: intervalRatio(rows, before, "totalProcessingDelay", "framesDecoded", 1000),
        lossPercent: measured(received) && measured(lost) && received + lost > 0 ? lost * 100 / (received + lost) : null,
        nacks: change(rows, before, "nackCount"), plis: change(rows, before, "pliCount"),
        freezes: change(rows, before, "freezeCount"), keyFrames: change(rows, before, "keyFramesDecoded"),
        retransmittedPackets: change(rows, before, "retransmittedPacketsReceived"),
        decoder, powerEfficientDecoder: rows.length && rows.every(row => typeof row.powerEfficientDecoder === "boolean")
            ? rows.every(row => row.powerEfficientDecoder) : null,
    };
}

function streamInterval(rows, before) {
    return rows.length && before?.length === rows.length && rows.every(row => {
        const old = before.find(candidate => candidate.id === row.id);
        return old && old.ssrc === row.ssrc && old.trackIdentifier === row.trackIdentifier &&
            Number.isFinite(row.timestamp) && Number.isFinite(old.timestamp) && row.timestamp > old.timestamp && row.timestamp - old.timestamp <= 60000 &&
            ["framesDecoded", "bytesReceived"].every(field => !measured(row[field]) || !measured(old[field]) || row[field] >= old[field]);
    });
}

function change(rows, before, field) {
    if (!before || !rows.length) return null;
    let total = 0;
    for (const row of rows) {
        const old = before.find(candidate => candidate.id === row.id);
        if (!old || !measured(row[field]) || !measured(old[field]) || row[field] < old[field]) return null;
        total += row[field] - old[field];
    }
    return total;
}

function intervalRatio(rows, before, numerator, denominator, scale) {
    const part = change(rows, before, numerator), count = change(rows, before, denominator);
    return measured(part) && measured(count) && count > 0 ? part * scale / count : null;
}

// Report what this runtime actually used, independently for each direction.
// Implementation names and the efficiency hint are not proof of GPU execution.
export function summarizeVideoProcessing(report) {
    const rows = report ? [...report.values()] : [];
    const codecs = new Map(rows.filter((row) => row.type === "codec").map((row) => [row.id, row]));
    const text = (value) => typeof value === "string" && value.trim() ? value.trim() : null;
    const collect = (type, frames, implementation, efficiency) => rows
        .filter((row) => row.type === type && (row.kind || row.mediaType) === "video" &&
            row.active !== false && measured(row[frames]) && row[frames] > 0)
        .map((row) => ({
            codec: text(codecs.get(row.codecId)?.mimeType)?.replace(/^video\//i, "") || null,
            implementation: text(row[implementation]),
            powerEfficient: typeof row[efficiency] === "boolean" ? row[efficiency] : null,
        }));
    return {
        encoders: collect("outbound-rtp", "framesEncoded", "encoderImplementation", "powerEfficientEncoder"),
        decoders: collect("inbound-rtp", "framesDecoded", "decoderImplementation", "powerEfficientDecoder"),
    };
}

export function formatBytes(value) {
    if (!measured(value)) return "—";
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    const unit = Math.min(units.length - 1, Math.floor(Math.log2(Math.max(1, value)) / 10));
    return `${(value / 1024 ** unit).toFixed(unit ? 2 : 0)} ${units[unit]}`;
}

export function formatDuration(seconds) {
    if (!measured(seconds)) return "—";
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor(seconds / 3600) % 24;
    const minutes = Math.floor(seconds / 60) % 60;
    return `${days ? `${days}d ` : ""}${hours}h ${minutes}m ${Math.floor(seconds) % 60}s`;
}

function sum(rows, field) {
    return rows.length && rows.every((r) => measured(r[field]))
        ? rows.reduce((total, r) => total + r[field], 0) : null;
}

function rate(rows, previous, field) {
    if (!rows.length || !previous || rows.length !== previous.length) return null;
    let total = 0;
    for (const row of rows) {
        const before = previous.find((r) => r.id === row.id);
        if (!before || row.ssrc !== before.ssrc || row.trackIdentifier !== before.trackIdentifier || !measured(row[field]) || !measured(before[field]) ||
            !Number.isFinite(row.timestamp) || !Number.isFinite(before.timestamp) ||
            row.timestamp <= before.timestamp || row[field] < before[field]) return null;
        total += (row[field] - before[field]) * 1000 / (row.timestamp - before.timestamp);
    }
    return total;
}

export function summarizeMedia(report, previous) {
    const rows = report ? [...report.values()] : [];
    const incoming = rows.filter((r) => r.type === "inbound-rtp");
    const outgoing = rows.filter((r) => r.type === "outbound-rtp");
    const audio = incoming.filter((r) => (r.kind || r.mediaType) === "audio");
    const received = sum(audio, "packetsReceived");
    // Late packets can make packetsLost negative; they do not mean negative loss.
    const lost = audio.length && audio.every((r) => Number.isFinite(r.packetsLost))
        ? audio.reduce((total, r) => total + Math.max(0, r.packetsLost), 0) : null;
    const jitter = audio.length && audio.every((r) => measured(r.jitter))
        ? Math.max(...audio.map((r) => r.jitter)) * 1000 : null;
    return {
        incoming, outgoing,
        inBytes: sum(incoming, "bytesReceived"), outBytes: sum(outgoing, "bytesSent"),
        inPackets: sum(incoming, "packetsReceived"), outPackets: sum(outgoing, "packetsSent"),
        inRate: rate(incoming, previous?.incoming, "bytesReceived"),
        outRate: rate(outgoing, previous?.outgoing, "bytesSent"),
        loss: measured(received) && measured(lost) && received + lost > 0 ? lost / (received + lost) * 100 : null,
        jitter,
    };
}
