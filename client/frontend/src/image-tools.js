import { escapeHTML as escapeTranslation } from "./markdown.js";
import { t } from "./i18n.js";
// image-tools.js — wave-7 image upload helpers (268/269/274). Avatars get a
// preview dialog with crop/zoom and are resized to 256x256 PNG on a canvas;
// icons are downscaled to max 1024px and recompressed as JPEG q0.85 (274).
// Animated GIF/WebP always pass through untouched — canvas re-encoding
// would flatten the animation (269).
import { setSafeImage } from "./safe-media.js";
import { closeDialog, mountDialog } from "./modal.js";

const V = () => window.__noxa;

const MAX_UPLOAD = 256 * 1024; // server-side image cap (same as avatars)

function generationCurrent(options) {
    return !Object.hasOwn(options, "serverGeneration") ||
        options.serverGeneration === V().state.serverGeneration;
}

// readFileBase64 reads a File as base64.
async function readFileBase64(file) {
    const buf = await file.arrayBuffer();
    return bytesBase64(new Uint8Array(buf));
}

// pickFile opens the file picker and resolves with the File (or null).
function pickFile() {
    return new Promise((resolve) => {
        const input = document.createElement("input");
        input.type = "file";
        input.accept = "image/png,image/jpeg,image/gif,image/webp";
        input.onchange = () => resolve(input.files[0] || null);
        input.click();
    });
}

// loadImage decodes a File into an Image.
function loadImage(file) {
    return new Promise((resolve, reject) => {
        const url = URL.createObjectURL(file);
        const img = new Image();
        let settled = false;
        const fail = () => {
            if (settled) return;
            settled = true;
            clearTimeout(timeout);
            img.onload = null;
            img.onerror = null;
            URL.revokeObjectURL(url);
            reject(new Error("cannot read image"));
        };
        const timeout = setTimeout(() => {
            img.src = "";
            fail();
        }, 15000);
        img.onload = () => {
            if (settled) return;
            settled = true;
            clearTimeout(timeout);
            img.onload = null;
            img.onerror = null;
            resolve({ img, url });
        };
        img.onerror = fail;
        img.src = url;
    });
}

// passthroughAnimated returns the file unchanged when it is an animated
// format (269); null otherwise.
async function passthroughAnimated(file, current = () => true) {
    if (file.type !== "image/gif" && file.type !== "image/webp") return null;
    if (file.size > MAX_UPLOAD) {
        if (current()) V().toast(t("desktop.animated.image.too.large.max.256.kib"), "warn");
        return { dataBase64: "", contentType: "" };
    }
    return { dataBase64: await readFileBase64(file), contentType: file.type };
}

// pickAvatar opens the picker + crop dialog (268) and resolves with
// {dataBase64, contentType} or null when cancelled.
function bytesBase64(bytes) {
    let binary = "";
    for (let i = 0; i < bytes.length; i += 8192) binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
    return btoa(binary);
}

export async function pickAvatar(options = {}) {
    return prepareAvatar(await pickFile(), options);
}

export async function prepareAvatar(file, options = {}) {
    const current = () => generationCurrent(options);
    if (!file || !current()) return null;
    if (file.size > 8 * 1024 * 1024) {
        V().toast(t("desktop.image.too.large.max.8.mib"), "warn"); return null;
    }
    if ((file.type === "image/gif" || file.type === "image/webp") && file.size > MAX_UPLOAD) {
        V().toast(t("desktop.animated.image.too.large.max.256.kib"), "warn"); return null;
    }
    try {
        const bytes = new Uint8Array(await file.arrayBuffer());
        if (!current()) return null;
        const head = String.fromCharCode(...bytes.subarray(0, 16));
        const type = /^GIF8[79]a/.test(head) ? "image/gif" :
            head.startsWith("RIFF") && head.slice(8, 12) === "WEBP" ? "image/webp" :
            head.startsWith("\x89PNG\r\n\x1a\n") ? "image/png" :
            bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255 ? "image/jpeg" : "";
        if (!type) throw new Error("unsupported image");
        if (type === "image/png") {
            const view = new DataView(bytes.buffer);
            for (let pos = 8; pos + 12 <= bytes.length;) {
                const kind = String.fromCharCode(...bytes.subarray(pos + 4, pos + 8));
                if (["acTL", "fcTL", "fdAT"].includes(kind)) {
                    V().toast(t("desktop.avatar.apng.unsupported"), "warn"); return null;
                }
                const next = pos + 12 + view.getUint32(pos);
                if (next > bytes.length) throw new Error("invalid PNG");
                pos = next;
            }
        }
        if (type === "image/gif" || type === "image/webp") {
            if (bytes.length > MAX_UPLOAD) { V().toast(t("desktop.animated.image.too.large.max.256.kib"), "warn"); return null; }
            return avatarOriginalDialog({ dataBase64: bytesBase64(bytes), contentType: type }, options);
        }
        return cropDialog(file, options);
    } catch {
        if (current()) V().toast(t("desktop.cannot.read.image"), "warn");
        return null;
    }
}

