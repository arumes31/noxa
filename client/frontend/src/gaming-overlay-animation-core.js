// Keep this preset and motion math in sync with gaming_overlay_animation.go.
export const OVERLAY_MOTION_SPEED = 2.4;
export const OVERLAY_COMB_AMPLITUDE = 8;
export const OVERLAY_CONTOUR_GAP = 1.2;

export function overlaySpeakerPhase(id, name) {
    let hash = 2166136261;
    for (const byte of new TextEncoder().encode(id + name)) hash = Math.imul(hash ^ byte, 16777619) >>> 0;
    return (hash % 628) / 100;
}

export function overlayCombActivity(x, seconds, phase = 0) {
    const t = seconds * OVERLAY_MOTION_SPEED + phase;
    return .15 + .55 * (.5 + .5 * Math.sin(t * 4.2 - x * .15)) + .3 * (.5 + .5 * Math.sin(t * 2.1 + x * .072));
}

// Extract glyph edges once, rather than measuring or rasterizing on every frame.
// Fill spaces from the nearest real ink column, without flattening descenders.
export function overlayGlyphContour(alpha, width, height) {
    const top = new Int32Array(width).fill(-1), bottom = new Int32Array(width).fill(-1);
    const nearest = new Int32Array(width).fill(-1);
    let last = -1, end = 0;
    for (let x = 0; x < width; x++) {
        for (let y = 0; y < height; y++) {
            if (alpha[y * width + x] > 48) {
                if (top[x] < 0) top[x] = y;
                bottom[x] = y + 1;
            }
        }
        if (top[x] >= 0) { last = x; end = x + 1; }
        nearest[x] = last;
    }
    last = -1;
    for (let x = width - 1; x >= 0; x--) {
        if (top[x] >= 0) last = x;
        if (last >= 0 && (nearest[x] < 0 || last - x < x - nearest[x])) nearest[x] = last;
    }
    const columns = [];
    for (let x = 0; x < end; x += 2.5) {
        const ink = nearest[Math.min(width - 1, Math.round(x))];
        if (ink >= 0) columns.push({ x, top: top[ink], bottom: bottom[ink] });
    }
    return columns;
}
