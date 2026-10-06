// Animation is a container feature, not a filename extension. Inspect only
// bounded headers in local data URLs; remote/blob bytes remain opaque and are
// conservatively frozen without fetching them again or requiring CORS.
export function animatedChatImageSource(source) {
    if (typeof source !== "string") return false;
    if (/^(?:https?:|blob:|data:image\/(?:gif|apng)[;,])/i.test(source)) return true;
    if (!/^data:image\/(?:png|webp)[;,]/i.test(source)) return false;
    const comma = source.indexOf(",");
    if (comma < 0 || !/;base64$/i.test(source.slice(0, comma))) return true;
    const start = comma + 1, length = source.length - start;
    if (length % 4) return true;
    const byteLength = length / 4 * 3 - (source.endsWith("==") ? 2 : source.endsWith("=") ? 1 : 0);
    function read(offset, count) {
        if (offset < 0 || offset + count > byteLength) throw new RangeError("Truncated image header");
        const block = Math.floor(offset / 3);
        return atob(source.slice(start + block * 4, start + Math.ceil((offset + count) / 3) * 4)).slice(offset % 3, offset % 3 + count);
    }
    try {
        const signature = read(0, Math.min(30, byteLength));
        // WebP extended header: the Animation flag is bit 1 of VP8X flags.
        // https://developers.google.com/speed/webp/docs/riff_container
        if (signature.startsWith("RIFF") && signature.slice(8, 12) === "WEBP") {
            const chunk = signature.slice(12, 16);
            if (chunk === "VP8 " || chunk === "VP8L") return false;
            if (chunk === "VP8X" && signature.length >= 30) return !!(signature.charCodeAt(20) & 2);
            return true;
        }
        // APNG requires acTL before IDAT. Skip chunk payloads by byte length,
        // including large ICC/EXIF data, without decoding/copying those bytes.
        // https://www.w3.org/TR/png-3/#acTL-chunk
        if (signature.slice(0, 8) !== "\x89PNG\r\n\x1a\n") return true;
        let offset = 8;
        for (let i = 0; i < 128; i++) {
            const header = read(offset, 8), type = header.slice(4);
            if (type === "acTL") return true;
            if (type === "IDAT" || type === "IEND") return false;
            const size = ((header.charCodeAt(0) << 24) | (header.charCodeAt(1) << 16) |
                (header.charCodeAt(2) << 8) | header.charCodeAt(3)) >>> 0;
            offset += size + 12;
        }
    } catch {
        // Unknown/invalid headers must not let a decodable animation bypass
        // suspension. The playback controller still requires a decoded image.
    }
    return true;
}
