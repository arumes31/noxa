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
        get size() { return entries.size; },
    };
}

const cache = createThumbCache();
const inflight = new Map();

// inlineThumbnail resolves to a cached thumbnail object URL, or null when the
// original should be shown (GIF animation, already under the cap, no stable key).
// It rejects on decode failure; callers then keep the full-size path.
export function inlineThumbnail(key, dataURL, mime) {
    if (!key || mime === "image/gif") return Promise.resolve(null);
    const hit = cache.get(key);
    if (hit !== undefined) return Promise.resolve(hit);
    let request = inflight.get(key);
    if (!request) {
        request = renderThumbnail(dataURL).then(url => { cache.set(key, url); return url; })
            .finally(() => inflight.delete(key));
        inflight.set(key, request);
    }
    return request;
}

async function renderThumbnail(dataURL) {
    const bitmap = await createImageBitmap(await (await fetch(dataURL)).blob());
    try {
        const size = thumbSize(bitmap.width, bitmap.height);
        if (!size) return null;
        const canvas = typeof OffscreenCanvas === "function" ? new OffscreenCanvas(size.width, size.height)
            : Object.assign(document.createElement("canvas"), size);
        canvas.getContext("2d").drawImage(bitmap, 0, 0, size.width, size.height);
        // WebP keeps transparency; engines without a WebP encoder return PNG.
        const blob = canvas.convertToBlob ? await canvas.convertToBlob({ type: "image/webp", quality: 0.85 })
            : await new Promise(resolve => canvas.toBlob(resolve, "image/webp", 0.85));
        if (!blob) throw new Error("thumbnail encode failed");
        return URL.createObjectURL(blob);
    } finally {
        bitmap.close();
    }
}
