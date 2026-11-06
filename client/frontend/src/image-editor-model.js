// image-editor-model.js — state and pixel logic for the attachment image
// editor (605), kept free of DOM so it can be unit tested.
//
// An edit is a list of operations. Coordinates are fractions (0..1) of the
// raster the operation applies to, so the same list renders the small
// on-screen preview and the full-resolution export. Rotate and crop change
// the raster; later operations are relative to the raster at that point.

/** Images above this many pixels are exported scaled down to it. */
export const MAX_EXPORT_PIXELS = 50_000_000;
/** The on-screen editing raster is at most this many pixels. */
export const MAX_PREVIEW_PIXELS = 4_000_000;
/** Formats the editor can decode and re-encode (animated GIF is not offered). */
export const EDITABLE_TYPES = Object.freeze(new Set(["image/png", "image/jpeg", "image/webp", "image/bmp"]));
export const ASPECTS = Object.freeze({ free: 0, "1:1": 1, "4:3": 4 / 3, "16:9": 16 / 9, "3:2": 3 / 2 });
export const COLORS = Object.freeze(["#ff3b30", "#ffcc00", "#34c759", "#0a84ff", "#ffffff", "#000000"]);
export const MIN_CROP = 0.02;

const clamp = (value, lo, hi) => Math.min(hi, Math.max(lo, value));

/** MIME type of a data: URL, or "". */
export function dataURLType(url) {
    const match = /^data:([^;,]+)[;,]/.exec(String(url || ""));
    return match ? match[1].toLowerCase() : "";
}

/** The first bytes of a base64 data: URL (enough for container headers). */
function dataURLHead(url, bytes = 512) {
    const comma = String(url || "").indexOf(",");
    if (comma < 0) return new Uint8Array(0);
    try {
        const chunk = url.slice(comma + 1, comma + 1 + Math.ceil(bytes / 3) * 4);
        return Uint8Array.from(atob(chunk), c => c.charCodeAt(0));
    } catch {
        return new Uint8Array(0);
    }
}

const ascii = (bytes, at, length) => String.fromCharCode(...bytes.subarray(at, at + length));

/**
 * Whether image bytes start an animation: a WebP with the VP8X animation
 * flag, or a PNG with an acTL chunk (APNG; acTL must precede the image data).
 */
export function isAnimatedImage(type, bytes) {
    if (type === "image/webp") {
        return bytes.length >= 21 && ascii(bytes, 0, 4) === "RIFF" && ascii(bytes, 8, 4) === "WEBP" &&
            ascii(bytes, 12, 4) === "VP8X" && (bytes[20] & 0x02) !== 0;
    }
    if (type === "image/png") return ascii(bytes, 0, bytes.length).includes("acTL");
    return false;
}

/**
 * Whether a staged attachment can be opened in the editor. Animated images
 * are not offered: re-encoding would silently keep only one frame.
 */
export function isEditable(staged) {
    if (!staged?.dataURL) return false;
    const type = dataURLType(staged.dataURL);
    return EDITABLE_TYPES.has(type) && !isAnimatedImage(type, dataURLHead(staged.dataURL));
}

/** Scale (<= 1) that fits width x height into maxPixels. */
export function fitScale(width, height, maxPixels) {
    const pixels = width * height;
    if (!(pixels > 0)) return 0;
    return pixels <= maxPixels ? 1 : Math.sqrt(maxPixels / pixels);
}

/** A rectangle from two corner points, normalised and clamped to 0..1. */
export function rectFromPoints(a, b) {
    const x1 = clamp(Math.min(a.x, b.x), 0, 1), y1 = clamp(Math.min(a.y, b.y), 0, 1);
    const x2 = clamp(Math.max(a.x, b.x), 0, 1), y2 = clamp(Math.max(a.y, b.y), 0, 1);
    return { x: x1, y: y1, w: x2 - x1, h: y2 - y1 };
}

/**
 * Applies an aspect ratio (width / height in pixels, 0 = free) to a crop
 * rectangle of a raster of rasterAspect (width / height). The rectangle
 * shrinks around its anchor corner and stays inside the raster.
 */
