// studio-assets.js — local PNG/JPEG image sources for the Studio.
//
// Only bytes the user picked from a local file are accepted. They are sniffed
// (never trusted by name or MIME type), bounded in size and declared
// dimensions BEFORE decoding, decoded to an ImageBitmap, and checked again
// after decoding. SVG, GIF, WebP, HTML or anything else is refused. No URL is
// ever fetched and no object URL is created: canvases draw the bitmap.
// Persisted copies live in a bounded IndexedDB store and are revalidated on
// load like any untrusted input.

export const MAX_IMAGE_BYTES = 8 * 1024 * 1024;
export const MAX_IMAGE_DIMENSION = 4096;
export const MAX_IMAGE_PIXELS = 4096 * 4096;
export const MAX_STORED_ASSETS = 16;
export const MAX_STORED_BYTES = 32 * 1024 * 1024;

export class StudioImageError extends Error {
    constructor(code) {
        super(code);
        this.name = "StudioImageError";
        this.code = code;
    }
}

const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
// JPEG start-of-frame markers that carry the picture size.
const JPEG_SOF = new Set([0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf]);

function checkDimensions(width, height) {
    if (!(Number.isInteger(width) && Number.isInteger(height) && width > 0 && height > 0)) throw new StudioImageError("image_invalid");
    if (width > MAX_IMAGE_DIMENSION || height > MAX_IMAGE_DIMENSION || width * height > MAX_IMAGE_PIXELS) throw new StudioImageError("image_too_large");
}

function pngInfo(bytes) {
    if (bytes.length < 33 || !PNG_SIGNATURE.every((value, i) => bytes[i] === value)) return null;
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    // The first chunk must be IHDR (13 bytes): width, height.
    if (view.getUint32(8) !== 13 || String.fromCharCode(...bytes.subarray(12, 16)) !== "IHDR") throw new StudioImageError("image_invalid");
    return { type: "image/png", width: view.getUint32(16), height: view.getUint32(20) };
}

function jpegInfo(bytes) {
    if (bytes.length < 4 || bytes[0] !== 0xff || bytes[1] !== 0xd8 || bytes[2] !== 0xff) return null;
    let offset = 2;
    // Walk marker segments up to the first frame header; bounded by the input.
    while (offset + 4 <= bytes.length) {
        if (bytes[offset] !== 0xff) throw new StudioImageError("image_invalid");
        const marker = bytes[offset + 1];
        if (marker === 0xff) { offset += 1; continue; }
        if (marker === 0xd8 || marker === 0x01 || (marker >= 0xd0 && marker <= 0xd7)) { offset += 2; continue; }
        if (marker === 0xd9 || marker === 0xda) break;
        const length = (bytes[offset + 2] << 8) | bytes[offset + 3];
        if (length < 2 || offset + 2 + length > bytes.length) throw new StudioImageError("image_invalid");
        if (JPEG_SOF.has(marker)) {
            if (length < 7) throw new StudioImageError("image_invalid");
            const height = (bytes[offset + 5] << 8) | bytes[offset + 6];
            const width = (bytes[offset + 7] << 8) | bytes[offset + 8];
            return { type: "image/jpeg", width, height };
        }
        offset += 2 + length;
    }
    throw new StudioImageError("image_invalid");
}

// inspectImageBytes validates untrusted bytes without decoding them.
export function inspectImageBytes(input) {
    const bytes = input instanceof Uint8Array ? input : new Uint8Array(input || []);
    if (bytes.length === 0) throw new StudioImageError("image_invalid");
    if (bytes.length > MAX_IMAGE_BYTES) throw new StudioImageError("image_too_large");
    const info = pngInfo(bytes) || jpegInfo(bytes);
    if (!info) throw new StudioImageError("image_type");
    checkDimensions(info.width, info.height);
    return info;
}

async function assetID(bytes) {
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    return "a-" + [...digest.subarray(0, 16)].map(value => value.toString(16).padStart(2, "0")).join("");
}

// decodeImage turns validated bytes into an ImageBitmap whose decoded size
// must match the declared header and the limits.
export async function decodeImage(bytes, { createBitmap = globalThis.createImageBitmap } = {}) {
    const info = inspectImageBytes(bytes);
    let bitmap;
    try { bitmap = await createBitmap(new Blob([bytes], { type: info.type })); }
    catch { throw new StudioImageError("image_invalid"); }
    // The decoded size must equal the declared header. A JPEG may decode
    // rotated by its EXIF orientation (5-8, most phone photos): width and
    // height swapped, the same bounded pixel count.
    const exact = bitmap.width === info.width && bitmap.height === info.height;
    const rotated = info.type === "image/jpeg" && bitmap.width === info.height && bitmap.height === info.width;
    if (!exact && !rotated) {
        bitmap.close?.();
        throw new StudioImageError("image_invalid");
    }
    return { ...info, width: bitmap.width, height: bitmap.height, bitmap };
}

