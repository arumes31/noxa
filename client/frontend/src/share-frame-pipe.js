import { captureFrameTransform } from "./share-frame-transform.js";

// Timer tasks can run before already-posted worker replies after a UI stall.
// Keep an independent watchdog for a hung worker, but give queued replies a
// fresh delivery window when the renderer missed its regular check-ins.
// The 30-second ceiling also bounds repeated stalls/background throttling.
function captureDeadline(expire) {
    let remaining = 5000, previous = performance.now(), timer, cancelled = false;
    const hardDeadline = previous + 30000;
    const schedule = () => {
        const delay = Math.min(250, remaining);
        timer = setTimeout(() => {
            if (cancelled) return;
            const now = performance.now(), elapsed = now - previous;
            previous = now;
            if (elapsed > delay + 250) remaining = 5000;
            else remaining -= Math.max(delay, elapsed);
            if (remaining <= 0 || now >= hardDeadline) expire();
            else schedule();
        }, delay);
    };
    schedule();
    return () => { cancelled = true; clearTimeout(timer); };
}

// Transfer both stream endpoints once. Frames stay on the worker; only size
// changes and constraint acknowledgements return to the UI thread. Stream
// backpressure and the capture processor's one-frame buffer bound retention.
export function createCaptureFramePipe(readable, writable, { region, onLayout, onError }) {
    if (typeof Worker !== "function") {
        const abort = new AbortController();
        let output = {};
        readable.pipeThrough(captureFrameTransform({ region, getOutput: () => output, onLayout }), { signal: abort.signal })
            .pipeTo(writable, { signal: abort.signal }).catch(() => { if (!abort.signal.aborted) onError(); });
        return { close: () => abort.abort(), setOutput: async next => { output = next; } };
    }
    const worker = new Worker(new URL("./share-frame.worker.js", import.meta.url), { type: "module" });
    const pending = new Map();
    let closed = false, seq = 0, resolveReady, rejectReady;
    const ready = new Promise((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
    // A worker can fail before the first applyConstraints call.
    ready.catch(() => {});
    let cancelStartup;
    const close = () => {
        if (closed) return;
        closed = true;
        cancelStartup?.();
        worker.terminate();
        const error = new DOMException("capture ended", "AbortError");
        rejectReady(error);
        for (const request of pending.values()) { request.cancelDeadline(); request.reject(error); }
        pending.clear();
    };
    const fail = () => { if (!closed) { close(); onError(); } };
    worker.onerror = fail;
    worker.onmessageerror = fail;
    worker.onmessage = ({ data }) => {
        if (closed) return;
        if (data.op === "ready") { cancelStartup?.(); resolveReady(); }
        else if (data.op === "layout") onLayout(data.layout);
        else if (data.op === "applied") {
            const request = pending.get(data.seq);
            if (request) { pending.delete(data.seq); request.cancelDeadline(); request.resolve(); }
        } else if (data.op === "error" || data.op === "ended") fail();
    };
    cancelStartup = captureDeadline(fail);
    try { worker.postMessage({ op: "init", readable, writable, region }, [readable, writable]); }
    catch (error) { close(); throw error; }
    return {
        close,
        async setOutput(output) {
            await ready;
            if (closed) throw new DOMException("capture ended", "AbortError");
            return new Promise((resolve, reject) => {
                const id = ++seq, cancelDeadline = captureDeadline(fail);
                pending.set(id, { resolve, reject, cancelDeadline });
                try { worker.postMessage({ op: "output", seq: id, output }); }
                catch { fail(); }
            });
        },
    };
}