export function constrainCrop(rect, aspect, rasterAspect) {
    let { x, y, w, h } = rect;
    w = clamp(w, 0, 1 - x);
    h = clamp(h, 0, 1 - y);
    if (aspect > 0 && rasterAspect > 0) {
        // In fractional units the target ratio is aspect / rasterAspect.
        const ratio = aspect / rasterAspect;
        if (w / Math.max(h, 1e-9) > ratio) w = h * ratio;
        else h = w / ratio;
        if (x + w > 1) w = 1 - x, h = w / ratio;
        if (y + h > 1) h = 1 - y, w = h * ratio;
    }
    return { x, y, w, h };
}

/**
 * Keyboard adjustment of a crop rectangle: arrows move it, Shift+arrows
 * resize it (right/down grow). step is a fraction of the raster.
 */
export function nudgeCrop(rect, key, { resize = false, step = 0.01, aspect = 0, rasterAspect = 1 } = {}) {
    const dx = key === "ArrowLeft" ? -step : key === "ArrowRight" ? step : 0;
    const dy = key === "ArrowUp" ? -step : key === "ArrowDown" ? step : 0;
    if (!dx && !dy) return rect;
    if (resize) {
        const next = { ...rect, w: clamp(rect.w + dx, MIN_CROP, 1 - rect.x), h: clamp(rect.h + dy, MIN_CROP, 1 - rect.y) };
        if (aspect > 0) {
            // Resize along the axis that was pressed and derive the other.
            const ratio = aspect / rasterAspect;
            if (dx) next.h = next.w / ratio; else next.w = next.h * ratio;
            const locked = constrainCrop(next, aspect, rasterAspect);
            // A step that would push either side under the minimum is ignored.
            return locked.w >= MIN_CROP && locked.h >= MIN_CROP ? locked : rect;
        }
        return next;
    }
    return { ...rect, x: clamp(rect.x + dx, 0, 1 - rect.w), y: clamp(rect.y + dy, 0, 1 - rect.h) };
}

/** Raster size after applying the size-changing operations to width x height. */
export function rasterSize(width, height, ops) {
    let w = width, h = height;
    for (const op of ops) {
        if (op.type === "rotate") [w, h] = [h, w];
        else if (op.type === "crop") {
            w = Math.max(1, Math.round(op.rect.w * w));
            h = Math.max(1, Math.round(op.rect.h * h));
        }
    }
    return { width: w, height: h };
}

/** Undo/redo history of operations. */
export function createHistory(initial = []) {
    let done = [...initial];
    let undone = [];
    return {
        get ops() { return [...done]; },
        get dirty() { return done.length > 0; },
        canUndo: () => done.length > 0,
        canRedo: () => undone.length > 0,
        push(op) { done.push(op); undone = []; },
        undo() { if (done.length) undone.push(done.pop()); return done.length; },
        redo() { if (undone.length) done.push(undone.pop()); return done.length; },
    };
}

/** Below this shorter side, sizes are computed as if the image were this big. */
export const MIN_SIZING_SIDE = 400;

/**
 * Size unit for a render: raster pixels per 1% of the original image's
 * shorter side (at least MIN_SIZING_SIDE, so text and strokes stay legible on
 * small images). Crop and rotate keep it, so the same op has the same size in
 * the small preview and the full-resolution export.
 */
export function sizeUnit(originalWidth, originalHeight, rasterScale) {
    return Math.max(0.01, Math.max(Math.min(originalWidth, originalHeight), MIN_SIZING_SIDE) * rasterScale / 100);
}

/** Pixelation block edge in pixels (2.5 units, at least 2). */
export function pixelBlock(unit) {
    return Math.max(2, Math.round(unit * 2.5));
}

/**
 * Replaces every block of the region with its average colour, in place.
 * image is ImageData-like ({ data, width, height }); the original pixels of
 * the region cannot be recovered from the result.
 */
