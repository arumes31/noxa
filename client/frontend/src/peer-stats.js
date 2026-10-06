// All diagnostic consumers share the raw, read-only snapshot; each keeps its
// own previous report and scope checks. Weak keys cannot retain retired peers.
const MAX_AGE_MS = 250;
const TIMEOUT_MS = 5000;
const closed = pc => pc.connectionState === "closed" || pc.signalingState === "closed";

export function createPeerStatsSampler({ now = () => performance.now() } = {}) {
    const peers = new WeakMap();

    function read(pc, entry, minimumRequest) {
        if (closed(pc)) {
            peers.delete(pc);
            return Promise.reject(new Error("peer connection closed"));
        }
        if (entry.pending) {
            // Publication replacement requires a collection started after its
            // caller, even if another consumer was already collecting stats.
            return entry.request >= minimumRequest ? entry.pending : entry.pending.then(() => read(pc, entry, minimumRequest));
        }
        const age = now() - entry.startedAt;
        if (entry.report && entry.request >= minimumRequest && age >= 0 && age <= MAX_AGE_MS) return Promise.resolve(entry.report);

        entry.report = null;
        entry.startedAt = now();
        entry.request++;
        let resolve, reject, timedOut = false;
        const pending = new Promise((yes, no) => { resolve = yes; reject = no; });
        entry.pending = pending;
        const timer = setTimeout(() => {
            timedOut = true;
            reject(new Error("peer stats timeout"));
            // Keep the failed promise until native collection settles. Retrying
            // a hung native call every interval would create unbounded work.
        }, TIMEOUT_MS);
        const finish = (report, error) => {
            clearTimeout(timer);
            entry.pending = null;
            if (timedOut) return; // A late result must never populate the cache.
            if (closed(pc)) {
                peers.delete(pc);
                reject(new Error("peer connection closed"));
            } else if (error) reject(error);
            else if (!report || typeof report.get !== "function" || typeof report.forEach !== "function") reject(new Error("invalid peer stats"));
            else { entry.report = report; resolve(report); }
        };
        try { Promise.resolve(pc.getStats()).then(report => finish(report), error => finish(null, error)); }
        catch (error) { finish(null, error); }
        return pending;
    }

    return function sample(pc, { fresh = false } = {}) {
        if (!pc || typeof pc.getStats !== "function") return Promise.reject(new Error("peer stats unavailable"));
        let entry = peers.get(pc);
        if (!entry) {
            entry = { request: 0, startedAt: 0, report: null, pending: null };
            peers.set(pc, entry);
        }
        return read(pc, entry, fresh ? entry.request + 1 : 0);
    };
}

export const samplePeerStats = createPeerStatsSampler();
