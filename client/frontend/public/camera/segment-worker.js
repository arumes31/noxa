// Classic worker: MediaPipe's local WebAssembly loader uses importScripts.
// CPU inference stays off the UI thread. Only one frame is in flight.
self.exports = {};
importScripts("./vision_bundle.js");
let segmenter;
self.onmessage = async ({ data }) => {
    try {
        if (data.type === "init") {
            const base = new URL("./", self.location.href);
            segmenter = await self.exports.ImageSegmenter.createFromOptions({
                wasmLoaderPath: new URL("vision_wasm_nosimd_internal.js", base).href,
                wasmBinaryPath: new URL("vision_wasm_nosimd_internal.wasm", base).href,
            }, {
                baseOptions: { modelAssetPath: new URL("selfie_segmenter.tflite", base).href, delegate: "CPU" },
                runningMode: "VIDEO", outputConfidenceMasks: true, outputCategoryMask: false,
            });
            self.postMessage({ ready: true });
            return;
        }
        const result = segmenter.segmentForVideo(data.frame, data.timestamp);
        try {
            const mask = result.confidenceMasks[0];
            const values = mask.getAsFloat32Array().slice();
            self.postMessage({ frame: data.frame, mask: values.buffer, width: mask.width, height: mask.height }, [data.frame, values.buffer]);
        } finally { result.close(); }
    } catch (error) {
        data.frame?.close();
        self.postMessage({ error: error.message || String(error) });
    }
};
