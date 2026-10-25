// avatar-crop.js — the math of the avatar editor: which square of the
// source image ends up in the 256×256 avatar for a zoom and pan. The
// editor draws the same square into its preview, so what you see is what
// is uploaded.

export const AVATAR_SIZE = 256;
export const MAX_ZOOM = 4;

/**
 * The source square for an image of width×height at zoom (1 = the largest
 * centred square) moved by (x, y) source pixels from the centre. The
 * offset is clamped so the square never leaves the image.
 * Returns {sx, sy, size, x, y} with the clamped offset.
 */
export function cropSquare(width, height, { zoom = 1, x = 0, y = 0 } = {}) {
    const z = Math.min(MAX_ZOOM, Math.max(1, Number(zoom) || 1));
    const size = Math.min(width, height) / z;
    const maxX = (width - size) / 2, maxY = (height - size) / 2;
    const cx = Math.min(maxX, Math.max(-maxX, x)), cy = Math.min(maxY, Math.max(-maxY, y));
    return { sx: maxX + cx, sy: maxY + cy, size, x: cx, y: cy, zoom: z };
}

/**
 * Moves the crop by a drag of (dx, dy) screen pixels in a view of viewSize
 * pixels: dragging the picture right shows more of its left side.
 */
export function panBy(width, height, crop, dx, dy, viewSize) {
    const { size } = cropSquare(width, height, crop);
    const scale = size / viewSize;
    const next = cropSquare(width, height, { ...crop, x: (crop.x || 0) - dx * scale, y: (crop.y || 0) - dy * scale });
    return { zoom: next.zoom, x: next.x, y: next.y };
}

/** Changes zoom, keeping the offset inside the image. */
export function zoomTo(width, height, crop, zoom) {
    const next = cropSquare(width, height, { ...crop, zoom });
    return { zoom: next.zoom, x: next.x, y: next.y };
}

/** Base64 of the bytes of a data: URL (the AvatarSet payload). */
export function dataURLBase64(url) {
    const comma = url.indexOf(",");
    return comma >= 0 ? url.slice(comma + 1) : "";
}
