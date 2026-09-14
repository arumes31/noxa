// Counters are RTP payload totals, not wire totals; remote reports must not be
// counted again. See https://www.w3.org/TR/webrtc-stats/#rtpstatshierarchy.
export const measured = (value) => Number.isFinite(value) && value >= 0;

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
        if (!before || !measured(row[field]) || !measured(before[field]) ||
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
