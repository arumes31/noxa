// paste-images.js — clipboard image handling for the composers (529).
// Screenshots arrive as PNG (or BMP); they are re-encoded as high-quality
// WebP (quality 0.92; Chromium's canvas encoder has no lossless mode), which
// is usually a fraction of the size. The result is used only when it saves at
// least 10%. GIF, JPEG and WebP are passed through untouched
// (animation, existing compression). Conversion happens before encryption, so
// the encrypted upload never sees the original.
export const CONVERTIBLE = new Set(["image/png", "image/bmp"]);

/** Files on a paste event, including images offered only as clipboard items. */
export function clipboardFiles(clipboardData, now = Date.now()) {
    const files = [...(clipboardData?.files || [])];
    if (files.length) return files;
    for (const item of [...(clipboardData?.items || [])]) {
        if (item.kind !== "file" || !item.type?.startsWith("image/")) continue;
        const raw = item.getAsFile?.();
        if (!raw) continue;
        const ext = item.type.split("/")[1]?.replace("jpeg", "jpg") || "png";
        files.push(raw.name ? raw : new File([raw], `clipboard-${now}.${ext}`, { type: item.type }));
    }
    return files;
}

const defaultDecode = file => createImageBitmap(file);
const defaultCanvas = (width, height) => new OffscreenCanvas(width, height);

export const WEBP_QUALITY = 0.92;
const MIN_SAVING = 0.9; // keep the WebP only below 90% of the original size

/** High-quality WebP of a pasted PNG/BMP when clearly smaller; otherwise file. */
export async function webpFromPaste(file, { decode = defaultDecode, makeCanvas = defaultCanvas } = {}) {
    if (!CONVERTIBLE.has(file?.type)) return file;
    let bitmap = null;
    try {
        bitmap = await decode(file);
        if (!(bitmap.width > 0 && bitmap.height > 0)) return file;
        const canvas = makeCanvas(bitmap.width, bitmap.height);
        canvas.getContext("2d").drawImage(bitmap, 0, 0);
        const blob = await canvas.convertToBlob({ type: "image/webp", quality: WEBP_QUALITY });
        if (!blob || blob.type !== "image/webp" || blob.size >= file.size * MIN_SAVING) return file;
        const base = (file.name || "clipboard").replace(/\.(png|bmp)$/i, "");
        return new File([blob], `${base}.webp`, { type: "image/webp", lastModified: file.lastModified });
    } catch {
        return file; // an undecodable image is still attached as pasted
    } finally {
        bitmap?.close?.();
    }
}

/** Converts every pasted file that qualifies, preserving order. */
export function preparePastedFiles(files, options) {
    return Promise.all([...files].map(file => webpFromPaste(file, options)));
}