function openStore(indexedDB, name) {
    return new Promise((resolve, reject) => {
        const request = indexedDB.open(name, 1);
        request.onupgradeneeded = () => request.result.createObjectStore("assets");
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
    });
}

const complete = request => new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
});

// createAssetStore owns decoded bitmaps for one Studio engine. dispose()
// closes every bitmap (GPU/memory); persisted bytes stay for the next start.
export function createAssetStore({ indexedDB = globalThis.indexedDB, name = "noxa-studio", createBitmap } = {}) {
    const loaded = new Map(); // id -> { bitmap, type, width, height, bytes }
    // Added this session and not yet seen referenced by a prune: protected
    // from a prune that raced the add (the source is committed right after).
    const fresh = new Set();
    let db = null;
    let disposed = false;
    const database = async () => {
        if (!indexedDB) return null;
        try { db ||= await openStore(indexedDB, name); } catch { db = null; }
        return db;
    };
    async function stored() {
        const handle = await database();
        if (!handle) return [];
        const tx = handle.transaction("assets", "readonly");
        const keys = await complete(tx.objectStore("assets").getAllKeys());
        return keys;
    }
    async function persist(id, type, bytes) {
        const handle = await database();
        if (!handle) return false;
        const keys = await stored();
        let total = 0;
        const tx = handle.transaction("assets", "readonly");
        const values = await complete(tx.objectStore("assets").getAll());
        for (const value of values) total += value?.bytes?.byteLength || 0;
        if (!keys.includes(id) && (keys.length >= MAX_STORED_ASSETS || total + bytes.byteLength > MAX_STORED_BYTES)) throw new StudioImageError("image_storage_full");
        const write = handle.transaction("assets", "readwrite");
        write.objectStore("assets").put({ type, bytes: bytes.slice().buffer }, id);
        await new Promise((resolve, reject) => { write.oncomplete = resolve; write.onerror = () => reject(write.error); });
        return true;
    }
    return {
        // add validates, decodes and stores a picked local file's bytes.
        async add(input) {
            if (disposed) throw new StudioImageError("image_invalid");
            const bytes = input instanceof Uint8Array ? input : new Uint8Array(input);
            inspectImageBytes(bytes);
            const id = await assetID(bytes);
            if (!loaded.has(id)) {
                const decoded = await decodeImage(bytes, { createBitmap });
                if (disposed) { decoded.bitmap.close?.(); throw new StudioImageError("image_invalid"); }
                await persist(id, decoded.type, bytes).catch(error => {
                    decoded.bitmap.close?.();
                    throw error;
                });
                loaded.set(id, { ...decoded, bytes: bytes.byteLength });
            }
            fresh.add(id);
            const entry = loaded.get(id);
            return { id, width: entry.width, height: entry.height };
        },
        // load restores persisted assets referenced by the layout.
        async load(ids) {
            const handle = await database();
            if (!handle) return;
            for (const id of ids) {
                if (loaded.has(id) || disposed) continue;
                const tx = handle.transaction("assets", "readonly");
                const value = await complete(tx.objectStore("assets").get(id)).catch(() => null);
                if (!value?.bytes) continue;
                try {
                    const bytes = new Uint8Array(value.bytes);
                    if (await assetID(bytes) !== id) continue;
                    const decoded = await decodeImage(bytes, { createBitmap });
                    if (disposed) { decoded.bitmap.close?.(); return; }
                    loaded.set(id, { ...decoded, bytes: bytes.byteLength });
                } catch { /* An invalid stored asset stays neutral. */ }
            }
        },
        bitmap(id) { return loaded.get(id)?.bitmap || null; },
        // settle ends the in-flight protection of an added asset once its
        // source was committed (or the commit failed).
        settle(id) { fresh.delete(id); },
        info(id) { const entry = loaded.get(id); return entry ? { width: entry.width, height: entry.height, type: entry.type } : null; },
        loadedCount() { return loaded.size; },
        // prune removes stored and loaded assets no layout references.
        // `keep` is evaluated after the asynchronous listing (a function), so
        // an image added meanwhile is never deleted; fresh adds are protected.
        async prune(keep) {
            const current = () => { const set = typeof keep === "function" ? keep() : keep; for (const id of set) fresh.delete(id); return set; };
            const unusedLoaded = current();
            for (const [id, entry] of loaded) if (!unusedLoaded.has(id) && !fresh.has(id)) { entry.bitmap.close?.(); loaded.delete(id); }
            const handle = await database();
            if (!handle || disposed) return;
            const keys = await stored();
            if (disposed) return;
            const keepNow = current();
            const unused = keys.filter(id => !keepNow.has(id) && !fresh.has(id));
            if (!unused.length) return;
            const tx = handle.transaction("assets", "readwrite");
            for (const id of unused) tx.objectStore("assets").delete(id);
            await new Promise(resolve => { tx.oncomplete = resolve; tx.onerror = resolve; });
        },
        dispose() {
            disposed = true;
            for (const entry of loaded.values()) entry.bitmap.close?.();
            loaded.clear();
            db?.close();
            db = null;
        },
    };
}