function avatarOriginalDialog(payload, options) {
    return new Promise(resolve => {
        const overlay = document.createElement("div"); overlay.className = "dlg-overlay";
        overlay.innerHTML = `<div class="dlg img-edit"><h3>${escapeTranslation(t("desktop.set.avatar"))}</h3>
            <div class="img-preview"></div><p class="dlg-text">${escapeTranslation(t("desktop.avatar.animation.original"))}</p>
            <div class="dlg-buttons"><button class="dlg-ok">${escapeTranslation(t("desktop.use.image"))}</button>
            <button class="dlg-cancel">${escapeTranslation(t("desktop.cancel"))}</button></div></div>`;
        const image = setSafeImage(overlay.querySelector(".img-preview"), `data:${payload.contentType};base64,${payload.dataBase64}`, { avatar: true });
        image.style.cssText = "width:256px;height:256px;object-fit:contain";
        let result = null;
        overlay.querySelector(".dlg-ok").onclick = () => { if (generationCurrent(options)) result = payload; closeDialog(overlay); };
        overlay.querySelector(".dlg-cancel").onclick = () => closeDialog(overlay, "cancel");
        mountDialog(overlay, { serverScoped: options.serverScoped, onCancel: () => { result = null; }, onClose: () => resolve(result) });
    });
}

// pickIcon opens the picker and returns a compressed image (274): downscale
// to maxDim, JPEG q0.85 (animated formats pass through, 269). A single pass at
// a fixed quality still leaves a detailed photo over the server's 256 KiB cap
// and the upload comes back rejected, so keep re-encoding cheaper until it
// fits, then shrink the canvas if quality alone is not enough.
export async function pickIcon(maxDim = 1024, quality = 0.85) {
    const file = await pickFile();
    return prepareIcon(file, maxDim, quality);
}

// File-input callers can keep preparation and feedback inside their own view.
export async function prepareIcon(file, maxDim = 1024, quality = 0.85, current = () => true) {
    if (!file || !current()) return null;
    const anim = await passthroughAnimated(file, current);
    if (!current()) return null;
    if (anim) return anim.dataBase64 ? anim : null;
    if (file.size > 8 * 1024 * 1024) {
        V().toast(t("desktop.image.too.large.max.8.mib"), "warn");
        return null;
    }
    let objectURL = "";
    try {
        const loaded = await loadImage(file);
        const { img } = loaded;
        objectURL = loaded.url;
        if (!current()) return null;
        let dim = maxDim;
        let best = null;
        for (let attempt = 0; attempt < 4; attempt++) {
            const scale = Math.min(1, dim / Math.max(img.width, img.height));
            const out = document.createElement("canvas");
            out.width = Math.max(1, Math.round(img.width * scale));
            out.height = Math.max(1, Math.round(img.height * scale));
            const ctx = out.getContext("2d");
            ctx.fillStyle = "#0b0f14"; // JPEG has no alpha: flatten onto ink navy
            ctx.fillRect(0, 0, out.width, out.height);
            ctx.drawImage(img, 0, 0, out.width, out.height);
            for (let q = quality; q >= 0.4; q -= 0.15) {
                best = out.toDataURL("image/jpeg", q).split(",")[1];
                // base64 inflates by 4/3; compare the decoded length.
                if (base64Bytes(best) <= MAX_UPLOAD) {
                    return { dataBase64: best, contentType: "image/jpeg" };
                }
            }
            dim = Math.max(128, Math.round(dim / 2));
        }
        if (current()) V().toast(t("desktop.image.could.not.be.compressed.under.256.kib"), "warn");
        return null;
    } catch {
        if (current()) V().toast(t("desktop.cannot.read.image"), "warn");
        return null;
    } finally {
        if (objectURL) URL.revokeObjectURL(objectURL);
    }
}

