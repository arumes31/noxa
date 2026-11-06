// Decode on-device; accept only pairing links belonging to this server.
import { parseLinkFragment } from "../core/device.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";

export function pairingLinkFromQR(value, origin = location.origin) {
    try {
        const url = new URL(value);
        if (url.origin !== origin || url.username || url.password || url.pathname !== "/link" || url.search) return null;
        return parseLinkFragment(url.hash);
    } catch { return null; }
}

export function scanPairingQR({ signal } = {}) {
    return scanQR({
        signal,
        labels: { title: t("connect.scan"), hint: t("connect.scanHint"), cancel: t("link.cancel"), unavailable: t("connect.scanUnavailable") },
        accept: value => {
            const link = pairingLinkFromQR(value);
            return link ? { done: true, value: link } : { message: t("connect.scanInvalid") };
        },
    });
}

/**
 * Scans QR codes until accept(text) returns { done: true, value }; { message }
 * keeps scanning with that status. Resolves with the value, or null when
 * cancelled or the camera failed. Decoding stays on this device.
 */
export function scanQR({ signal, labels, accept }) {
    return new Promise(resolve => {
        let stream, timer, finished = false;
        const video = h("video", { autoplay: true, playsinline: true, muted: true, class: "qr-camera" });
        const status = h("p", { class: "hint", role: "status" }, labels.hint);
        const close = h("button", { type: "button", class: "btn btn-block" }, labels.cancel);
        const dialog = h("dialog", { class: "qr-dialog", "aria-labelledby": "qr-title" },
            h("h2", { id: "qr-title" }, labels.title), video, status, close);
        const previousFocus = document.activeElement;
        const stop = (result = null) => {
            if (finished) return;
            finished = true;
            clearTimeout(timer);
            stream?.getTracks().forEach(track => track.stop());
            video.srcObject = null;
            dialog.close(); dialog.remove();
            signal?.removeEventListener("abort", abort);
            window.removeEventListener("pagehide", abort);
            document.removeEventListener("visibilitychange", hidden);
            if (previousFocus?.isConnected) previousFocus.focus();
            resolve(result);
        };
        const abort = () => stop();
        const hidden = () => { if (document.hidden) stop(); };
        close.onclick = abort;
        dialog.addEventListener("cancel", event => { event.preventDefault(); stop(); });
        document.body.append(dialog);
        dialog.showModal();
        signal?.addEventListener("abort", abort, { once: true });
        window.addEventListener("pagehide", abort);
        document.addEventListener("visibilitychange", hidden);
        if (signal?.aborted) { stop(); return; }
        (async () => {
            try {
                if (!navigator.mediaDevices?.getUserMedia) throw new Error("camera unavailable");
                // Open only in response to the scan button. Never request audio.
                stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: { ideal: "environment" } }, audio: false });
                if (finished) { stream.getTracks().forEach(track => track.stop()); return; }
                video.srcObject = stream;
                await video.play();
                const { default: decode } = await import("jsqr");
                if (finished) return;
                const canvas = document.createElement("canvas");
                const context = canvas.getContext("2d", { willReadFrequently: true });
                const scan = () => {
                    if (finished) return;
                    try {
                        if (video.readyState >= 2 && video.videoWidth) {
                            const scale = Math.min(1, 640 / video.videoWidth);
                            canvas.width = Math.round(video.videoWidth * scale);
                            canvas.height = Math.round(video.videoHeight * scale);
                            context.drawImage(video, 0, 0, canvas.width, canvas.height);
                            const pixels = context.getImageData(0, 0, canvas.width, canvas.height);
                            const qr = decode(pixels.data, pixels.width, pixels.height);
                            if (qr) {
                                const verdict = accept(qr.data) || {};
                                if (verdict.done) { stop(verdict.value); return; }
                                if (verdict.message) status.textContent = verdict.message;
                            }
                        }
                        timer = setTimeout(scan, 200);
                    } catch { cameraError(); }
                };
                scan();
            } catch { if (!finished) cameraError(); }
        })();
        function cameraError() {
            stream?.getTracks().forEach(track => track.stop());
            video.srcObject = null;
            video.hidden = true;
            status.textContent = labels.unavailable;
        }
    });
}
