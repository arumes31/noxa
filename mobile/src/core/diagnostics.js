// diagnostics.js — connection measurements without DOM: ping statistics
// over Msg.Ping, a throughput probe against the app's own assets, and
// WebRTC stats reduced to the numbers the Diagnostics screen shows.
import { Msg } from "./frame.js";

export const PING_INTERVAL = 5000;
export const PING_WINDOW = 12; // one minute of samples

/**
 * Round-trip statistics of samples in milliseconds; null marks a lost ping.
 * Jitter is the mean absolute difference of consecutive answered samples,
 * the same idea as the RTP interarrival jitter, without its smoothing.
 */
export function pingStats(samples) {
    const answered = samples.filter(s => typeof s === "number" && Number.isFinite(s));
    const lost = samples.length - answered.length;
    if (!answered.length) return { count: samples.length, mean: null, min: null, max: null, jitter: null, loss: samples.length ? 1 : 0, last: null };
    const mean = answered.reduce((a, b) => a + b, 0) / answered.length;
    let jitter = 0;
    for (let i = 1; i < answered.length; i++) jitter += Math.abs(answered[i] - answered[i - 1]);
    jitter = answered.length > 1 ? jitter / (answered.length - 1) : 0;
    return {
        count: samples.length,
        mean,
        min: Math.min(...answered),
        max: Math.max(...answered),
        jitter,
        loss: lost / samples.length,
        last: samples[samples.length - 1] ?? null,
    };
}

/** "good" | "fair" | "poor" | "unknown" for a round trip and loss fraction. */
export function pingQuality(rtt, loss = 0, jitter = 0) {
    if (rtt === null || rtt === undefined || !Number.isFinite(rtt)) return loss > 0 ? "poor" : "unknown";
    if (rtt < 120 && loss < 0.02 && jitter < 30) return "good";
    if (rtt < 300 && loss < 0.1 && jitter < 80) return "fair";
    return "poor";
}

/** One Ping/Pong round trip in milliseconds. */
export async function pingOnce(conn, now = () => performance.now()) {
    const started = now();
    await conn.request(Msg.Ping, {}, Msg.Pong, { timeout: 5000 });
    return now() - started;
}

/**
 * Pings every interval while started; "sample" events carry the stats of
 * the last PING_WINDOW samples. The connection is read lazily so a
 * reconnect simply continues on the new session.
 */
export class PingMonitor extends EventTarget {
    samples = [];
    #timer = 0;
    #users = 0;
    #getConn;
    #busy = false;

    constructor(getConn, { interval = PING_INTERVAL, window = PING_WINDOW } = {}) {
        super();
        this.#getConn = getConn;
        this.interval = interval;
        this.window = window;
    }

    get stats() {
        return pingStats(this.samples);
    }

    get quality() {
        const stats = this.stats;
        return pingQuality(stats.last ?? stats.mean, stats.loss, stats.jitter);
    }