export function pixelateImageData(image, block) {
    const { data, width, height } = image;
    const size = Math.max(1, Math.floor(block));
    for (let by = 0; by < height; by += size) {
        for (let bx = 0; bx < width; bx += size) {
            const xe = Math.min(width, bx + size), ye = Math.min(height, by + size);
            let r = 0, g = 0, b = 0, a = 0, n = 0;
            for (let y = by; y < ye; y++) {
                for (let x = bx; x < xe; x++) {
                    const i = (y * width + x) * 4;
                    r += data[i]; g += data[i + 1]; b += data[i + 2]; a += data[i + 3]; n++;
                }
            }
            r = Math.round(r / n); g = Math.round(g / n); b = Math.round(b / n); a = Math.round(a / n);
            for (let y = by; y < ye; y++) {
                for (let x = bx; x < xe; x++) {
                    const i = (y * width + x) * 4;
                    data[i] = r; data[i + 1] = g; data[i + 2] = b; data[i + 3] = a;
                }
            }
        }
    }
    return image;
}

/** Pixel rectangle (integers, inside the raster) for a fractional one. */
export function pixelRect(rect, width, height) {
    const x = clamp(Math.floor(rect.x * width), 0, width);
    const y = clamp(Math.floor(rect.y * height), 0, height);
    const x2 = clamp(Math.ceil((rect.x + rect.w) * width), 0, width);
    const y2 = clamp(Math.ceil((rect.y + rect.h) * height), 0, height);
    return { x, y, w: x2 - x, h: y2 - y };
}

/** Stroke width in pixels: 0.6 units, at least one pixel. */
export function strokeWidth(unit) {
    return Math.max(1, unit * 0.6);
}

/**
 * Draws ops on a 2D context. makeCanvas(w, h) returns a scratch canvas; the
 * returned canvas holds the result (rotate and crop return a new one).
 */
export function renderOps(base, ops, makeCanvas, unit = Math.min(base.width, base.height) / 100) {
    let canvas = base;
    for (const op of ops) canvas = applyOp(canvas, op, makeCanvas, unit);
    return canvas;
}

export function applyOp(canvas, op, makeCanvas, unit = Math.min(canvas.width, canvas.height) / 100) {
    const { width, height } = canvas;
    if (op.type === "rotate") {
        const next = makeCanvas(height, width);
        const ctx = next.getContext("2d");
        ctx.translate(height, 0);
        ctx.rotate(Math.PI / 2);
        ctx.drawImage(canvas, 0, 0);
        return next;
    }
    if (op.type === "crop") {
        const r = pixelRect(op.rect, width, height);
        const next = makeCanvas(Math.max(1, r.w), Math.max(1, r.h));
        next.getContext("2d").drawImage(canvas, r.x, r.y, r.w, r.h, 0, 0, r.w, r.h);
        return next;
    }
    const ctx = canvas.getContext("2d");
    if (op.type === "redact") {
        // Opaque fill: nothing of the covered pixels remains.
        const r = pixelRect(op.rect, width, height);
        ctx.save();
        ctx.globalAlpha = 1;
        ctx.globalCompositeOperation = "source-over";
        ctx.fillStyle = "#000000";
        ctx.fillRect(r.x, r.y, r.w, r.h);
        ctx.restore();
        return canvas;
    }
    if (op.type === "pixelate") {
        const r = pixelRect(op.rect, width, height);
        if (r.w > 0 && r.h > 0) {
            const image = ctx.getImageData(r.x, r.y, r.w, r.h);
            ctx.putImageData(pixelateImageData(image, pixelBlock(unit)), r.x, r.y);
        }
        return canvas;
    }
    const px = p => ({ x: p.x * width, y: p.y * height });
    ctx.save();
    ctx.strokeStyle = ctx.fillStyle = op.color || COLORS[0];
    ctx.lineWidth = strokeWidth(unit);
    ctx.lineCap = ctx.lineJoin = "round";
    if (op.type === "pen" && op.points?.length) {
        ctx.beginPath();
        const first = px(op.points[0]);
        ctx.moveTo(first.x, first.y);
        for (const point of op.points.slice(1)) { const p = px(point); ctx.lineTo(p.x, p.y); }
        if (op.points.length === 1) ctx.lineTo(first.x + 0.01, first.y);
        ctx.stroke();
    } else if (op.type === "rect") {
        const r = pixelRect(op.rect, width, height);
        ctx.strokeRect(r.x, r.y, r.w, r.h);
    } else if (op.type === "arrow") {
        const a = px(op.from), b = px(op.to);
        const head = ctx.lineWidth * 4;
        const angle = Math.atan2(b.y - a.y, b.x - a.x);
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.moveTo(b.x, b.y);
        ctx.lineTo(b.x - head * Math.cos(angle - Math.PI / 7), b.y - head * Math.sin(angle - Math.PI / 7));
        ctx.moveTo(b.x, b.y);
        ctx.lineTo(b.x - head * Math.cos(angle + Math.PI / 7), b.y - head * Math.sin(angle + Math.PI / 7));
        ctx.stroke();
    } else if (op.type === "text" && op.text) {
        const size = Math.max(4, Math.round(unit * 4.5));
        ctx.font = `600 ${size}px sans-serif`;
        ctx.textBaseline = "top";
        const at = px(op.at);
        // A thin outline keeps the text readable on any background without
        // swallowing the fill.
        ctx.lineWidth = Math.max(1, size / 12);
        ctx.strokeStyle = op.color === "#000000" ? "#ffffff" : "#000000";
        ctx.strokeText(op.text, at.x, at.y);
        ctx.fillText(op.text, at.x, at.y);
    }
    ctx.restore();
    return canvas;
}

