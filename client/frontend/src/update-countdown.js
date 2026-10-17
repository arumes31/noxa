// Owned by one verified update attempt, never by raw download progress.
export function createUpdateCountdown({ current, tick, restart, now = () => performance.now(), schedule = setTimeout, unschedule = clearTimeout }) {
    const deadline = now() + 3000;
    let timer = null, settled = false, started = false, resolve;
    const done = new Promise(r => { resolve = r; });
    const cancel = () => {
        if (settled || started) return;
        settled = true;
        unschedule(timer);
        resolve(false);
    };
    const startNow = () => {
        if (settled || started) return done;
        if (!current()) { cancel(); return done; }
        started = true;
        unschedule(timer);
        tick(0);
        Promise.resolve().then(restart).then(value => { settled = true; resolve(value === true); }, () => { settled = true; resolve(false); });
        return done;
    };
    const step = () => {
        if (settled || started) return;
        if (!current()) { cancel(); return; }
        const remaining = Math.max(0, deadline - now());
        if (remaining === 0) { startNow(); return; }
        tick(Math.ceil(remaining / 1000));
        timer = schedule(step, Math.min(1000, remaining));
    };
    step();
    return { done, cancel, startNow };
}