    /** Starts pinging; every start needs a stop. Returns the stop function. */
    start() {
        this.#users++;
        if (this.#users === 1) {
            this.tick();
            this.#timer = setInterval(() => this.tick(), this.interval);
        }
        let stopped = false;
        return () => {
            if (stopped) return;
            stopped = true;
            if (--this.#users === 0) clearInterval(this.#timer);
        };
    }

    async tick() {
        if (this.#busy) return;
        if (typeof document !== "undefined" && document.visibilityState === "hidden") return;
        const conn = this.#getConn();
        if (!conn?.open) return;
        this.#busy = true;
        let sample = null;
        try { sample = await pingOnce(conn); } catch { sample = null; }
        this.#busy = false;
        this.samples.push(sample);
        if (this.samples.length > this.window) this.samples.splice(0, this.samples.length - this.window);
        this.dispatchEvent(new CustomEvent("sample", { detail: this.stats }));
    }
}

/** Pings count times back to back; resolves with the samples (ms or null). */
export async function pingSeries(conn, count = 10, gap = 150) {
    const samples = [];
    for (let i = 0; i < count; i++) {
        try { samples.push(await pingOnce(conn)); } catch { samples.push(null); }
        if (gap && i < count - 1) await new Promise(resolve => setTimeout(resolve, gap));
    }
    return samples;
}

/** Bytes and milliseconds -> kilobits per second. */
export function kbps(bytes, ms) {
    return ms > 0 ? (bytes * 8) / ms : 0;
}

/**
 * Downloads app assets until at least minBytes arrived and returns
 * {bytes, ms, kbps}. Query strings defeat the HTTP cache and the service
 * worker's asset cache; the probe entries are removed again afterwards.
 */
export async function measureThroughput(urls, { minBytes = 300 * 1024, fetchImpl = fetch, now = () => performance.now(), cacheStorage = globalThis.caches } = {}) {
    const unique = [...new Set(urls)].filter(Boolean);
    if (!unique.length) throw new Error("no assets to measure");
    const tag = `noxa-probe=${Date.now()}`;
    let bytes = 0;
    const started = now();
    try {
        for (let round = 0; bytes < minBytes && round < 8; round++) {
            for (const url of unique) {
                const response = await fetchImpl(`${url}${url.includes("?") ? "&" : "?"}${tag}-${round}`, { cache: "no-store" });
                if (!response.ok) continue;
                bytes += (await response.arrayBuffer()).byteLength;
                if (bytes >= minBytes) break;
            }
            if (!bytes) throw new Error("the assets could not be downloaded");
        }
    } finally {
        await removeProbeEntries(cacheStorage).catch(() => {});
    }
    const ms = Math.max(1, now() - started);
    return { bytes, ms, kbps: kbps(bytes, ms) };
}

async function removeProbeEntries(cacheStorage) {
    if (!cacheStorage) return;
    for (const name of await cacheStorage.keys()) {
        const cache = await cacheStorage.open(name);
        for (const request of await cache.keys()) if (request.url.includes("noxa-probe=")) await cache.delete(request);
    }
}

// --- WebRTC statistics --------------------------------------------------------------

function list(stats) {
    if (!stats) return [];
    if (Array.isArray(stats)) return stats;
    if (typeof stats.values === "function") return [...stats.values()];
    return Object.values(stats);
}

function codecName(byID, codecId) {
    const codec = byID.get(codecId);
    return codec?.mimeType ? codec.mimeType.split("/").pop() : "";
}

/** A rate from two cumulative counters, per second. */
function rate(now, before, field, timestamp, previous) {
    if (!before || typeof previous !== "number" || !(timestamp > previous)) return null;
    const delta = (now?.[field] ?? 0) - (before[field] ?? 0);
    return delta >= 0 ? delta / ((timestamp - previous) / 1000) : null;
}

/**
 * Voice numbers from RTCPeerConnection.getStats(): {rtt, jitter, packetLoss,
 * codec, bitrateIn, bitrateOut} (ms, ms, percent, name, kbit/s). previous is
 * the snapshot returned last time, for the rates; the result carries the new
 * snapshot as .snapshot.
 */
export function voiceStats(stats, previous = null) {
    const all = list(stats);
    const byID = new Map(all.map(s => [s.id, s]));
    const inbound = all.filter(s => s.type === "inbound-rtp" && (s.kind || s.mediaType) === "audio");
    const outbound = all.filter(s => s.type === "outbound-rtp" && (s.kind || s.mediaType) === "audio");
    const pair = all.find(s => s.type === "candidate-pair" && (s.nominated || s.selected) && s.state === "succeeded")
        || all.find(s => s.type === "candidate-pair" && s.state === "succeeded");
    const remote = all.find(s => s.type === "remote-inbound-rtp" && (s.kind || s.mediaType) === "audio");
    const sum = (rows, field) => rows.reduce((n, s) => n + (Number(s[field]) || 0), 0);
    const timestamp = Math.max(0, ...all.map(s => Number(s.timestamp) || 0));
    const snapshot = {
        timestamp,
        bytesReceived: sum(inbound, "bytesReceived"),
        bytesSent: sum(outbound, "bytesSent"),
        packetsLost: sum(inbound, "packetsLost"),
        packetsReceived: sum(inbound, "packetsReceived"),
    };
    let packetLoss = null;
    const lost = previous ? snapshot.packetsLost - previous.packetsLost : snapshot.packetsLost;
    const received = previous ? snapshot.packetsReceived - previous.packetsReceived : snapshot.packetsReceived;
    if (lost + received > 0) packetLoss = Math.max(0, (lost / (lost + received)) * 100);
    const rttSeconds = pair?.currentRoundTripTime ?? remote?.roundTripTime;
    const jitters = inbound.map(s => s.jitter).filter(v => typeof v === "number");
    const inRate = rate(snapshot, previous, "bytesReceived", timestamp, previous?.timestamp);
    const outRate = rate(snapshot, previous, "bytesSent", timestamp, previous?.timestamp);
    return {
        rtt: typeof rttSeconds === "number" ? rttSeconds * 1000 : null,
        jitter: jitters.length ? (Math.max(...jitters) * 1000) : null,
        packetLoss,
        codec: codecName(byID, inbound[0]?.codecId) || codecName(byID, outbound[0]?.codecId),
        bitrateIn: inRate === null ? null : (inRate * 8) / 1000,
        bitrateOut: outRate === null ? null : (outRate * 8) / 1000,
        streams: inbound.length,
        snapshot,
    };
}

/**
 * Watched video streams: one entry per inbound video RTP stream with
 * {id, width, height, fps, bitrate (kbit/s), freezes, codec}; previous is the
 * snapshot map returned last time.
 */
export function videoStats(stats, previous = null) {
    const all = list(stats);
    const byID = new Map(all.map(s => [s.id, s]));
    const snapshot = new Map();
    const out = [];
    for (const s of all) {
        if (s.type !== "inbound-rtp" || (s.kind || s.mediaType) !== "video") continue;
        const before = previous?.get?.(s.id);
        const bytesRate = rate(s, before, "bytesReceived", s.timestamp, before?.timestamp);
        snapshot.set(s.id, { timestamp: s.timestamp, bytesReceived: s.bytesReceived || 0 });
        out.push({
            id: s.id,
            trackIdentifier: s.trackIdentifier || "",
            mid: s.mid ?? "",
            width: s.frameWidth || 0,
            height: s.frameHeight || 0,
            fps: s.framesPerSecond ?? null,
            bitrate: bytesRate === null ? null : (bytesRate * 8) / 1000,
            freezes: s.freezeCount ?? 0,
            dropped: s.framesDropped ?? 0,
            codec: codecName(byID, s.codecId),
        });
    }
    return { streams: out, snapshot };
}

/** Keeps the last `size` numbers (null for a gap) — a sparkline series. */
export function pushSeries(series, value, size = 60) {
    series.push(typeof value === "number" && Number.isFinite(value) ? value : null);
    if (series.length > size) series.splice(0, series.length - size);
    return series;
}

/** SVG polyline points for a series in a width×height box. */
export function sparklinePoints(series, width = 120, height = 28) {
    const values = series.filter(v => v !== null);
    if (!values.length) return "";
    const max = Math.max(...values, 1);
    const step = series.length > 1 ? width / (series.length - 1) : 0;
    return series.map((v, i) => v === null ? null : `${(i * step).toFixed(1)},${(height - (v / max) * (height - 2) - 1).toFixed(1)}`)
        .filter(Boolean).join(" ");
}
