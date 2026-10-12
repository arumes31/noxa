import { remoteTrackID } from "./media-track-id.js";
import { samplePeerStats } from "./peer-stats.js";

const scopeKey = state => JSON.stringify([state.activeTabID, state.serverGeneration, state.sessionGeneration, state.myClientID, state.myChannelID]);
const delays = [30000, 120000, 300000];
const capabilityQueries = new WeakMap();
const receiverResetRequests = new WeakMap();
const timedOut = Symbol("hardware recovery timeout");

async function bounded(promise, milliseconds) {
    let timer;
    try {
        return await Promise.race([promise, new Promise(resolve => { timer = setTimeout(() => resolve(timedOut), milliseconds); })]);
    } finally { clearTimeout(timer); }
}

// Candidates in both directions share one operation budget. Budgets belong to
// publications, not SSRCs: replacing an RTP stream must not refill its retries.
export function createHardwareRecoveryScheduler({ now = () => performance.now() } = {}) {
    let scope, busy = false, lastMutationSample;
    const entries = new Map();
    return async (key, candidates, sample) => {
        if (scope !== key) { scope = key; entries.clear(); lastMutationSample = undefined; }
        if (busy) return;
        const at = now();
        for (const candidate of candidates) {
            if (!candidate.current() || !Number.isFinite(candidate.timestamp)) continue;
            let entry = entries.get(candidate.key);
            const counter = value => Number.isSafeInteger(value) && value >= 0;
            const knownSoftwareInput = candidate.software && !candidate.hardware && entry?.hardware.has(candidate.config) &&
                counter(candidate.frames) && counter(candidate.receivedFrames) && counter(candidate.receivedPackets);
            const capture = candidate.capture;
            const knownSoftwareCapture = candidate.software && !candidate.hardware && entry?.hardware.has(candidate.config) &&
                counter(candidate.frames) && typeof capture?.id === "string" && capture.id.length > 0 &&
                counter(capture.frames) && Number.isFinite(capture.timestamp);
            // Zero decoded output is observable only if the runtime still
            // supplies geometry matching prior hardware, plus complete input.
            if (!(candidate.frames > 0) && !(knownSoftwareInput && candidate.receivedFrames > 0 && candidate.receivedPackets > 0) &&
                !(knownSoftwareCapture && capture.frames > 0)) continue;
            if (!entry) {
                if (entries.size >= 64) entries.delete(entries.keys().next().value);
                entry = { attempts: 0, next: 0, timestamp: -Infinity, hardware: new Set() };
                entries.set(candidate.key, entry);
            }
            if (candidate.timestamp <= entry.timestamp && candidate.instance === entry.instance) continue;
            // Received packets alone can be repairs/padding on an idle source.
            // For known software only, complete frames AND packets must advance
            // before incoming activity can substitute for stalled decoded output.
            const receiving = knownSoftwareInput && candidate.frames >= entry.frames &&
                counter(entry.receivedFrames) && counter(entry.receivedPackets) &&
                candidate.receivedFrames > entry.receivedFrames && candidate.receivedPackets > entry.receivedPackets;
            const capturing = knownSoftwareCapture && candidate.frames >= entry.frames && entry.capture?.id === capture.id &&
                capture.frames > entry.capture.frames && capture.timestamp > entry.capture.timestamp &&
                capture.timestamp - entry.capture.timestamp <= 15000;
            const continuous = entry.instance === candidate.instance && entry.config === candidate.config &&
                candidate.timestamp > entry.timestamp && candidate.timestamp - entry.timestamp <= 15000 &&
                (candidate.frames > entry.frames || receiving || capturing);
            entry.timestamp = candidate.timestamp;
            entry.instance = candidate.instance;
            entry.config = candidate.config;
            entry.frames = candidate.frames;
            entry.receivedFrames = counter(candidate.receivedFrames) ? candidate.receivedFrames : null;
            entry.receivedPackets = counter(candidate.receivedPackets) ? candidate.receivedPackets : null;
            entry.capture = capture ? { ...capture } : null;
            if (!continuous) { entry.softwareSince = null; entry.healthySince = null; }
            if (candidate.hardware) {
                entry.hardware.add(candidate.config);
                if (entry.hardware.size > 8) entry.hardware.delete(entry.hardware.values().next().value);
                entry.softwareSince = null;
                entry.healthySince ??= at;
                if (at - entry.healthySince >= 60000) { entry.attempts = 0; entry.next = 0; }
                continue;
            }
            entry.healthySince = null;
            if (!candidate.software) { entry.softwareSince = null; continue; }
            entry.softwareSince ??= at;
            if (at - entry.softwareSince < 10000 || at < entry.next || (sample !== undefined && sample === lastMutationSample)) continue;
            busy = true;
            try {
                const supported = entry.hardware.has(candidate.config) || await candidate.capable();
                if (scope !== key || !candidate.current()) return;
                // Negative/unknown capabilities also have a bounded probe rate.
                if (!supported) { entry.next = now() + delays[0]; continue; }
                entry.next = now() + delays[Math.min(entry.attempts++, delays.length - 1)];
                entry.softwareSince = null;
                lastMutationSample = sample;
                await candidate.recover({ hardwareObserved: entry.hardware.has(candidate.config) });
            } catch {
                // A failed reset must not become a poll-rate negotiation loop.
                if (scope === key) entry.next = Math.max(entry.next, now() + delays[0]);
            } finally { busy = false; }
            return; // At most one mutation per diagnostic sample.
        }
    };
}

