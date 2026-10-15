// RTCStats.timestamp uses the performance clock expressed relative to its epoch.
// Date.now uses a separately adjustable wall clock and truncates milliseconds.
export function rtcStatsEpochNow() {
    try {
        const clock = globalThis.performance;
        const origin = clock?.timeOrigin, elapsed = clock?.now();
        const value = origin + elapsed;
        return Number.isFinite(origin) && origin >= 0 && Number.isFinite(elapsed) && elapsed >= 0 &&
            Number.isFinite(value) ? value : NaN;
    } catch { return NaN; }
}
