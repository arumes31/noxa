const SKIPPED = Symbol("skipped");
const metadata = new Map();
const queue = [];
let running = 0;

function drain() {
    while (running < 4 && queue.length) {
        const job = queue.shift();
        running++;
        Promise.resolve().then(job.run).then(job.resolve, () => job.resolve(null)).finally(() => { running--; drain(); });
    }
}

// Providers share one bounded queue and cache, scoped to the active connection.
export function loadPreviewMetadata(provider, id, scope, isCurrent, request) {
    const key = JSON.stringify([scope.generation, scope.tabID, provider, id]);
    if (!metadata.has(key)) {
        if (metadata.size >= 128) metadata.delete(metadata.keys().next().value);
        let skipped = false;
        const pending = new Promise(resolve => {
            queue.push({ resolve, run: () => {
                if (!isCurrent()) { skipped = true; return null; }
                return request();
            } });
            drain();
        }).then(result => {
            if (skipped && metadata.get(key) === pending) metadata.delete(key);
            return skipped ? SKIPPED : result;
        });
        metadata.set(key, pending);
    }
    return metadata.get(key).then(result => result !== SKIPPED
        ? result
        : (isCurrent() ? loadPreviewMetadata(provider, id, scope, isCurrent, request) : null));
}