export async function hardwareVideoDecodeAvailable(codec, stat, mediaCapabilities = globalThis.navigator?.mediaCapabilities) {
    if (!mediaCapabilities?.decodingInfo || !codec?.mimeType || !codec.sdpFmtpLine ||
        !(stat.frameWidth > 0) || !(stat.frameHeight > 0) || !(stat.framesPerSecond > 0)) return false;
    let timer;
    try {
        let queries = capabilityQueries.get(mediaCapabilities);
        if (!queries) { queries = new Map(); capabilityQueries.set(mediaCapabilities, queries); }
        const video = { contentType: `${codec.mimeType};${codec.sdpFmtpLine}`, width: stat.frameWidth, height: stat.frameHeight,
            framerate: stat.framesPerSecond, bitrate: 8000000 };
        const key = JSON.stringify(video), at = performance.now();
        let entry = queries.get(key);
        if (entry?.settled && at >= entry.expires) { queries.delete(key); entry = null; }
        if (!entry) {
            // A native capability call can outlive our timeout. Keep its promise
            // until it settles, and never create an unbounded set of hung calls.
            if (queries.size >= 16) {
                const disposable = [...queries].find(([, value]) => value.settled);
                if (!disposable) return false;
                queries.delete(disposable[0]);
            }
            entry = { settled: false };
            entry.promise = Promise.resolve().then(() => mediaCapabilities.decodingInfo({ type: "webrtc", video }))
                .catch(() => null).then(result => { entry.settled = true; entry.expires = performance.now() + 30000; return result; });
            queries.set(key, entry);
        }
        const result = await Promise.race([
            entry.promise,
            new Promise(resolve => { timer = setTimeout(() => resolve(null), 2000); }),
        ]);
        return result?.supported === true && result.powerEfficient === true;
    } catch { return false; } finally { clearTimeout(timer); }
}

