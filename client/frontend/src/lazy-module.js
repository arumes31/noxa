// lazy-module.js — loads a code-split module once (83). Concurrent callers
// share one import; a failed import (for example a missing chunk after an
// update) is forgotten so the next call can try again. current() returns the
// module once it has loaded, so callers can stay synchronous after that.
export function lazyModule(importer) {
    let loaded = null;
    let pending = null;
    return {
        current: () => loaded,
        load() {
            if (loaded) return Promise.resolve(loaded);
            pending ||= Promise.resolve().then(importer).then(module => {
                loaded = module;
                return module;
            }, error => {
                pending = null;
                throw error;
            });
            return pending;
        },
    };
}

/**
 * Starts loading a module when the browser is idle (or after timeoutMs), so
 * the first use is usually instant without delaying startup.
 */
export function preloadWhenIdle(lazy, { timeoutMs = 4000, scheduler = globalThis } = {}) {
    const run = () => { lazy.load().catch(() => { /* the first real use retries */ }); };
    if (typeof scheduler.requestIdleCallback === "function") scheduler.requestIdleCallback(run, { timeout: timeoutMs });
    else scheduler.setTimeout?.(run, Math.min(timeoutMs, 1500));
}
