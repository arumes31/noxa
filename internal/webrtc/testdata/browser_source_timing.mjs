// Fixed cumulative bins, not exact percentiles. Measuring synchronous canvas
// calls cannot measure deferred raster/GPU work or capture-to-display latency.
export const drawDurationBoundsMS = Object.freeze([1, 2, 4, 8, 16, 33, null]);
export const rafGapBoundsMS = Object.freeze([8, 12, 17, 25, 34, 50, 100, null]);

function observe(bins, bounds, value) {
    const index = bounds.findIndex(bound => bound === null || value < bound);
    bins[index]++;
}

export function createSourceFrameTiming(identity) {
    let generation = 0, previous, counters;
    function reset() {
        previous = undefined;
        counters = { epoch: `${identity}:${generation++}`, raf_opportunities: 0, throttle_skips: 0,
            completed_draws: 0, sync_draw_ms: 0, lifetime_max_sync_draw_ms: 0, lifetime_max_raf_gap_ms: 0,
            sync_draw_bins: drawDurationBoundsMS.map(() => 0), raf_gap_bins: rafGapBoundsMS.map(() => 0) };
    }
    reset();
    return {
        opportunity(now) {
            if (!Number.isFinite(now) || now < 0) { reset(); return; }
            if (previous !== undefined && now < previous) reset();
            if (previous !== undefined) {
                const gap = now - previous;
                observe(counters.raf_gap_bins, rafGapBoundsMS, gap);
                counters.lifetime_max_raf_gap_ms = Math.max(counters.lifetime_max_raf_gap_ms, gap);
            }
            previous = now;
            counters.raf_opportunities++;
        },
        skipped() { counters.throttle_skips++; },
        completed(durationMS) {
            if (!Number.isFinite(durationMS) || durationMS < 0) { reset(); return; }
            counters.completed_draws++;
            counters.sync_draw_ms += durationMS;
            counters.lifetime_max_sync_draw_ms = Math.max(counters.lifetime_max_sync_draw_ms, durationMS);
            observe(counters.sync_draw_bins, drawDurationBoundsMS, durationMS);
        },
        snapshot() { return { ...counters, sync_draw_bins: [...counters.sync_draw_bins], raf_gap_bins: [...counters.raf_gap_bins] }; },
    };
}
