// reconnect-hint.js — the reconnect jitter a scheduled shutdown announces
// (#52). The first reconnect after the restart waits a random part of it so
// that clients do not all return in the same second. A hint is used once,
// cleared by a successful connection, and expires a few minutes after the
// announced shutdown so it cannot delay an unrelated later reconnect.

let hint = null; // { seconds, expires }

/** Remembers the window from a server_shutdown_scheduled event. */
export function noteShutdownJitter(seconds, shutdownIn = 600, now = Date.now()) {
    const value = Math.min(60, Math.max(0, Math.round(Number(seconds) || 0)));
    const until = Math.min(600, Math.max(1, Math.round(Number(shutdownIn) || 0)));
    hint = value ? { seconds: value, expires: now + (until + 300) * 1000 } : null;
}

/** Forgets a pending hint (the connection came back without it). */
export function clearShutdownJitter() {
    hint = null;
}

/** A random delay within the remembered window, used once. */
export function takeShutdownJitterMs(random = Math.random, now = Date.now()) {
    const current = hint;
    hint = null;
    if (!current || current.expires <= now) return 0;
    return Math.floor(random() * current.seconds * 1000);
}
