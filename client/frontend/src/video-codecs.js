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

export async function publicationVideoCodecs(track) {
    const codec = capabilities("RTCRtpSender").find(relayH264);
    if (!codec || !globalThis.navigator?.mediaCapabilities?.encodingInfo) return ["vp8"];
    const settings = track.getSettings?.() || {};
    let timeout;
    try {
        const query = navigator.mediaCapabilities.encodingInfo({ type: "webrtc", video: {
            contentType: `${codec.mimeType};${codec.sdpFmtpLine}`, width: settings.width || 1920,
            height: settings.height || 1080, framerate: settings.frameRate || 60, bitrate: 8000000,
        } });
        const result = await Promise.race([query, new Promise(resolve => { timeout = setTimeout(() => resolve({}), 2000); })]);
        return result.supported && result.powerEfficient ? ["h264", "vp8"] : ["vp8"];
    } catch { return ["vp8"]; } finally { clearTimeout(timeout); }
}

export function preferPublicationCodec(transceiver, codec) {
    if (!transceiver?.setCodecPreferences || !["h264", "vp8"].includes(codec)) return false;
    const codecs = capabilities("RTCRtpSender").filter(item => item.mimeType?.toLowerCase() === "video/vp8" || relayH264(item));
    const preferred = `video/${codec}`;
    if (!codecs.some(item => item.mimeType.toLowerCase() === preferred)) throw new Error(`Video codec unavailable: ${codec}`);
    codecs.sort((a, b) => Number(b.mimeType.toLowerCase() === preferred) - Number(a.mimeType.toLowerCase() === preferred));
    // Multiple negotiated formats are alternatives on this ONE encoding.
    transceiver.setCodecPreferences(codecs);
    return true;
}
