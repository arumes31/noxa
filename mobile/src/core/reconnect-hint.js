// reconnect-hint.js — the reconnect jitter a scheduled shutdown announces
// (#52). The first reconnect after the restart waits a random part of it so
// that clients do not all return in the same second.

let jitterSeconds = 0;

/** Remembers the window from a server_shutdown_scheduled event. */
export function noteShutdownJitter(seconds) {
    const value = Math.round(Number(seconds) || 0);
    jitterSeconds = Math.min(60, Math.max(0, value));
}

/** A random delay within the remembered window, used once. */
export function takeShutdownJitterMs(random = Math.random) {
    const seconds = jitterSeconds;
    jitterSeconds = 0;
    return Math.floor(random() * seconds * 1000);
}
