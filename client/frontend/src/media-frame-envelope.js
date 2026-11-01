// Layout checks around the pinned LiveKit frame cipher. H.264 encryption
// normalizes Annex-B header start codes and escapes its encrypted tail.
// Raw byte lengths therefore cannot prove an H.264 transform succeeded.
const MAX_FRAME = 16 * 1024 * 1024;
const MAX_HEADER = 64 * 1024;
export function frameMime(frame) {
    try { return String(frame.getMetadata?.().mimeType || "").toLowerCase(); } catch { return ""; }
}
function startCode(bytes, i) {
    if (bytes[i] !== 0 || bytes[i + 1] !== 0) return 0;
    if (bytes[i + 2] === 1) return 3;
    return bytes[i + 2] === 0 && bytes[i + 3] === 1 ? 4 : 0;
}
// Media Foundation emits temporal-only SVC prefix NALs even for baseline
// H264. Keep them authenticated, but do not admit spatial/MVC extensions.
export function h264TemporalPrefix(nal) {
    return nal.length >= 5 && nal.length <= 16 && !(nal[0] & 0x80) && (nal[0] & 31) === 14 &&
        !!(nal[1] & 0x80) && nal[2] === 0x80 && !(nal[3] & 0x10) &&
        (nal[3] & 3) === 3 && (nal[3] >> 5) <= 2;
}
export function h264ClearHeader(bytes) {
    let offset = 0, normalized = 0, nals = 0, prefix = null;
    while (offset < bytes.length && offset <= MAX_HEADER && nals++ < 128) {
        const code = startCode(bytes, offset);
        if (!code) return null;
        const nal = offset + code;
        if (nal + 1 >= bytes.length || bytes[nal] & 0x80) return null;
        const type = bytes[nal] & 31;
        if (type === 1 || type === 5) {
            if (prefix && !!(prefix[1] & 0x40) !== (type === 5)) return null;
            return { length: nal + 2, normalized: normalized + 4 + 2 };
        }
        if (![6, 7, 8, 9, 12, 14].includes(type)) return null;
        let end = nal + 1;
        while (end < bytes.length && !startCode(bytes, end)) end++;
        if (end > MAX_HEADER) return null;
        if (type === 14) {
            if (prefix || !h264TemporalPrefix(bytes.subarray(nal, end))) return null;
            prefix = bytes.subarray(nal, end);
        }
        normalized += 4 + end - nal;
        offset = end;
    }
    return null;
}
function unescapeTail(bytes) {
    // Only the encrypted tail is RBSP-escaped by LiveKit. Never unescape a
    // plaintext slice or the authenticated SPS/PPS/header prefix.
    const out = new Uint8Array(bytes.length); let length = 0;
    for (let i = 0; i < bytes.length; i++) {
        if (i >= 2 && bytes[i] === 3 && bytes[i - 1] === 0 && bytes[i - 2] === 0 && i + 1 < bytes.length && bytes[i + 1] <= 3) continue;
        out[length++] = bytes[i];
    }
    return out.subarray(0, length);
}
export function mediaFrameEnvelope(frame, encrypted = false) {
    const bytes = new Uint8Array(frame.data);
    if (!bytes.length || bytes.length > MAX_FRAME) return null;
    if (frameMime(frame) !== "video/h264") return { size: bytes.length, tail: bytes };
    const header = h264ClearHeader(bytes);
    if (!header) return null;
    const raw = bytes.subarray(header.length);
    const tail = encrypted ? unescapeTail(raw) : raw;
    return { size: header.normalized + tail.length, tail };
}
