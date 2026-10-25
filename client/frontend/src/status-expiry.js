// Status messages with an optional expiry (533/534). The server's SetStatus
// carries no expiry, so the schedule lives on this device: a timer fires while
// the client runs, and check() re-evaluates on startup and resume.
export const STATUS_EXPIRY_OPTIONS = ["none", "30m", "1h", "4h", "today", "custom"];
const MINUTE = 60_000;
const MAX_TIMER = 2_147_000_000; // setTimeout saturates above ~24.8 days
// RETRY_DELAY spaces attempts to clear a status whose expiry already passed
// but could not be delivered yet (startup before the connection is up).
const RETRY_DELAY = 15_000;
const MAX_CUSTOM_MINUTES = 7 * 24 * 60;

// statusExpiryAt returns the absolute expiry in ms, or 0 for "never".
export function statusExpiryAt(option, now, customMinutes = 0) {
    switch (option) {
    case "30m": return now + 30 * MINUTE;
    case "1h": return now + 60 * MINUTE;
    case "4h": return now + 240 * MINUTE;
    case "today": { const end = new Date(now); end.setHours(23, 59, 59, 999); return end.getTime(); }
    case "custom": {
        const minutes = Math.round(Number(customMinutes));
        return Number.isFinite(minutes) && minutes > 0 ? now + Math.min(minutes, MAX_CUSTOM_MINUTES) * MINUTE : 0;
    }
    default: return 0;
    }
}

// createStatusExpiryScheduler keeps at most one pending expiry. load/save
// persist the record; onExpire(record) runs exactly once per expiry.
export function createStatusExpiryScheduler({ now = Date.now, setTimer = setTimeout, clearTimer = clearTimeout, load, save, onExpire }) {
    let record = normalize(load?.());
    let timer = null;
    let inFlight = null;
    const persist = () => save?.(record);
    const stop = () => { if (timer !== null) { clearTimer(timer); timer = null; } };
    function arm() {
        stop();
        if (!record) return;
        timer = setTimer(check, Math.min(Math.max(0, record.expiresAt - now()), MAX_TIMER));
    }
    // settle consumes the record once onExpire handled it. A callback result
    // of exactly `false` (or a rejected promise) means "could not clear yet",
    // e.g. no connection: the record stays and is retried after RETRY_DELAY.
    function settle(expired, ok) {
        if (inFlight === expired) inFlight = null;
        if (record !== expired) return;
        if (ok === false) { stop(); timer = setTimer(check, RETRY_DELAY); return; }
        record = null; stop(); persist();
    }
    function check() {
        if (!record) { stop(); return false; }
        if (inFlight === record) return false;
        if (record.expiresAt > now()) { arm(); return false; }
        const expired = record;
        inFlight = expired;
        stop();
        let result;
        try { result = onExpire?.(expired); } catch { result = false; }
        if (result && typeof result.then === "function") result.then(ok => settle(expired, ok), () => settle(expired, false));
        else settle(expired, result);
        return true;
    }
    arm();
    return {
        set(next) { record = normalize(next, now()); persist(); arm(); return record; },
        clear() { record = null; persist(); stop(); },
        current: () => record,
        check,
        dispose: stop,
    };
}

function normalize(record, now) {
    if (!record || typeof record !== "object") return null;
    const expiresAt = Number(record.expiresAt);
    if (!Number.isFinite(expiresAt) || expiresAt <= 0 || (now !== undefined && expiresAt <= now)) return null;
    return { ...record, expiresAt };
}
