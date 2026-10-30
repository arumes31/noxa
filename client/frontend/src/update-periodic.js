export const UPDATE_INTERVAL_HOURS = [1, 2, 4, 8, 24];
export const updateIntervalHours = value => UPDATE_INTERVAL_HOURS.includes(value) ? value : 2;

// Monotonic elapsed time survives clock corrections; wall time accounts for
// sleep on platforms whose performance clock pauses while suspended.
export function createUpdateClock(wall = Date.now, monotonic = () => performance.now()) {
    let previousWall = wall(), previousMono = monotonic(), elapsed = 0;
    return () => {
        const nextWall = wall(), nextMono = monotonic();
        elapsed += Math.max(0, nextWall - previousWall, nextMono - previousMono);
        previousWall = nextWall; previousMono = nextMono;
        return elapsed;
    };
}

// Read persisted preferences on each tick, including after sleep. A missed
// interval produces one check, never a burst of catch-up requests.
export function createPeriodicUpdateCheck({ settings, check, now = createUpdateClock() }) {
    let configuration = null, deadline = 0, running = false;
    return async function tick() {
        const saved = settings();
        const hours = updateIntervalHours(saved?.updates_check_interval_hours);
        const enabled = () => !!settings() && settings().updates_check_periodically !== false;
        const next = saved && saved.updates_check_periodically !== false ? hours : null;
        if (configuration !== next) {
            configuration = next;
            deadline = now() + hours * 3600000;
        }
        if (!next || running || now() < deadline) return;
        deadline = now() + hours * 3600000;
        running = true;
        try { await check(enabled); } catch { /* Offline: retry at the next interval. */ }
        finally { running = false; }
    };
}
