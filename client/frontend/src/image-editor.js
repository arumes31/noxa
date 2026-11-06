// image-editor.js — modal editor for a staged image attachment (605): crop,
// rotate, pixelate (redaction), pen, arrow, rectangle and text, with undo and
// redo. It runs before staging encryption and only ever replaces the staged
// file with a fresh encoding of the flattened pixels, so redacted areas and
// metadata of the original (EXIF, GPS) are not sent. Cancel leaves the staged
// file untouched.
import { t } from "./i18n.js";
import { closeDialog, confirmDialog, mountDialog, promptDialog } from "./modal.js";
import {
    ASPECTS, COLORS, MAX_PREVIEW_PIXELS, MIN_CROP, ExportTooLargeError, constrainCrop, createHistory,
    dataURLType, exportEdited, exportName, fitScale, nudgeCrop, rectFromPoints, renderOps, sizeUnit,
} from "./image-editor-model.js";

const TOOLS = Object.freeze([
    { id: "crop", icon: "⛶" },
    { id: "redact", icon: "■" },
    { id: "pixelate", icon: "▦" },
    { id: "pen", icon: "✎" },
    { id: "arrow", icon: "➚" },
    { id: "rect", icon: "▭" },
    { id: "text", icon: "T" },
]);

const makeCanvas = (width, height) => {
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(width));
    canvas.height = Math.max(1, Math.round(height));
    return canvas;
};

const encodeCanvas = (canvas, type, quality) => new Promise((resolve, reject) => {
    canvas.toBlob(blob => (blob ? resolve(blob) : reject(new Error("encoding failed"))), type, quality);
});

const blobDataURL = blob => new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
});

function loadImage(url) {
    return new Promise((resolve, reject) => {
        const image = new Image();
        image.onload = () => resolve(image);
        image.onerror = () => reject(new Error("cannot decode image"));
        image.src = url;
    });
}

function button(className, label, text) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = className;
    b.textContent = text;
    b.title = label;
    b.setAttribute("aria-label", label);
    return b;
}

/**
 * Opens the editor for a staged attachment ({ name, dataURL, ... }). Resolves
 * with a replacement staged attachment, or null when cancelled or unchanged.
 * isCurrent() is checked before resolving with a replacement.
 */
