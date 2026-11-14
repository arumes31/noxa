// Tracks per-tab "the server told us it is shutting down" so the disconnect
// that follows does not also announce "connection lost" — the shutdown notice
// already explained the drop, and a reconnect is scheduled. Entries expire so a
// much later, unrelated drop still announces normally.
export function createShutdownNotice(now = () => Date.now()) {
    const announced = new Map(); // tabID -> expires (ms)
    return {
        // The server announced a shutdown for this tab; ttlMs bounds how long a
        // following disconnect counts as "expected" (imminent: short; scheduled:
        // the lead time plus a grace margin).
        announce(tabID, ttlMs) {
            if (tabID) announced.set(tabID, now() + Math.max(0, ttlMs));
        },
        // True once if a shutdown was announced for this tab and has not expired.
        // Consumes the entry so only the first disconnect after it is suppressed.
        consume(tabID) {
            const expires = announced.get(tabID);
            if (expires === undefined) return false;
            announced.delete(tabID);
            return expires > now();
        },
        forget(tabID) { announced.delete(tabID); },
    };
}
