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
        entry.autoRetries = 0; // a picture refills the automatic retry budget
        return { state, retry: false };
    }
    entry.recoverySince ??= now;
    const persistent = now - entry.recoverySince >= 8000;
    if (state === "waiting" && persistent) state = "waitingLong";
    return { state, retry: !entry.pending && (persistent || !!entry.error) };
}

// A watch stuck without a picture repairs itself with the same request as the
// Retry button: after 15 s without a picture (the button shows from 8 s), at
// most three times per watch; after that the button remains. It only repeats
// a watch the user started and never starts one.
export const STREAM_AUTO_RETRIES = 3;
export function streamAutoRetry(entry, recovery, now = Date.now()) {
    if (!entry.watching) entry.autoRetries = 0;
    if (recovery.state !== "waitingLong" || !recovery.retry || now - entry.recoverySince < 15000 ||
        (entry.autoRetries || 0) >= STREAM_AUTO_RETRIES) return false;
    entry.autoRetries = (entry.autoRetries || 0) + 1;
    return true;
}
