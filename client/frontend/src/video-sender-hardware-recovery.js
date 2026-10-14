import { hardwareRecoveryCodec, knownSoftwareVideoEncoder } from "./video-hevc.js";
import { createHardwareRecoveryScheduler } from "./video-hardware-recovery.js";
import { publicationSnapshot, publicationUploadActive, withdrawPublicationHEVC } from "./stream-publication.js";

const scopeKey = state => JSON.stringify([state.activeTabID, state.serverGeneration, state.sessionGeneration, state.myClientID, state.myChannelID]);
const queries = new WeakMap();

// Capture counters are useful only when bound to this exact sender's source.
// They show delivery into WebRTC, not successful hardware encoding.
export function videoSenderCaptureSample(stats, stat, track) {
    if (typeof stat.mediaSourceId !== "string" || !stat.mediaSourceId || typeof track.id !== "string" || !track.id) return null;
    const source = stats.get(stat.mediaSourceId);
    if (!source || source.id !== stat.mediaSourceId || source.type !== "media-source" ||
        (source.kind || source.mediaType) !== "video" || source.trackIdentifier !== track.id ||
        !Number.isFinite(source.timestamp) || !Number.isFinite(stat.timestamp) ||
        source.timestamp > stat.timestamp || stat.timestamp - source.timestamp > 1000 ||
        !Number.isSafeInteger(source.frames) || source.frames < 0) return null;
    return { id: source.id, timestamp: source.timestamp, frames: source.frames };
}

export async function hardwareVideoEncodeAvailable(codec, stat, mediaCapabilities = globalThis.navigator?.mediaCapabilities) {
    if (!mediaCapabilities?.encodingInfo || !codec?.sdpFmtpLine || !(stat.framesPerSecond > 0)) return false;
    try {
        let cache = queries.get(mediaCapabilities);
        if (!cache) { cache = new Map(); queries.set(mediaCapabilities, cache); }
        const video = { contentType: `${codec.mimeType};${codec.sdpFmtpLine}`, width: stat.frameWidth, height: stat.frameHeight,
            framerate: stat.framesPerSecond, bitrate: 8000000 };
        const key = JSON.stringify(video), now = performance.now();
        let entry = cache.get(key);
        if (entry?.settled && entry.expires <= now) { cache.delete(key); entry = null; }
        if (!entry) {
            if (cache.size >= 16) {
                const disposable = [...cache].find(([, value]) => value.settled);
                if (!disposable) return false;
                cache.delete(disposable[0]);
            }
            entry = { settled: false };
            entry.promise = Promise.resolve().then(() => mediaCapabilities.encodingInfo({ type: "webrtc", video }))
                .catch(() => null).then(value => { entry.settled = true; entry.expires = performance.now() + 30000; entry.value = value; return value; });
            let timer;
            entry.wait = Promise.race([entry.promise, new Promise(resolve => { timer = setTimeout(() => resolve(null), 1000); })])
                .finally(() => clearTimeout(timer));
            cache.set(key, entry);
        }
        const result = entry.settled ? entry.value : await entry.wait;
        return result?.supported === true && result.powerEfficient === true;
    } catch { return false; }
}

// Publication identity, rather than an RTP SSRC, owns the retry budget. A source
// or channel change cannot apply a delayed reset to a different user's intent.
export function createVideoSenderRecovery({ state, retry, snapshot = publicationSnapshot, uploadActive = publicationUploadActive,
    scheduler = createHardwareRecoveryScheduler(), capable = hardwareVideoEncodeAvailable, withdraw = withdrawPublicationHEVC }) {
    return async (pc, stats) => {
        if (state.pc !== pc || pc.connectionState !== "connected" || pc.signalingState !== "stable") return;
        const scope = scopeKey(state), candidates = [];
        for (const { publication: p, generation } of snapshot()) {
            const transceiver = pc.getTransceivers().find(item => item.sender.track === p.track);
            const current = () => state.pc === pc && scopeKey(state) === scope && pc.connectionState === "connected" &&
                p.pc === pc && !p.cancelled && p.generation === generation && generation !== "0" &&
                p.scope.tabID === state.activeTabID && p.scope.generation === state.serverGeneration &&
                p.scope.session === state.sessionGeneration && p.scope.clientID === state.myClientID && p.scope.channelID === state.myChannelID &&
                p.track?.readyState === "live" && p.track.enabled && transceiver?.sender.track === p.track && !transceiver.stopped &&
                uploadActive(p.slot, p.track) !== false && snapshot().some(item => item.publication === p && item.generation === generation);
            if (!transceiver || !["cam", "screen"].includes(p.slot) || !current()) continue;
            const parameters = transceiver.sender.getParameters();
            if (parameters.encodings?.length !== 1 || parameters.encodings[0].active === false) continue;
            const rows = [...stats.values()].filter(stat => stat.type === "outbound-rtp" && !stat.isRemote &&
                (stat.kind || stat.mediaType) === "video" && (stat.mid != null ? stat.mid === transceiver.mid :
                    stats.get(stat.mediaSourceId)?.trackIdentifier === p.track.id));
            if (rows.length !== 1) continue;
            const stat = rows[0], codec = stats.get(stat.codecId), settings = p.track.getSettings();
            if (!hardwareRecoveryCodec(codec)) continue;
            const capture = videoSenderCaptureSample(stats, stat, p.track);
            const scale = parameters.encodings[0].scaleResolutionDownBy ?? 1;
            const geometry = Number.isFinite(scale) && scale >= 1 && stat.frameWidth >= 640 && stat.frameHeight >= 360 &&
                stat.frameWidth % 4 === 0 && stat.frameHeight % 4 === 0 &&
                Number.isFinite(settings.width) && Number.isFinite(settings.height) &&
                stat.frameWidth * scale <= settings.width && stat.frameHeight * scale <= settings.height &&
                stat.frameWidth * settings.height === stat.frameHeight * settings.width;
            const recoveryGeometry = codec.mimeType.toLowerCase() === "video/h265" ?
                Number.isSafeInteger(stat.frameWidth) && stat.frameWidth > 0 && Number.isSafeInteger(stat.frameHeight) && stat.frameHeight > 0 &&
                stat.frameWidth <= settings.width && stat.frameHeight <= settings.height : geometry;
            candidates.push({ key: `send:${p.slot}:${generation}`, instance: `${stat.id}:${stat.ssrc}`,
                config: JSON.stringify([codec.mimeType, codec.sdpFmtpLine, stat.frameWidth, stat.frameHeight, capture?.id ?? null]),
                timestamp: stat.timestamp, frames: stat.framesEncoded, capture,
                hardware: stat.powerEfficientEncoder === true && recoveryGeometry,
                software: stat.powerEfficientEncoder === false && knownSoftwareVideoEncoder(codec, stat.encoderImplementation) &&
                    ["none", "cpu", "bandwidth"].includes(stat.qualityLimitationReason) && recoveryGeometry && !state.shareQualityUpdating,
                current, capable: async () => {
                    if (codec.mimeType.toLowerCase() === "video/h265") return true;
                    return capable(codec, stat);
                }, recover: evidence => codec.mimeType.toLowerCase() === "video/h265" ? withdraw(p.slot, p.track, current) : retry(p.slot, p.track, current,
                    evidence?.hardwareObserved && capture ? { sourceId: capture.id, codec: codec.mimeType,
                        fmtp: codec.sdpFmtpLine, width: stat.frameWidth, height: stat.frameHeight } : null) });
        }
        await scheduler(scope, candidates, stats, pc);
    };
}