// base64Bytes returns the decoded byte length of a base64 payload.
function base64Bytes(b64) {
    const padding = b64.endsWith("==") ? 2 : b64.endsWith("=") ? 1 : 0;
    return Math.floor(b64.length * 3 / 4) - padding;
}

// These small, side-effect-free boundaries are also used by the upload flows
// above; exporting them keeps their compatibility tests independent of DOM
// image decoding.
export { base64Bytes, generationCurrent };

// cropDialog shows the 256x256 preview/crop editor (268): zoom slider and
// drag-to-reposition on a canvas, output PNG.
function cropDialog(file, options = {}) {
    return new Promise(async (resolve) => {
        let img, url;
        try {
            ({ img, url } = await loadImage(file));
        } catch {
            if (generationCurrent(options)) V().toast(t("desktop.cannot.read.image"), "warn");
            return resolve(null);
        }
        if (!generationCurrent(options)) {
            URL.revokeObjectURL(url);
            return resolve(null);
        }
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay";
        overlay.innerHTML = `
            <div class="dlg img-edit">
                <h3>${escapeTranslation(t("desktop.set.avatar"))}</h3>
                <div class="img-preview"><canvas width="256" height="256"></canvas></div>
                <label class="dlg-label">${escapeTranslation(t("desktop.zoom"))}</label>
                <input type="range" class="img-zoom" min="100" max="300" value="100" />
                <div class="dlg-text img-hint">${escapeTranslation(t("desktop.drag.the.preview.to.reposition.output.256.256.png"))}</div>
                <div class="dlg-buttons">
                    <button class="dlg-ok">${escapeTranslation(t("desktop.use.image"))}</button>
                    <button class="dlg-cancel">${escapeTranslation(t("desktop.cancel"))}</button>
                </div>
            </div>`;
        const canvas = overlay.querySelector("canvas");
        const zoomEl = overlay.querySelector(".img-zoom");
        const ctx = canvas.getContext("2d");
        const st = { zoom: 1, dx: 0, dy: 0, drag: null };

        const draw = () => {
            ctx.fillStyle = "#0b0f14";
            ctx.fillRect(0, 0, canvas.width, canvas.height);
            const base = Math.max(canvas.width / img.width, canvas.height / img.height);
            const scale = base * st.zoom;
            const w = img.width * scale, h = img.height * scale;
            ctx.drawImage(img, (canvas.width - w) / 2 + st.dx, (canvas.height - h) / 2 + st.dy, w, h);
        };
        zoomEl.oninput = () => { st.zoom = zoomEl.value / 100; draw(); };
        canvas.addEventListener("mousedown", (e) => {
            st.drag = { x: e.clientX - st.dx, y: e.clientY - st.dy };
        });
        const onMove = (e) => {
            if (!st.drag) return;
            st.dx = e.clientX - st.drag.x;
            st.dy = e.clientY - st.drag.y;
            draw();
        };
        const onUp = () => { st.drag = null; };
        window.addEventListener("mousemove", onMove);
        window.addEventListener("mouseup", onUp);
        draw();

        let result = null;
        let settled = false;
        const cleanup = () => {
            window.removeEventListener("mousemove", onMove);
            window.removeEventListener("mouseup", onUp);
            URL.revokeObjectURL(url);
            if (!settled) {
                settled = true;
                resolve(result);
            }
        };
        overlay.querySelector(".dlg-ok").onclick = () => {
            const dataUrl = canvas.toDataURL("image/png");
            result = { dataBase64: dataUrl.split(",")[1], contentType: "image/png" };
            closeDialog(overlay);
        };
        overlay.querySelector(".dlg-cancel").onclick = () => closeDialog(overlay, "cancel");
        overlay.onclick = (e) => { if (e.target === overlay) closeDialog(overlay, "cancel"); };
        mountDialog(overlay, {
            onCancel: () => { result = null; },
            onClose: cleanup,
            serverScoped: options.serverScoped,
        });
    });
}
