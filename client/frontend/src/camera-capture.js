import { videoConstraints } from "./media-limits.js";

export function cameraConstraints(settings, limits) {
    const constraints = limits?.video_max_width
        ? videoConstraints(640, 360, settings?.camera_fps || 30, limits)
        : { width: 640, height: 360, frameRate: { ideal: settings?.camera_fps || 30 } };
    if (settings?.camera_device_id) constraints.deviceId = { exact: settings.camera_device_id };
    return constraints;
}

export function cameraEffect(settings) {
    return {
        mode: ["blur", "replace"].includes(settings?.camera_background) ? settings.camera_background : "none",
        background: ["slate", "warm", "studio", "custom"].includes(settings?.camera_background_scene) ? settings.camera_background_scene : "slate",
    };
}

export function cameraBlurStrength(settings) {
    const value = Number(settings?.camera_blur_strength ?? 14);
    return Number.isFinite(value) ? Math.max(2, Math.min(30, value)) : 14;
}
export function cameraBackgroundImage(settings) {
    const value = settings?.camera_background_image || "";
    if (value.length > 2800000 || !/^data:image\/(png|jpeg);base64,[A-Za-z0-9+/]+=*$/.test(value)) throw new Error("Choose a local PNG or JPEG background image.");
    return value;
}
// Presentation only: the transmitted track and segmentation canvas stay unmirrored.
export function applyCameraPreview(video, settings) {
    video.style.transform = settings?.camera_mirror_preview !== false ? "scaleX(-1)" : "";
}

export async function prepareCameraBackground(file) {
    if (!file || file.size > 8 * 1024 * 1024 || !["image/png", "image/jpeg", "image/webp"].includes(file.type)) throw new Error("Choose a PNG, JPEG or WebP image up to 8 MiB.");
    const bitmap = await createImageBitmap(file);
    try {
        if (bitmap.width > 16384 || bitmap.height > 16384 || bitmap.width * bitmap.height > 32000000) throw new Error("Background image dimensions are too large.");
        const scale = Math.min(1, 1280 / bitmap.width, 720 / bitmap.height);
        const canvas = document.createElement("canvas"); canvas.width = Math.max(1, Math.round(bitmap.width * scale)); canvas.height = Math.max(1, Math.round(bitmap.height * scale));
        canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
        return cameraBackgroundImage({ camera_background_image: canvas.toDataURL("image/jpeg", .85) });
    } finally { bitmap.close(); }
}

export function foregroundAlpha(confidence) {
    if (!Number.isFinite(confidence)) return 0;
    const value = Math.max(0, Math.min(1, (confidence - 0.25) / 0.5));
    return Math.round(value * value * (3 - 2 * value) * 255);
}

function segmentWorker() {
    const worker = new Worker(new URL("./camera/segment-worker.js", document.baseURI));
    let pending = null;
    const settle = (error, value) => {
        if (!pending) { value?.frame?.close(); return; }
        const request = pending; pending = null; clearTimeout(request.timer);
        if (error) request.reject(error); else request.resolve(value);
    };
    worker.onmessage = ({ data }) => settle(data.error ? new Error(data.error) : null, data);
    worker.onerror = event => { event.preventDefault(); settle(new Error("Camera background processing is unavailable on this device.")); };
    return {
        request(data, transfer = []) {
            return new Promise((resolve, reject) => {
                pending = { resolve, reject, timer: setTimeout(() => settle(new Error("Camera background processing timed out.")), 30000) };
                try { worker.postMessage(data, transfer); } catch (error) { settle(error); }
            });
        },
        stop() { worker.terminate(); settle(new DOMException("Camera stopped", "AbortError")); },
    };
}

function drawBackground(context, width, height, scene) {
    context.fillStyle = scene === "warm" ? "#b7987c" : scene === "studio" ? "#c3cbd0" : "#283541";
    context.fillRect(0, 0, width, height);
    context.globalCompositeOperation = "source-over";
    context.fillStyle = scene === "warm" ? "#90745f" : scene === "studio" ? "#899ca8" : "#354858";
    context.fillRect(0, height * 0.72, width, height * 0.28);
}