// Ask the relay to replace this output binding. Direction toggles can stop a
// Pion transceiver irreversibly; playback and watch toggles retain the decoder.
// Do not occupy the negotiation queue while waiting: the relay's normal offer
// must be free to negotiate the replacement video without disturbing audio.
export async function resetVideoReceiver({ state, pc, transceiver, current, watched }, {
    request = (tab, control) => window.go.main.App.VideoStreamControlForTab(tab, control),
    sample = samplePeerStats, wait = ms => new Promise(resolve => setTimeout(resolve, ms)), now = () => performance.now(),
} = {}) {
    if (!current() || watched.bindingCurrent?.() === false || transceiver.stopped || transceiver.direction !== "recvonly" || transceiver.sender?.track) return false;
    let requests = receiverResetRequests.get(pc);
    if (!requests) { requests = new Map(); receiverResetRequests.set(pc, requests); }
    const key = watched.key || JSON.stringify(watched.request);
    let pending = requests.get(key);
    if (!pending) {
        if (requests.size >= 8) return false;
        pending = Promise.resolve().then(() => current() && watched.bindingCurrent?.() !== false ? request(state.activeTabID, watched.request) : timedOut);
        requests.set(key, pending);
        const finish = () => { if (requests.get(key) === pending) requests.delete(key); };
        pending.then(finish, finish);
    }
    // Do not block voice telemetry indefinitely, or enqueue another native
    // request behind an unresponsive bridge after our local timeout.
    if (await bounded(pending, 12000) === timedOut) return false;
    const end = now() + 10000;
    while (current() && now() < end) {
        const stats = await sample(pc, { fresh: true });
        if (!current()) return false;
        for (const receiver of pc.getTransceivers()) {
            if (receiver === transceiver || receiver.stopped || receiver.currentDirection !== "recvonly" ||
                remoteTrackID(pc, receiver.receiver.track, receiver) !== watched.id) continue;
            if ([...stats.values()].some(stat => stat.type === "inbound-rtp" && stat.kind === "video" &&
                stat.mid === receiver.mid && stat.framesDecoded > 0)) return true;
        }
        await wait(250);
    }
    return false;
}

export function createVideoReceiverRecovery({ state, watch, scheduler = createHardwareRecoveryScheduler(),
    supported = async () => false, capable = hardwareVideoDecodeAvailable, reset = resetVideoReceiver }) {
    const supportRequests = new Map();
    return async (pc, stats) => {
        if (state.pc !== pc || pc.connectionState !== "connected" || pc.signalingState !== "stable") return;
        const scope = scopeKey(state), candidates = [];
        // Never probe an unadvertised recovery path by interrupting playback.
        let supportRequest = supportRequests.get(scope);
        if (!supportRequest) {
            if (supportRequests.size >= 8) return;
            const promise = Promise.resolve().then(() => state.pc === pc && scopeKey(state) === scope ? supported() : false);
            supportRequest = promise;
            supportRequests.set(scope, promise);
            const finish = () => { if (supportRequests.get(scope) === promise) supportRequests.delete(scope); };
            promise.then(finish, finish);
        }
        let enabled;
        try { enabled = await bounded(supportRequest, 1000); } catch { return; }
        if (enabled !== true || state.pc !== pc || scopeKey(state) !== scope) return;
        for (const stat of stats.values()) {
            if (stat.type !== "inbound-rtp" || (stat.kind || stat.mediaType) !== "video") continue;
            const transceiver = pc.getTransceivers().find(item => stat.mid != null ? item.mid === stat.mid : item.receiver.track.id === stat.trackIdentifier);
            if (!transceiver || transceiver.stopped || transceiver.direction !== "recvonly" || transceiver.currentDirection !== "recvonly" || transceiver.sender?.track) continue;
            const track = transceiver.receiver.track, id = remoteTrackID(pc, track, transceiver), watched = watch(pc, id, track);
            const codec = stats.get(stat.codecId);
            if (!watched || !track.enabled || track.readyState !== "live" || codec?.mimeType?.toLowerCase() !== "video/h264") continue;
            const current = () => state.pc === pc && scopeKey(state) === scope && pc.connectionState === "connected" && watched.current();
            const implementation = String(stat.decoderImplementation || "");
            candidates.push({ key: `receive:${watched.key}`, instance: `${stat.id}:${stat.ssrc}`,
                config: JSON.stringify([codec.mimeType, codec.sdpFmtpLine, stat.frameWidth, stat.frameHeight]),
                timestamp: stat.timestamp, frames: stat.framesDecoded,
                receivedFrames: stat.framesReceived, receivedPackets: stat.packetsReceived,
                hardware: stat.powerEfficientDecoder === true && /D3D11|MediaFoundation|VideoToolbox|Vaapi|V4L2|MediaCodec/i.test(implementation),
                software: stat.powerEfficientDecoder === false && /FFmpeg|OpenH264/i.test(implementation),
                current, capable: () => capable(codec, stat), recover: () => reset({ state, pc, transceiver, current, watched }) });
        }
        await scheduler(scope, candidates, stats);
    };
}
