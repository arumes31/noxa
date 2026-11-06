// virtual-log.js — windowing maths for the chat log (601). Long conversations
// render only the rows near the viewport plus an overscan margin; spacer
// elements stand in for the rest. Row heights vary (attachments, wrapped
// text), so measured heights are cached per row key and unmeasured rows use
// the running average. Offsets come from a prefix-sum array rebuilt only when
// a height or the row list changes, so a scroll frame costs O(log n) even for
// 100,000 rows. This module is pure: the DOM side lives in chat-ui.js.

// Logs shorter than this render every row, exactly as before virtualization.
export const VIRTUAL_MIN_ROWS = 300;
// Rendered margin above and below the viewport, in pixels.
export const OVERSCAN_PX = 900;
// Height assumed for a row that has never been measured.
export const DEFAULT_ROW_PX = 56;

/** Measured row heights by key, with the average as the estimate. */
export function createHeightCache(fallback = DEFAULT_ROW_PX) {
    const heights = new Map();
    let sum = 0;
    return {
        get(key) {
            const h = heights.get(key);
            return h === undefined ? this.estimate() : h;
        },
        has: key => heights.has(key),
        /** Stores a height; returns true when it changed. */
        set(key, height) {
            if (!(height >= 0) || !Number.isFinite(height)) return false;
            const old = heights.get(key);
            if (old === height) return false;
            if (old !== undefined) sum -= old;
            heights.set(key, height);
            sum += height;
            return true;
        },
        estimate: () => (heights.size ? sum / heights.size : fallback),
        clear() { heights.clear(); sum = 0; },
        get size() { return heights.size; },
    };
}

/** prefix[i] is the offset of row i; prefix[keys.length] is the total. */
export function buildPrefix(keys, cache) {
    const prefix = new Float64Array(keys.length + 1);
    for (let i = 0; i < keys.length; i++) prefix[i + 1] = prefix[i] + cache.get(keys[i]);
    return prefix;
}

/** Index of the row containing offset y (clamped to the last row). */
export function rowAt(prefix, y) {
    const count = prefix.length - 1;
    if (count <= 0) return 0;
    let lo = 0, hi = count - 1;
    while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (prefix[mid] <= y) lo = mid;
        else hi = mid - 1;
    }
    return lo;
}

/**
 * The rows to render for a viewport. scrollTop is relative to the top of the
 * list (not the scroll container). Returns { start, end, before, after } where
 * rows [start, end) are rendered and before/after are the spacer heights.
 */
export function computeWindow({ prefix, scrollTop, viewport, overscan = OVERSCAN_PX }) {
    const count = prefix.length - 1;
    if (count <= 0) return { start: 0, end: 0, before: 0, after: 0 };
    const top = Math.max(0, scrollTop - overscan);
    const bottom = Math.max(top, scrollTop + Math.max(0, viewport) + overscan);
    const start = rowAt(prefix, top);
    let end = rowAt(prefix, bottom) + 1;
    end = Math.min(count, Math.max(end, start + 1));
    return { start, end, before: prefix[start], after: prefix[count] - prefix[end] };
}
