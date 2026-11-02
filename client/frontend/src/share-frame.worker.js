import { captureFrameTransform } from "./share-frame-transform.js";

let started = false, output = {}, abort;
self.onmessage = ({ data }) => {
    if (data.op === "init" && !started) {
        started = true;
        abort = new AbortController();
        const transform = captureFrameTransform({ region: data.region, getOutput: () => output,
            onLayout: layout => self.postMessage({ op: "layout", layout }) });
        data.readable.pipeThrough(transform, { signal: abort.signal })
            .pipeTo(data.writable, { signal: abort.signal })
            .then(() => self.postMessage({ op: "ended" }))
            .catch(() => { if (!abort.signal.aborted) self.postMessage({ op: "error" }); });
        self.postMessage({ op: "ready" });
    } else if (data.op === "output" && started) {
        output = data.output;
        self.postMessage({ op: "applied", seq: data.seq });
    }
};
