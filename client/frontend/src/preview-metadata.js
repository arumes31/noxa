const SKIPPED = Symbol("skipped");
const metadata = new Map();
const queue = [];
const expires = new Map();
let running = 0;

function drain() {
    while (running < 4 && queue.length) {
        const job = queue.shift();
        running++;
        Promise.resolve().then(job.run).then(job.resolve, () => job.resolve(null)).finally(() => { running--; drain(); });
    }
}

// Providers share one bounded queue and cache, scoped to the active connection.
// Every automatic metadata caller must supply explicit renderer-owned channel context.
// Check before cache access as well as queueing: private callers cannot reuse channel metadata.
export function loadPreviewMetadata(provider, id, scope, isCurrent, request) {
    if (scope?.conversation !== "channel") return Promise.resolve(null);
    const key = JSON.stringify([scope.generation, scope.tabID, provider, id]);
    if (expires.has(key) && expires.get(key) < Date.now()) { metadata.delete(key); expires.delete(key); }
    if (!metadata.has(key)) {
        if (metadata.size >= 128) { const oldest = metadata.keys().next().value; metadata.delete(oldest); expires.delete(oldest); }
        let skipped = false;
        const pending = new Promise(resolve => {
            queue.push({ resolve, run: () => {
                if (scope.conversation !== "channel" || !isCurrent()) { skipped = true; return null; }
                return request();
            } });
            drain();
        }).then(result => {
            if (metadata.get(key) === pending) {
                if (skipped) { metadata.delete(key); expires.delete(key); }
                // Transient failures must not poison YouTube/X previews for the
                // entire connection. Briefly cache failures to avoid hot retries.
                else if (result == null) expires.set(key, Date.now() + 5000);
            }
            return skipped ? SKIPPED : result;
        });
        metadata.set(key, pending);
        if (provider.startsWith("portal:")) expires.set(key, Date.now() + 60000);
    }
    return metadata.get(key).then(result => result !== SKIPPED
        ? result
        : (isCurrent() ? loadPreviewMetadata(provider, id, scope, isCurrent, request) : null));
}
