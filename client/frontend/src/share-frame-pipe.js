import { captureFrameTransform } from "./share-frame-transform.js";

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
    let startupTimer;
    const close = () => {
        if (closed) return;
        closed = true;
        clearTimeout(startupTimer);
        worker.terminate();
        const error = new DOMException("capture ended", "AbortError");
        rejectReady(error);
        for (const request of pending.values()) { clearTimeout(request.timer); request.reject(error); }
        pending.clear();
    };
    const fail = () => { if (!closed) { close(); onError(); } };
    worker.onerror = fail;
    worker.onmessageerror = fail;
    worker.onmessage = ({ data }) => {
        if (closed) return;
        if (data.op === "ready") { clearTimeout(startupTimer); resolveReady(); }
        else if (data.op === "layout") onLayout(data.layout);
        else if (data.op === "applied") {
            const request = pending.get(data.seq);
            if (request) { pending.delete(data.seq); clearTimeout(request.timer); request.resolve(); }
        } else if (data.op === "error" || data.op === "ended") fail();
    };
    startupTimer = setTimeout(fail, 5000);
    try { worker.postMessage({ op: "init", readable, writable, region }, [readable, writable]); }
    catch (error) { close(); throw error; }
    return {
        close,
        async setOutput(output) {
            await ready;
            if (closed) throw new DOMException("capture ended", "AbortError");
            return new Promise((resolve, reject) => {
                const id = ++seq, timer = setTimeout(fail, 5000);
                pending.set(id, { resolve, reject, timer });
                try { worker.postMessage({ op: "output", seq: id, output }); }
                catch { fail(); }
            });
        },
    };
}
