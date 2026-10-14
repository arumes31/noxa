// Main 8-bit, Main tier, single-RTP-stream transport without DONL. Unknown
// parameter semantics fail closed; native capabilities are not a GPU guarantee.
const levels = new Set([30, 60, 63, 90, 93, 120, 123, 150, 153, 156, 180, 183, 186]);
export function hevcFormat(codec) {
    if (codec?.mimeType?.toLowerCase() !== "video/h265" || codec.clockRate !== 90000 ||
        typeof codec.sdpFmtpLine !== "string" || codec.sdpFmtpLine.length > 512) return null;
    const parameters = new Map();
    for (const item of codec.sdpFmtpLine.split(";")) {
        const match = /^\s*([a-zA-Z0-9-]+)\s*=\s*([^;=]+?)\s*$/.exec(item);
        if (!match) return null;
        const key = match[1].toLowerCase();
        if (parameters.has(key)) return null;
        parameters.set(key, match[2]);
    }
    const allowed = new Set(["profile-id", "tier-flag", "level-id", "tx-mode", "sprop-max-don-diff", "interop-constraints"]);
    if ([...parameters.keys()].some(key => !allowed.has(key)) ||
        (parameters.get("profile-id") ?? "1") !== "1" || (parameters.get("tier-flag") ?? "0") !== "0" ||
        (parameters.get("tx-mode") ?? "SRST") !== "SRST" || (parameters.get("sprop-max-don-diff") ?? "0") !== "0" ||
        parameters.has("interop-constraints") && !/^[0-9a-fA-F]{12}$/.test(parameters.get("interop-constraints"))) return null;
    const raw = parameters.get("level-id");
    if (!/^[1-9][0-9]{1,2}$/.test(raw || "") || !levels.has(Number(raw))) return null;
    return { level: Number(raw) };
}

export function hevcFormats(codecs) {
    // Advertise this exact highest native level; server intersects it with all
    // receiver ceilings. No codec preference is rewritten to a fictitious level.
    return codecs.filter(codec => hevcFormat(codec)).filter((codec, index, all) => all.findIndex(item => item.sdpFmtpLine === codec.sdpFmtpLine) === index)
        .sort((a, b) => hevcFormat(b).level - hevcFormat(a).level).slice(0, 4);
}

export function preferredHEVCFormat(codecs) { return hevcFormats(codecs)[0] ?? null; }

const decodeQueries = new WeakMap();
async function receiverHEVCFormatSupport(format) {
    let media;
    try {
        media = globalThis.navigator?.mediaCapabilities;
    } catch { return null; }
    if (!format || typeof media?.decodingInfo !== "function") return null;
    const video = { contentType: `${format.mimeType};${format.sdpFmtpLine}`, width: 1920, height: 1080, framerate: 60, bitrate: 8000000 };
    const key = JSON.stringify(video);
    let cache = decodeQueries.get(media);
    if (!cache) { cache = new Map(); decodeQueries.set(media, cache); }
    let entry = cache.get(key);
    if (entry?.settled && entry.expires <= Date.now()) { cache.delete(key); entry = null; }
    if (!entry) {
        if (cache.size >= 16) {
            const settled = [...cache].find(([, value]) => value.settled);
            if (!settled) return null;
            cache.delete(settled[0]);
        }
        entry = { settled: false };
        const native = Promise.resolve().then(() => media.decodingInfo({ type: "webrtc", video })).catch(() => null).then(result => {
            entry.result = result?.supported === true && result.powerEfficient === true;
            entry.settled = true; entry.expires = Date.now() + (entry.result ? 30000 : 300000);
            return entry.result;
        });
        let timer;
        entry.wait = Promise.race([native, new Promise(resolve => { timer = setTimeout(() => resolve(false), 2000); })])
            .finally(() => clearTimeout(timer));
        cache.set(key, entry);
    }
    const supported = entry.settled ? entry.result : await entry.wait;
    let current;
    try { current = hevcFormats(globalThis.RTCRtpReceiver?.getCapabilities?.("video")?.codecs || []); } catch { return null; }
    return supported && current.some(item => item.sdpFmtpLine === format.sdpFmtpLine) ? { hevc_level_id: hevcFormat(format).level } : null;
}

// A lower advertised Main level may have hardware even when the highest
// format does not. Inspect at most four formats with the same bounded cache.
export async function receiverHEVCSupport() {
    let formats;
    try { formats = hevcFormats(globalThis.RTCRtpReceiver?.getCapabilities?.("video")?.codecs || []); } catch { return null; }
    return (await Promise.all(formats.map(receiverHEVCFormatSupport))).find(Boolean) ?? null;
}

export function hardwareRecoveryCodec(codec) {
    return codec?.mimeType?.toLowerCase() === "video/h264" || !!hevcFormat(codec);
}
export function knownSoftwareVideoEncoder(codec, implementation) {
    const name = String(implementation || "").split(/\s*\(fallback from:/i)[0].trim();
    return codec?.mimeType?.toLowerCase() === "video/h264" ? /^openh264$/i.test(name) :
        !!hevcFormat(codec) && /^(?:FFmpeg|libx265|x265)$/i.test(name);
}


// A known software fallback permits only three automatic HEVC re-admissions
// in this source/watch lifetime. A fresh source or watch owns a fresh budget.
export function hevcReadmissionDelay(attempts) {
    return [300000, 900000, 3600000][attempts] ?? null;
}
