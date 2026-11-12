// disappearing.js — disappearing direct messages (plan item 28): the timers
// offered and the parsing of the encrypted body fields. Shared by the
// direct-message core and the UI. Mirrors client/dm_disappearing.go and the
// desktop's dm-disappearing.js.

/** Timers offered (seconds); 0 = off. */
export const DISAPPEAR_OPTIONS = Object.freeze([0, 30, 300, 3600, 86400, 604800]);
/** Largest TTL accepted from a sender (four weeks). */
export const DISAPPEAR_MAX_TTL = 28 * 24 * 3600;

export const validDisappearTTL = (value) => Number.isSafeInteger(value) && value > 0 && value <= DISAPPEAR_MAX_TTL;

/**
 * Disappearing fields of a decrypted body: the message TTL in seconds, a
 * setting change (timer, 0 = off, else null) and the expired marker of a
 * redacted archive record. Invalid values are ignored.
 */
export function disappearingFromBody(body) {
    const b = body && typeof body === "object" ? body : {};
    return {
        ttl: validDisappearTTL(b.ttl) ? b.ttl : 0,
        timer: b.disappearing === 0 || validDisappearTTL(b.disappearing) ? b.disappearing : null,
        expired: b.expired === true,
    };
}

/** Compact duration ("3d", "4h", "12m", "45s") for a countdown. */
export function durationLabel(seconds) {
    const s = Math.max(0, Math.ceil(Number(seconds) || 0));
    if (s >= 86400) return Math.floor(s / 86400) + "d";
    if (s >= 3600) return Math.floor(s / 3600) + "h";
    if (s >= 60) return Math.floor(s / 60) + "m";
    return s + "s";
}

/**
 * A late edit of an expired, already redacted message stored its own readable
 * archive record, so the message must be redacted again (audit #11).
 */
export function lateEditReopensRedaction(message, change) {
    return change?.action === "edit" && !!message?.expired && !!message?.redacted;
}