// Both private calls and channel publishing own this handle. Stopping the
// output track also releases the physical camera, worker, and frame timer.
export async function captureCamera(settings = {}, limits, { signal } = {}) {
    const effect = cameraEffect(settings);
    if (signal?.aborted) throw new DOMException("Camera stopped", "AbortError");
    const source = await navigator.mediaDevices.getUserMedia({ audio: false, video: cameraConstraints(settings, limits) });
    if (signal?.aborted) {
        source.getTracks().forEach(track => track.stop());
        throw new DOMException("Camera stopped", "AbortError");
    }
    if (effect.mode === "none") return { stream: source, stop: () => source.getTracks().forEach(track => track.stop()) };

    let worker, video, output, timer, stopped = false, background;
    let originalStop;
    const stop = () => {
        if (stopped) return;
        stopped = true;
        clearTimeout(timer);
        worker?.stop();
        originalStop?.();
        source.getTracks().forEach(track => track.stop());
        signal?.removeEventListener("abort", stop);
        if (video) { video.pause(); video.srcObject = null; }
    };
    signal?.addEventListener("abort", stop, { once: true });
    try {
        if (effect.mode === "replace" && effect.background === "custom") {
            background = new Image(); background.src = cameraBackgroundImage(settings); await background.decode();
            if (stopped) throw new DOMException("Camera stopped", "AbortError");
        }
        if (!globalThis.Worker || !globalThis.createImageBitmap || !globalThis.OffscreenCanvas) {
            throw new Error("Camera backgrounds require a browser with WebAssembly and OffscreenCanvas support. Choose No effect to use this camera.");
        }
        worker = segmentWorker();
        await worker.request({ type: "init" });
        if (stopped) throw new DOMException("Camera stopped", "AbortError");
        video = document.createElement("video");
        video.muted = true; video.playsInline = true; video.srcObject = source;
        await video.play();
        const canvas = document.createElement("canvas");
        const native = source.getVideoTracks()[0]?.getSettings() || {};
        canvas.width = video.videoWidth || native.width || 640;
        canvas.height = video.videoHeight || native.height || 360;
        const context = canvas.getContext("2d");
        const maskCanvas = document.createElement("canvas");
        const foreground = document.createElement("canvas");
        foreground.width = canvas.width; foreground.height = canvas.height;
        const fg = foreground.getContext("2d");
        let timestamp = 0;
        const render = async () => {
            const frame = await createImageBitmap(video);
            if (stopped) { frame.close(); return; }
            const result = await worker.request({ type: "frame", frame, timestamp: ++timestamp * 100 }, [frame]);
            if (stopped) { result.frame.close(); return; }
            try {
                maskCanvas.width = result.width; maskCanvas.height = result.height;
                const maskContext = maskCanvas.getContext("2d");
                const mask = maskContext.createImageData(result.width, result.height);
                const confidence = new Float32Array(result.mask);
                for (let i = 0; i < confidence.length; i++) mask.data[i * 4 + 3] = foregroundAlpha(confidence[i]);
                maskContext.putImageData(mask, 0, 0);
                fg.globalCompositeOperation = "copy";
                fg.drawImage(result.frame, 0, 0, canvas.width, canvas.height);
                fg.globalCompositeOperation = "destination-in";
                fg.drawImage(maskCanvas, 0, 0, canvas.width, canvas.height);
                context.globalCompositeOperation = "copy";
                if (effect.mode === "blur") {
                    context.filter = `blur(${cameraBlurStrength(settings)}px)`;
                    context.drawImage(result.frame, -24, -24, canvas.width + 48, canvas.height + 48);
                    context.filter = "none";
                } else if (background) {
                    const scale = Math.max(canvas.width / background.width, canvas.height / background.height);
                    const width = background.width * scale, height = background.height * scale;
                    context.drawImage(background, (canvas.width - width) / 2, (canvas.height - height) / 2, width, height);
                } else drawBackground(context, canvas.width, canvas.height, effect.background);
                context.globalCompositeOperation = "source-over";
                context.drawImage(foreground, 0, 0);
            } finally { result.frame.close(); }
        };
        // Never expose an unprocessed frame, even during initialization/failure.
        await render();
        if (stopped) throw new DOMException("Camera stopped", "AbortError");
        output = canvas.captureStream(Math.min(settings.camera_fps || 30, 15));
        const track = output.getVideoTracks()[0];
        originalStop = track.stop.bind(track);
        track.stop = stop;
        const ended = () => { stop(); track.dispatchEvent(new Event("ended")); };
        source.getVideoTracks()[0].addEventListener("ended", ended, { once: true });
        const tick = async () => {
            if (stopped) return;
            try { await render(); } catch { if (!stopped) ended(); return; }
            if (!stopped) timer = setTimeout(tick, 1000 / Math.min(settings.camera_fps || 30, 15));
        };
        timer = setTimeout(tick, 70);
        return { stream: output, stop };
    } catch (error) { stop(); throw error; }
}
