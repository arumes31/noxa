// Inline chat images render as a capped thumbnail so a 6 MB photo is not
// decoded at full size for every visible message; the viewer opens the original.
export const THUMB_MAX_SIDE = 640;
export const THUMB_CACHE_MAX = 100;

// thumbSize scales the longest side down to max, keeping the aspect ratio.
// null means the image is already small enough to show as is.
export function thumbSize(width, height, max = THUMB_MAX_SIDE) {
    const longest = Math.max(width, height);
    if (!(longest > max)) return null;
    const scale = max / longest;
    return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) };
}

// An LRU of object URLs (or null for "no thumbnail needed"); evicted URLs are revoked.
export function createThumbCache(max = THUMB_CACHE_MAX, revoke = url => URL.revokeObjectURL(url)) {
    const entries = new Map();
    return {
        get(key) {
            if (!entries.has(key)) return undefined;
            const value = entries.get(key);
            entries.delete(key);
            entries.set(key, value);
            return value;
        },
        set(key, value) {
            const old = entries.get(key);
            if (old && old !== value) revoke(old);
            entries.delete(key);
            entries.set(key, value);
            while (entries.size > max) {
                const [oldest, url] = entries.entries().next().value;
                entries.delete(oldest);
                if (url) revoke(url);
            }
        },
        clear() {
            for (const url of entries.values()) if (url) revoke(url);
            entries.clear();
        },
        get size() { return entries.size; },
    };
}

// An animated WebP is an extended (VP8X) file with the animation flag (bit 1)
// set in the VP8X flags byte at offset 20; an ICC profile can push the ANIM
// chunk itself far past any fixed prefix, so the flag is the reliable signal.
export function animatedWebP(bytes) {
    if (bytes.length < 21) return false;
    const text = String.fromCharCode(...bytes.subarray(0, 16));
    return text.startsWith("RIFF") && text.slice(8, 12) === "WEBP" && text.slice(12, 16) === "VP8X" && (bytes[20] & 0x02) !== 0;
}

// An animated PNG has an acTL chunk before its first IDAT. Scans the chunk
// headers in the given prefix: true for acTL, false at IDAT, and null when the
// prefix ends before either (a large ancillary chunk can push acTL past it).
export function animatedPNG(bytes) {
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    for (let at = 8; at + 8 <= bytes.length;) {
        const type = String.fromCharCode(...bytes.subarray(at + 4, at + 8));
        if (type === "acTL") return true;
        if (type === "IDAT") return false;
        at += 12 + view.getUint32(at);
    }
    return null;
}

const cache = createThumbCache();
const SKIPPED = Symbol("skipped");
const inflight = new Map();
let generation = 0;

// Full-resolution decodes are the expensive part: at most two run at once.
const RENDER_CONCURRENCY = 2;
const renderQueue = [];
let rendering = 0;

function limitRender(task) {
    return new Promise((resolve, reject) => {
        const run = () => {
            rendering++;
            Promise.resolve().then(task).then(resolve, reject).finally(() => {
                rendering--;
                renderQueue.shift()?.();
            });
        };
        if (rendering < RENDER_CONCURRENCY) run();
        else renderQueue.push(run);
    });
}

// Thumbnails hold decrypted image data: drop them all on a server, tab or
// identity reset. Renders still in flight from before are discarded.
export function clearInlineThumbnails() {
    generation++;
    inflight.clear();
    cache.clear();
}

// inlineThumbnail resolves to a cached thumbnail object URL, or null when the
// original should be shown (GIF, animated WebP or PNG, already under the cap, no
// stable key). It rejects on decode failure; callers keep the full-size path.
// A queued render whose every caller is no longer current (isCurrent) is
// skipped without decoding and resolves to null.
export function inlineThumbnail(key, dataURL, mime, isCurrent = () => true) {
    if (!key || mime === "image/gif") return Promise.resolve(null);
    const hit = cache.get(key);
    if (hit !== undefined) return Promise.resolve(hit);
    const pending = inflight.get(key);
    if (pending) {
        pending.waiters.add(isCurrent);
        return pending.request;
    }
    const started = generation, waiters = new Set([isCurrent]);
    const stale = () => started !== generation;
    const wanted = () => !stale() && [...waiters].some(current => current());
    const entry = { waiters, request: null };
    entry.request = limitRender(() => wanted() ? renderThumbnail(dataURL, mime) : SKIPPED).then(url => {
        if (url === SKIPPED) return null; // not cached: a later view renders it
        if (stale()) {
            if (url) URL.revokeObjectURL(url);
            return null;
        }
        cache.set(key, url);
        return url;
    }).finally(() => { if (inflight.get(key) === entry) inflight.delete(key); });
    inflight.set(key, entry);
    return entry.request;
}

const PNG_SCAN_BYTES = 4096;

async function renderThumbnail(dataURL, mime) {
    const source = await (await fetch(dataURL)).blob();
    if (mime === "image/webp" && animatedWebP(new Uint8Array(await source.slice(0, 21).arrayBuffer()))) return null;
    if (mime === "image/png") {
        const animated = animatedPNG(new Uint8Array(await source.slice(0, PNG_SCAN_BYTES).arrayBuffer()));
        // An incomplete scan of a larger file cannot rule animation out: keep the original.
        if (animated === true || (animated === null && source.size > PNG_SCAN_BYTES)) return null;
    }
    const bitmap = await createImageBitmap(source);
    let canvas;
    try {
        const size = thumbSize(bitmap.width, bitmap.height);
        if (!size) return null;
        canvas = typeof OffscreenCanvas === "function" ? new OffscreenCanvas(size.width, size.height)
            : Object.assign(document.createElement("canvas"), size);
        canvas.getContext("2d").drawImage(bitmap, 0, 0, size.width, size.height);
    } finally {
        bitmap.close(); // free the full-resolution decode before encoding
    }
    if (!canvas) return null;
    // WebP keeps transparency; engines without a WebP encoder return PNG.
    const blob = canvas.convertToBlob ? await canvas.convertToBlob({ type: "image/webp", quality: 0.85 })
        : await new Promise(resolve => canvas.toBlob(resolve, "image/webp", 0.85));
    if (!blob) throw new Error("thumbnail encode failed");
    return URL.createObjectURL(blob);
}
