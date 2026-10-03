// A static screen is healthy once decoded. Only the grid's camera diagnostics
// (or explicit track state) may classify an existing picture as stalled.
export function streamRecovery(entry, track, now = Date.now()) {
    if (entry.available === false) return { state: "stopped", retry: false };
    if (!entry.watching) return { state: "idle", retry: false };
    let state = entry.receiverEnded || track?.readyState === "ended" ? "ended"
        : !track || track.muted ? "waiting"
        : entry.tile?.dataset.streamState || "waiting";
    if (state === "ended") return { state, retry: false };
    if (state === "live" || state === "paused") {
        entry.recoverySince = null;
        return { state, retry: false };
    }
    entry.recoverySince ??= now;
    const persistent = now - entry.recoverySince >= 8000;
    if (state === "waiting" && persistent) state = "waitingLong";
    return { state, retry: !entry.pending && (persistent || !!entry.error) };
}