export async function openImageEditor(staged, { maxBytes = 0, isCurrent = () => true, toast = () => {} } = {}) {
    let image;
    try {
        image = await loadImage(staged.dataURL);
    } catch {
        toast(t("chat.imageEditor.decodeFailed"), "warn");
        return null;
    }
    if (!isCurrent()) return null;
    const sourceType = dataURLType(staged.dataURL);
    const fullWidth = image.naturalWidth, fullHeight = image.naturalHeight;
    const previewScale = fitScale(fullWidth, fullHeight, MAX_PREVIEW_PIXELS);
    const previewBase = makeCanvas(fullWidth * previewScale, fullHeight * previewScale);
    previewBase.getContext("2d").drawImage(image, 0, 0, previewBase.width, previewBase.height);
    const previewUnit = sizeUnit(fullWidth, fullHeight, previewBase.width / fullWidth);

    return new Promise(resolve => {
        const history = createHistory();
        let tool = "pen";
        let color = COLORS[0];
        let aspect = 0;
        let committed = null; // preview raster with every op applied
        let pending = null; // op being drawn
        let crop = null; // crop rectangle while the crop tool is active
        let result = null;
        let discardConfirmed = false;
        let busy = false;

        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay image-editor-overlay";
        const dialog = document.createElement("div");
        dialog.className = "dlg image-editor";
        const title = document.createElement("h3");
        title.id = "image-editor-title-" + Math.random().toString(36).slice(2, 8);
        title.textContent = t("chat.imageEditor.title");
        dialog.setAttribute("aria-labelledby", title.id);

        const toolbar = document.createElement("div");
        toolbar.className = "ie-toolbar";
        toolbar.setAttribute("role", "toolbar");
        toolbar.setAttribute("aria-label", t("chat.imageEditor.tools"));
        const toolButtons = new Map();
        for (const def of TOOLS) {
            const b = button("ie-tool ie-tool-" + def.id, t("chat.imageEditor.tool." + def.id), def.icon);
            b.setAttribute("aria-pressed", "false");
            b.onclick = () => setTool(def.id);
            toolButtons.set(def.id, b);
            toolbar.appendChild(b);
        }
        const rotate = button("ie-rotate", t("chat.imageEditor.rotate"), "⟳");
        rotate.onclick = () => { commit({ type: "rotate" }); };
        const colorInput = document.createElement("input");
        colorInput.type = "color";
        colorInput.className = "ie-color";
        colorInput.value = color;
        colorInput.title = t("chat.imageEditor.color");
        colorInput.setAttribute("aria-label", t("chat.imageEditor.color"));
        colorInput.oninput = () => { color = colorInput.value; };
        const undo = button("ie-undo", t("chat.imageEditor.undo"), "↶");
        undo.onclick = () => { if (busy) return; history.undo(); rebuild(); };
        const redo = button("ie-redo", t("chat.imageEditor.redo"), "↷");
        redo.onclick = () => { if (busy) return; history.redo(); rebuild(); };
        toolbar.append(rotate, colorInput, undo, redo);

        const cropBar = document.createElement("div");
        cropBar.className = "ie-crop-bar";
        cropBar.hidden = true;
        const aspectLabel = document.createElement("label");
        aspectLabel.textContent = t("chat.imageEditor.aspect") + " ";
        const aspectSelect = document.createElement("select");
        aspectSelect.className = "ie-aspect";
        for (const key of Object.keys(ASPECTS)) {
            const option = document.createElement("option");
            option.value = key;
            option.textContent = key === "free" ? t("chat.imageEditor.aspectFree") : key;
            aspectSelect.appendChild(option);
        }
        aspectSelect.onchange = () => {
            aspect = ASPECTS[aspectSelect.value] || 0;
            if (crop) { crop = constrainCrop(crop, aspect, rasterAspect()); placeCropBox(); }
        };
        aspectLabel.appendChild(aspectSelect);
        const applyCrop = document.createElement("button");
        applyCrop.type = "button";
        applyCrop.className = "ie-apply-crop";
        applyCrop.textContent = t("chat.imageEditor.applyCrop");
        applyCrop.onclick = () => finishCrop();
        cropBar.append(aspectLabel, applyCrop);

        const stage = document.createElement("div");
        stage.className = "ie-stage";
        const canvas = document.createElement("canvas");
        canvas.className = "ie-canvas";
        canvas.setAttribute("role", "img");
        canvas.setAttribute("aria-label", t("chat.imageEditor.canvas", { name: staged.name }));
        const cropBox = document.createElement("div");
        cropBox.className = "ie-crop-box";
        cropBox.tabIndex = 0;
        cropBox.hidden = true;
        cropBox.setAttribute("role", "group");
        cropBox.setAttribute("aria-label", t("chat.imageEditor.cropBox"));
        stage.append(canvas, cropBox);

        const hint = document.createElement("p");
        hint.className = "ie-hint dlg-text";
        hint.setAttribute("aria-live", "polite");

        const buttons = document.createElement("div");
        buttons.className = "dlg-buttons";
        const done = document.createElement("button");
        done.type = "button";
        done.className = "dlg-ok";
        done.textContent = t("chat.imageEditor.done");
        const cancel = document.createElement("button");
        cancel.type = "button";
        cancel.className = "dlg-cancel";
        cancel.textContent = t("desktop.cancel");
        buttons.append(done, cancel);

        dialog.append(title, toolbar, cropBar, stage, hint, buttons);
        overlay.appendChild(dialog);

        const rasterAspect = () => (committed ? committed.width / committed.height : 1);

        function setTool(next) {
            if (busy) return;
            if (tool === "crop" && next !== "crop") { crop = null; cropBox.hidden = true; }
            tool = next;
            for (const [id, b] of toolButtons) b.setAttribute("aria-pressed", String(id === tool));
            cropBar.hidden = tool !== "crop";
            hint.textContent = t("chat.imageEditor.hint." + tool);
            if (tool === "crop") {
                crop = constrainCrop({ x: 0.05, y: 0.05, w: 0.9, h: 0.9 }, aspect, rasterAspect());
                placeCropBox();
                cropBox.focus();
            }
            draw();
        }

        function placeCropBox() {
            if (!crop) { cropBox.hidden = true; return; }
            cropBox.hidden = false;
            cropBox.style.left = crop.x * 100 + "%";
            cropBox.style.top = crop.y * 100 + "%";
            cropBox.style.width = crop.w * 100 + "%";
            cropBox.style.height = crop.h * 100 + "%";
        }

        function finishCrop() {
            if (!crop || crop.w < MIN_CROP || crop.h < MIN_CROP) return;
            const rect = crop;
            crop = null;
            cropBox.hidden = true;
            commit({ type: "crop", rect });
            setTool("pen");
        }

        function commit(op) {
            if (busy) return;
            history.push(op);
            rebuild();
        }

        function rebuild() {
            const base = makeCanvas(previewBase.width, previewBase.height);
            base.getContext("2d").drawImage(previewBase, 0, 0);
            committed = renderOps(base, history.ops, makeCanvas, previewUnit);
            canvas.width = committed.width;
            canvas.height = committed.height;
            canvas.style.aspectRatio = `${committed.width} / ${committed.height}`;
            undo.disabled = !history.canUndo();
            redo.disabled = !history.canRedo();
            if (crop) { crop = constrainCrop(crop, aspect, rasterAspect()); placeCropBox(); }
            draw();
        }

        function draw() {
            const ctx = canvas.getContext("2d");
            ctx.clearRect(0, 0, canvas.width, canvas.height);
            if (committed) ctx.drawImage(committed, 0, 0);
            if (!pending) return;
            if (pending.type === "pixelate" || pending.type === "redact") {
                // Preview the region only; pixels change when the stroke ends.
                ctx.save();
                ctx.setLineDash([6, 4]);
                ctx.strokeStyle = "#ffffff";
                ctx.lineWidth = 2;
                const r = pending.rect;
                ctx.strokeRect(r.x * canvas.width, r.y * canvas.height, r.w * canvas.width, r.h * canvas.height);
                ctx.restore();
            } else {
                // Shapes render through the same code as the export.
                renderOps(canvas, [pending], makeCanvas, previewUnit);
            }
        }

        const pointFor = event => {
            const box = canvas.getBoundingClientRect();
            return {
                x: Math.min(1, Math.max(0, (event.clientX - box.left) / Math.max(1, box.width))),
                y: Math.min(1, Math.max(0, (event.clientY - box.top) / Math.max(1, box.height))),
            };
        };
        let start = null;
        canvas.addEventListener("pointerdown", event => {
            if (busy || event.button !== 0) return;
            const p = pointFor(event);
            start = p;
            canvas.setPointerCapture?.(event.pointerId);
            if (tool === "pen") pending = { type: "pen", color, points: [p] };
            else if (tool === "arrow") pending = { type: "arrow", color, from: p, to: p };
            else if (tool === "rect" || tool === "pixelate" || tool === "redact") pending = { type: tool, color, rect: rectFromPoints(p, p) };
            else if (tool === "crop") { crop = constrainCrop(rectFromPoints(p, p), aspect, rasterAspect()); placeCropBox(); }
            draw();
        });
        canvas.addEventListener("pointermove", event => {
            if (!start) return;
            const p = pointFor(event);
            if (pending?.type === "pen") pending.points.push(p);
            else if (pending?.type === "arrow") pending.to = p;
            else if (pending) pending.rect = rectFromPoints(start, p);
            else if (tool === "crop") { crop = constrainCrop(rectFromPoints(start, p), aspect, rasterAspect()); placeCropBox(); }
            draw();
        });
        const endStroke = event => {
            if (!start) return;
            const p = pointFor(event);
            const from = start;
            start = null;
            if (tool === "text") {
                void addText(p);
                return;
            }
            const op = pending;
            pending = null;
            if (!op) { draw(); return; }
            const big = r => r.w > 0.005 && r.h > 0.005;
            if (op.type === "pen" || (op.type === "arrow" && Math.hypot(op.to.x - from.x, op.to.y - from.y) > 0.01) ||
                ((op.type === "rect" || op.type === "pixelate" || op.type === "redact") && big(op.rect))) commit(op);
            else draw();
        };
        canvas.addEventListener("pointerup", endStroke);
        canvas.addEventListener("pointercancel", () => { start = null; pending = null; draw(); });

        async function addText(at) {
            const text = await promptDialog({ title: t("chat.imageEditor.textTitle"), label: t("chat.imageEditor.textLabel") });
            const value = String(text || "").trim().slice(0, 200);
            if (value && overlay.isConnected) commit({ type: "text", color, at, text: value });
        }

        cropBox.addEventListener("keydown", event => {
            if (!crop) return;
            if (event.key === "Enter") { event.preventDefault(); finishCrop(); return; }
            const next = nudgeCrop(crop, event.key, { resize: event.shiftKey, step: event.altKey ? 0.002 : 0.01, aspect, rasterAspect: rasterAspect() });
            if (next !== crop) {
                event.preventDefault();
                crop = next;
                placeCropBox();
            }
        });
        dialog.addEventListener("keydown", event => {
            if (!(event.ctrlKey || event.metaKey) || event.altKey) return;
            const key = event.key.toLowerCase();
            if (key === "z" && !event.shiftKey) { event.preventDefault(); undo.click(); }
            else if (key === "y" || (key === "z" && event.shiftKey)) { event.preventDefault(); redo.click(); }
        });

        done.onclick = async () => {
            if (busy) return;
            if (tool === "crop" && crop) finishCrop();
            if (!history.dirty) { closeDialog(overlay); return; }
            busy = true;
            done.disabled = undo.disabled = redo.disabled = true;
            hint.textContent = t("chat.imageEditor.saving");
            try {
                const { blob } = await exportEdited({
                    source: image, width: fullWidth, height: fullHeight, sourceType, ops: history.ops,
                    makeCanvas, encode: encodeCanvas, maxBytes,
                });
                const dataURL = await blobDataURL(blob);
                if (overlay.isConnected && isCurrent()) {
                    result = { ...staged, name: exportName(staged.name, blob.type), dataBase64: dataURL.split(",")[1] || "", isImage: true, dataURL };
                }
                closeDialog(overlay);
            } catch (error) {
                busy = false;
                done.disabled = false;
                undo.disabled = !history.canUndo();
                redo.disabled = !history.canRedo();
                hint.textContent = "";
                toast(t(error instanceof ExportTooLargeError ? "chat.imageEditor.tooLarge" : "chat.imageEditor.saveFailed"), "warn");
            }
        };
        cancel.onclick = () => closeDialog(overlay, "cancel");

        mountDialog(overlay, {
            initialFocus: toolButtons.get("pen"),
            onCancel: () => {
                if (busy) return false;
                if (!history.dirty || discardConfirmed) { result = null; return true; }
                void confirmDialog({ title: t("chat.imageEditor.discardTitle"), message: t("chat.imageEditor.discardMessage"), confirmLabel: t("chat.imageEditor.discard"), danger: true })
                    .then(ok => { if (ok) { discardConfirmed = true; closeDialog(overlay, "cancel"); } });
                return false;
            },
            onClose: () => {
                // Release the large rasters at once instead of waiting for GC.
                for (const c of [previewBase, committed, canvas]) if (c) { c.width = 0; c.height = 0; }
                resolve(result);
            },
        });
        rebuild();
        setTool("pen");
    });
}