/**
 * Output format for an edit: photos stay JPEG (quality 0.92), everything else
 * is PNG, and a PNG is offered as WebP when that is clearly smaller.
 */
export function exportPlan(sourceType) {
    if (sourceType === "image/jpeg") return { type: "image/jpeg", quality: 0.92, tryWebP: false };
    if (sourceType === "image/webp") return { type: "image/webp", quality: 0.92, tryWebP: false };
    return { type: "image/png", quality: undefined, tryWebP: true };
}

const EXTENSIONS = { "image/png": "png", "image/jpeg": "jpg", "image/webp": "webp" };

/** The staged name with the extension of the exported type. */
export function exportName(name, type) {
    const ext = EXTENSIONS[type] || "png";
    const base = String(name || "image").replace(/\.[^.]*$/, "") || "image";
    return `${base}.${ext}`;
}

/** Thrown when the edited image cannot be encoded under the size limit. */
export class ExportTooLargeError extends Error {}

/**
 * Renders ops at full resolution (bounded to maxPixels) and encodes the
 * result. source is anything drawImage accepts with width x height pixels;
 * encode(canvas, type, quality) resolves with a Blob. The output is always a
 * fresh encoding of flattened pixels, so no metadata of the original (EXIF,
 * GPS) and no pre-redaction pixels survive.
 */
export async function exportEdited({ source, width, height, sourceType, ops, makeCanvas, encode, maxBytes, maxPixels = MAX_EXPORT_PIXELS, webpQuality = 0.92 }) {
    const scale = fitScale(width, height, maxPixels);
    // Rounded down, so the integer size never exceeds maxPixels.
    const w = Math.max(1, scale < 1 ? Math.floor(width * scale) : width);
    const h = Math.max(1, scale < 1 ? Math.floor(height * scale) : height);
    const base = makeCanvas(w, h);
    base.getContext("2d").drawImage(source, 0, 0, w, h);
    const canvas = renderOps(base, ops, makeCanvas, sizeUnit(width, height, w / width));
    const plan = exportPlan(sourceType);
    let blob = await encode(canvas, plan.type, plan.quality);
    if (plan.tryWebP) {
        const webp = await encode(canvas, "image/webp", webpQuality).catch(() => null);
        if (webp?.type === "image/webp" && webp.size < blob.size * 0.9) blob = webp;
    }
    if (maxBytes && blob.size > maxBytes) {
        // A lossy fallback before giving up: large PNG screenshots usually fit.
        const lossy = await encode(canvas, "image/webp", 0.85).catch(() => null);
        if (lossy?.type === "image/webp" && lossy.size <= maxBytes) blob = lossy;
        else throw new ExportTooLargeError("edited image is too large");
    }
    return { blob, width: canvas.width, height: canvas.height };
}
