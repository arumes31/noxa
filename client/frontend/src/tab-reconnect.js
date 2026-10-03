// One retry series per native server tab. A record owns its captured
// credentials and timers; replacing or canceling it invalidates every await.
export function reconnectDelay(attempt, random = Math.random) {
    if (attempt <= 1) return 5000;
    const ceiling = Math.min(30000, 10000 * 2 ** Math.min(2, attempt - 2));
    return Math.floor(ceiling * (0.8 + 0.2 * random()));
}

export function createTabReconnects(options) {
    const entries = new Map();
    const current = entry => entries.get(entry.tabID) === entry;
    const notify = entry => options.changed?.(entry.tabID, entry);
    const clear = entry => {
        clearTimeout(entry.timer);
        clearInterval(entry.countdown);
        entry.timer = entry.countdown = null;
    };
    function cancel(tabID) {
        const entry = entries.get(tabID);
        if (!entry) return;
        clear(entry);
        entries.delete(tabID);
        options.changed?.(tabID, null);
    }
    function schedule(entry) {
        if (!current(entry)) return;
        if (!options.enabled()) { cancel(entry.tabID); return; }
        entry.attempts++;
        const delay = (options.delay || reconnectDelay)(entry.attempts);
        entry.remaining = Math.ceil(delay / 1000);
        options.scheduled?.(entry.tabID, entry);
        entry.countdown = setInterval(() => {
            if (!current(entry)) return;
            entry.remaining = Math.max(0, entry.remaining - 1);
            notify(entry);
        }, 1000);
        entry.timer = setTimeout(async () => {
            clear(entry);
            if (!current(entry)) return;
            if (!options.enabled()) { cancel(entry.tabID); return; }
            entry.inFlight = true;
            notify(entry);
            const lossVersion = entry.lossVersion;
            const attemptCurrent = () => current(entry) && entry.lossVersion === lossVersion;
            let error = "";
            try {
                const result = await options.connect(entry.tabID, entry.target);
                if (!current(entry)) return;
                error = String(result?.error || "");
                if (result?.terminal) {
                    cancel(entry.tabID);
                    options.stopped?.(entry.tabID, error);
                    return;
                }
                if (!error && attemptCurrent() && await options.complete(entry.tabID, entry.target, attemptCurrent)) {
                    if (attemptCurrent()) {
                        if (result?.warning) options.warning?.(entry.tabID, result.warning);
                        cancel(entry.tabID);
                    }
                    else { entry.inFlight = false; schedule(entry); }
                    return;
                }
            } catch (cause) { error = String(cause || "reconnect failed"); }
            if (!current(entry)) return;
            entry.inFlight = false;
            if (error) options.failed?.(entry.tabID, error);
            schedule(entry);
        }, delay);
        notify(entry);
    }
    return {
        start(tabID, target) {
            if (!tabID || !target || !options.enabled()) return;
            const existing = entries.get(tabID);
            if (existing) {
                // A newly recovered socket can fail while its status query is
                // pending. Keep the backoff, but reject that stale success.
                existing.lossVersion++;
                return;
            }
            const entry = { tabID, target: { ...target }, attempts: 0, inFlight: false, lossVersion: 0 };
            entries.set(tabID, entry);
            schedule(entry);
        },
        cancel,
        cancelAll() { for (const tabID of [...entries.keys()]) cancel(tabID); },
        refresh(tabID) { options.changed?.(tabID, entries.get(tabID) || null); },
    };
}
