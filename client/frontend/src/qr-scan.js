// qr-scan.js — a camera QR scanner dialog (205). Frames are decoded on this
// device: BarcodeDetector where the browser has one, otherwise the bundled
// jsQR (Apache-2.0, loaded on first use). The camera opens only from the
// user's click, never with audio, and every track stops when the dialog
// closes for any reason.

const SCAN_INTERVAL_MS = 200;
const MAX_SCAN_WIDTH = 640;

/**
 * A frame decoder: async (source, { canvas, context }) => text | "".
 * BarcodeDetector reads the video directly; jsQR needs pixels.
 */
export async function createQRDecoder({ win = globalThis, loadJsQR = () => import("jsqr") } = {}) {
    const Detector = win.BarcodeDetector;
    if (typeof Detector === "function") {
        try {
            const formats = typeof Detector.getSupportedFormats === "function" ? await Detector.getSupportedFormats() : ["qr_code"];
            if (formats.includes("qr_code")) {
                const detector = new Detector({ formats: ["qr_code"] });
                // A native detector that rejects a frame (some platforms list
                // qr_code but cannot run it) hands over to jsQR for good. A
                // failed jsQR load is forgotten so the next frame retries it.
                let fallback = null;
                let pending = null;
                let nativeFailed = false;
                return async (video, frame) => {
                    if (fallback) return fallback(video, frame);
                    if (!nativeFailed) {
                        try {
                            return (await detector.detect(video))[0]?.rawValue || "";
                        } catch { nativeFailed = true; }
                    }
                    pending ||= jsQRDecoder(loadJsQR).then(decode => { fallback = decode; return decode; }, error => { pending = null; throw error; });
                    return (await pending)(video, frame);
                };
            }
        } catch { /* fall back to jsQR */ }
    }
    return jsQRDecoder(loadJsQR);
}

async function jsQRDecoder(loadJsQR) {
    const { default: decode } = await loadJsQR();
    return async (video, { canvas, context }) => {
        if (!video.videoWidth) return "";
        const scale = Math.min(1, MAX_SCAN_WIDTH / video.videoWidth);
        canvas.width = Math.round(video.videoWidth * scale);
        canvas.height = Math.round(video.videoHeight * scale);
        context.drawImage(video, 0, 0, canvas.width, canvas.height);
        const pixels = context.getImageData(0, 0, canvas.width, canvas.height);
        return decode(pixels.data, pixels.width, pixels.height, { inversionAttempts: "attemptBoth" })?.data || "";
    };
}

/**
 * Opens the scanner. accept(text) returns { done: true, value } to close
 * with value, or { message } to keep scanning with that status. Resolves
 * with the accepted value, or null when cancelled or the camera failed.
 * labels: { title, hint, cancel, unavailable }.
 */
export function scanQR({ labels, accept, doc = document, media = globalThis.navigator?.mediaDevices, decoder = createQRDecoder, mount = null }) {
    return new Promise(resolve => {
        let stream = null, timer = null, finished = false;
        const overlay = doc.createElement("div");
        overlay.className = "dlg-overlay qr-scan-overlay";
        // The overlay is the dialog, as for the app's other .dlg-overlay
        // dialogs (modal.js gives them the same semantics).
        overlay.setAttribute("role", "dialog");
        overlay.setAttribute("aria-modal", "true");
        const dialog = doc.createElement("div");
        dialog.className = "dlg qr-scan";
        const title = doc.createElement("h3");
        title.id = "qr-scan-title";
        title.textContent = labels.title;
        overlay.setAttribute("aria-labelledby", title.id);
        const video = doc.createElement("video");
        video.className = "qr-scan-video";
        video.muted = true;
        video.autoplay = true;
        video.playsInline = true;
        video.setAttribute("aria-hidden", "true");
        const status = doc.createElement("p");
        status.className = "qr-scan-status";
        status.setAttribute("role", "status");
        status.textContent = labels.hint;
        const buttons = doc.createElement("div");
        buttons.className = "dlg-buttons";
        const cancel = doc.createElement("button");
        cancel.type = "button";
        cancel.className = "dlg-cancel";
        cancel.textContent = labels.cancel;
        buttons.append(cancel);
        dialog.append(title, video, status, buttons);
        overlay.append(dialog);
        const previousFocus = doc.activeElement;

        const stopCamera = () => {
            clearTimeout(timer);
            timer = null;
            stream?.getTracks().forEach(track => track.stop());
            stream = null;
            video.srcObject = null;
        };
        const finish = (value = null) => {
            if (finished) return;
            finished = true;
            removal?.disconnect();
            stopCamera();
            overlay.remove();
            doc.removeEventListener("keydown", onKey, true);
            doc.removeEventListener("visibilitychange", onHidden);
            if (previousFocus?.isConnected) previousFocus.focus?.();
            resolve(value);
        };
        const onKey = event => {
            if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); finish(); }
        };
        const onHidden = () => { if (doc.hidden) finish(); };
        const unavailable = () => {
            stopCamera();
            video.hidden = true;
            status.textContent = labels.unavailable;
            status.classList.add("warn");
        };
        cancel.addEventListener("click", () => finish());
        overlay.addEventListener("click", event => { if (event.target === overlay) finish(); });
        doc.addEventListener("keydown", onKey, true);
        doc.addEventListener("visibilitychange", onHidden);
        // mount registers the overlay with a dialog lifecycle (for example
        // closing on a server change). Whoever removes it, the camera stops
        // and the scan resolves.
        if (mount) mount(overlay);
        else doc.body.append(overlay);
        const removal = typeof MutationObserver === "function" ? new MutationObserver(() => { if (!overlay.isConnected) finish(); }) : null;
        removal?.observe(doc.body, { childList: true, subtree: true });
        if (!overlay.isConnected) { finish(); return; }
        cancel.focus();

        (async () => {
            try {
                if (!media?.getUserMedia) throw new Error("camera unavailable");
                stream = await media.getUserMedia({ video: { facingMode: { ideal: "environment" } }, audio: false });
                if (finished) { stream.getTracks().forEach(track => track.stop()); return; }
                video.srcObject = stream;
                await video.play?.();
                const decode = await decoder();
                if (finished) return;
                const canvas = doc.createElement("canvas");
                const frame = { canvas, context: canvas.getContext("2d", { willReadFrequently: true }) };
                const scan = async () => {
                    if (finished) return;
                    let text = "";
                    try {
                        if (video.readyState >= 2) text = await decode(video, frame);
                    } catch { /* a frame that cannot be read is skipped */ }
                    if (finished) return;
                    if (text) {
                        const verdict = accept(text) || {};
                        if (verdict.done) { finish(verdict.value); return; }
                        if (verdict.message) status.textContent = verdict.message;
                    }
                    timer = setTimeout(scan, SCAN_INTERVAL_MS);
                };
                void scan();
            } catch {
                if (!finished) unavailable();
            }
        })();
    });
}
