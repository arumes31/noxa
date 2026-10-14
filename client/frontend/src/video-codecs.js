// The relay's H264 option is baseline, packetization-mode 1. Capability names
// describe an interoperable format, not merely the presence of an H264 GPU.
function relayH264(codec) {
    const fmtp = new Map(String(codec.sdpFmtpLine || "").split(";").map(item => item.trim().toLowerCase().split("=")));
    return codec.mimeType?.toLowerCase() === "video/h264" && codec.clockRate === 90000 &&
        fmtp.get("packetization-mode") === "1" && /^4200[0-9a-f]{2}$/.test(fmtp.get("profile-level-id") || "");
}

function capabilities(type) {
    try { return globalThis[type]?.getCapabilities?.("video")?.codecs || []; } catch { return []; }
}

export function receiverVideoCodecs() {
    return capabilities("RTCRtpReceiver").some(relayH264) ? ["h264", "vp8"] : ["vp8"];
}

const publicationQueries = new WeakMap();

function publicationQuery(track) {
    const codec = capabilities("RTCRtpSender").find(relayH264);
    const media = globalThis.navigator?.mediaCapabilities;
    if (!codec || typeof media?.encodingInfo !== "function") return null;
    let settings;
    try { settings = track?.getSettings?.(); } catch { return null; }
    if (![settings?.width, settings?.height].every(value => Number.isInteger(value) && value > 0 && value <= 16384) ||
        !Number.isFinite(settings?.frameRate) || settings.frameRate <= 0 || settings.frameRate > 240) return null;
    const video = { contentType: `${codec.mimeType};${codec.sdpFmtpLine}`, width: settings.width,
        height: settings.height, framerate: settings.frameRate, bitrate: 8000000 };
    return { media, video, key: JSON.stringify(video) };
}

export function publicationCodecQueryKey(track) { return publicationQuery(track)?.key ?? null; }

// Initial selection and later retries share the same bounded native promises.
// A timeout never starts another unresolved query for the same geometry.
export async function publicationCodecSupport(track) {
    const query = publicationQuery(track);
    if (!query) return { codecs: ["vp8"], status: "unavailable", key: null };
    const { media, video, key } = query;
    try {
        let cache = publicationQueries.get(media);
        if (!cache) { cache = new Map(); publicationQueries.set(media, cache); }
        let entry = cache.get(key);
        if (entry?.settled && entry.expires <= Date.now()) { cache.delete(key); entry = null; }
        if (!entry) {
            if (cache.size >= 16) {
                const disposable = [...cache].find(([, value]) => value.settled);
                if (!disposable) return { codecs: ["vp8"], status: "unknown", key };
                cache.delete(disposable[0]);
            }
            entry = { settled: false };
            entry.promise = Promise.resolve().then(() => media.encodingInfo({ type: "webrtc", video }))
                .catch(() => null).then(result => {
                    const status = result?.supported === true && result.powerEfficient === true ? "hardware" :
                        result?.supported === false || result?.powerEfficient === false ? "unsupported" : "unknown";
                    entry.settled = true;
                    entry.status = status;
                    entry.expires = Date.now() + (status === "unsupported" ? 300000 : 30000);
                    return status;
                });
            // One bounded wait belongs to the native query, not each caller.
            // Retries after timeout reuse its settled unknown result instead of
            // attaching new race handlers to a potentially never-settling call.
            let timeout;
            entry.wait = Promise.race([entry.promise,
                new Promise(resolve => { timeout = setTimeout(() => resolve("unknown"), 2000); }),
            ]).finally(() => clearTimeout(timeout));
            cache.set(key, entry);
        }
        // A native result arriving after the JS timeout remains useful. Its TTL
        // still starts at native settlement; it never rewrites earlier replies.
        let status = entry.settled ? entry.status : await entry.wait;
        if (publicationCodecQueryKey(track) !== key) status = "unknown";
        return { codecs: status === "hardware" ? ["h264", "vp8"] : ["vp8"], status, key };
    } catch { return { codecs: ["vp8"], status: "unknown", key }; }
}

export async function publicationVideoCodecs(track) { return (await publicationCodecSupport(track)).codecs; }

export function preferPublicationCodec(transceiver, codec) {
    if (!transceiver?.setCodecPreferences || !["h264", "vp8"].includes(codec)) return false;
    const codecs = capabilities("RTCRtpSender").filter(item => ["video/vp8", "video/rtx"].includes(item.mimeType?.toLowerCase()) || relayH264(item));
    const preferred = `video/${codec}`;
    if (!codecs.some(item => item.mimeType.toLowerCase() === preferred)) throw new Error(`Video codec unavailable: ${codec}`);
    codecs.sort((a, b) => Number(b.mimeType.toLowerCase() === preferred) - Number(a.mimeType.toLowerCase() === preferred));
    // Multiple media formats are alternatives on this ONE encoding. Keep RTX
    // so packet repair is negotiated for the selected format as well.
    transceiver.setCodecPreferences(codecs);
    return true;
}
