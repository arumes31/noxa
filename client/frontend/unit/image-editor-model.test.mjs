import assert from "node:assert/strict";
import test from "node:test";
import {
    ExportTooLargeError, MAX_EXPORT_PIXELS, MIN_CROP, constrainCrop, createHistory, dataURLType, exportEdited, exportName,
    exportPlan, fitScale, isAnimatedImage, isEditable, nudgeCrop, pixelRect, pixelateImageData, rasterSize, rectFromPoints,
    renderOps, sizeUnit, strokeWidth,
} from "../src/image-editor-model.js";

const close = (a, b, eps = 1e-9) => Math.abs(a - b) < eps;

// A canvas stand-in that records drawing and keeps a real RGBA buffer for
// getImageData/putImageData.
function fakeCanvas(width, height, log = []) {
    const pixels = new Uint8ClampedArray(width * height * 4);
    const canvas = { width, height, pixels, log };
    const ctx = {
        save() {}, restore() {}, translate(...a) { log.push(["translate", ...a]); }, rotate(a) { log.push(["rotate", a]); },
        beginPath() {}, moveTo() {}, lineTo() {}, stroke() { log.push(["stroke", ctx.strokeStyle, ctx.lineWidth]); },
        strokeRect(...a) { log.push(["strokeRect", ...a]); }, strokeText(text) { log.push(["strokeText", text]); },
        fillText(text) { log.push(["fillText", text, ctx.fillStyle]); },
        drawImage(...a) { log.push(["drawImage", a.length]); },
        fillRect(...a) { log.push(["fillRect", ctx.fillStyle, ctx.globalAlpha, ...a]); },
        getImageData(x, y, w, h) {
            const data = new Uint8ClampedArray(w * h * 4);
            for (let row = 0; row < h; row++) for (let col = 0; col < w; col++) {
                const src = ((y + row) * width + (x + col)) * 4, dst = (row * w + col) * 4;
                data.set(pixels.subarray(src, src + 4), dst);
            }
            return { data, width: w, height: h };
        },
        putImageData(image, x, y) {
            log.push(["putImageData", x, y, image.width, image.height]);
            for (let row = 0; row < image.height; row++) for (let col = 0; col < image.width; col++) {
                const dst = ((y + row) * width + (x + col)) * 4, src = (row * image.width + col) * 4;
                pixels.set(image.data.subarray(src, src + 4), dst);
            }
        },
    };
    canvas.getContext = () => ctx;
    return canvas;
}

test("editable staged attachments are still images the editor can re-encode", () => {
    assert.equal(dataURLType("data:image/png;base64,AAAA"), "image/png");
    assert.equal(dataURLType("nonsense"), "");
    assert.ok(isEditable({ dataURL: "data:image/jpeg;base64,AA" }));
    assert.ok(isEditable({ dataURL: "data:image/bmp;base64,AA" }));
    assert.ok(!isEditable({ dataURL: "data:image/gif;base64,AA" }), "animated GIFs would be flattened");
    assert.ok(!isEditable({ dataURL: null }));
    assert.ok(!isEditable(null));
});

test("fitScale bounds the pixel count and never upscales", () => {
    assert.equal(fitScale(100, 100, 1_000_000), 1);
    const s = fitScale(10_000, 10_000, MAX_EXPORT_PIXELS);
    assert.ok(close(10_000 * s * 10_000 * s, MAX_EXPORT_PIXELS, 1));
    assert.equal(fitScale(0, 10, 100), 0);
});

test("rectangles from two points are normalised and clamped", () => {
    assert.deepEqual(rectFromPoints({ x: 0.8, y: 0.9 }, { x: 0.2, y: 0.1 }), { x: 0.2, y: 0.1, w: 0.6000000000000001, h: 0.8 });
    const r = rectFromPoints({ x: -0.5, y: 0.5 }, { x: 1.5, y: 2 });
    assert.deepEqual(r, { x: 0, y: 0.5, w: 1, h: 0.5 });
});

test("an aspect ratio constrains the crop in pixels, inside the raster", () => {
    // A 2:1 raster; a square crop is half as wide as tall in fractions.
    const square = constrainCrop({ x: 0, y: 0, w: 1, h: 1 }, 1, 2);
    assert.ok(close(square.w * 2, square.h));
    assert.ok(square.x + square.w <= 1 && square.y + square.h <= 1);
    const wide = constrainCrop({ x: 0.5, y: 0.5, w: 0.9, h: 0.9 }, 16 / 9, 1);
    assert.ok(wide.x + wide.w <= 1 + 1e-9 && wide.y + wide.h <= 1 + 1e-9);
    assert.ok(close(wide.w / wide.h, 16 / 9));
    assert.deepEqual(constrainCrop({ x: 0.1, y: 0.1, w: 0.5, h: 0.2 }, 0, 1), { x: 0.1, y: 0.1, w: 0.5, h: 0.2 }, "free keeps the shape");
});

test("arrow keys move the crop, Shift+arrows resize it, both stay inside", () => {
    const r = { x: 0.1, y: 0.1, w: 0.5, h: 0.5 };
    assert.deepEqual(nudgeCrop(r, "ArrowRight"), { x: 0.11, y: 0.1, w: 0.5, h: 0.5 });
    assert.deepEqual(nudgeCrop({ ...r, x: 0.5 }, "ArrowRight", { step: 0.2 }), { x: 0.5, y: 0.1, w: 0.5, h: 0.5 }, "cannot leave the raster");
    const bigger = nudgeCrop(r, "ArrowDown", { resize: true });
    assert.ok(close(bigger.h, 0.51) && bigger.w === 0.5);
    const tiny = nudgeCrop({ x: 0, y: 0, w: 0.02, h: 0.02 }, "ArrowLeft", { resize: true });
    assert.ok(tiny.w >= 0.02, "a crop never shrinks below the minimum");
    const locked = nudgeCrop(r, "ArrowRight", { resize: true, aspect: 1, rasterAspect: 1 });
    assert.ok(close(locked.w, locked.h), "aspect stays locked while resizing");
    assert.equal(nudgeCrop(r, "Tab"), r);
});

test("history supports undo and redo, and a new edit clears redo", () => {
    const h = createHistory();
    assert.equal(h.dirty, false);
    h.push({ type: "rotate" });
    h.push({ type: "pen" });
    assert.deepEqual(h.ops.map(o => o.type), ["rotate", "pen"]);
    h.undo();
    assert.ok(h.canRedo());
    assert.deepEqual(h.ops.map(o => o.type), ["rotate"]);
    h.redo();
    assert.deepEqual(h.ops.map(o => o.type), ["rotate", "pen"]);
    h.undo();
    h.push({ type: "rect" });
    assert.equal(h.canRedo(), false);
    h.undo(); h.undo(); h.undo();
    assert.equal(h.dirty, false);
});

test("rotate swaps the raster and crop scales it", () => {
    assert.deepEqual(rasterSize(400, 300, [{ type: "rotate" }]), { width: 300, height: 400 });
    assert.deepEqual(rasterSize(400, 300, [{ type: "crop", rect: { x: 0, y: 0, w: 0.5, h: 0.5 } }, { type: "rotate" }]), { width: 150, height: 200 });
    assert.deepEqual(pixelRect({ x: 0.25, y: 0.5, w: 0.5, h: 0.5 }, 100, 10), { x: 25, y: 5, w: 50, h: 5 });
    const made = [];
    const out = renderOps(fakeCanvas(40, 20), [{ type: "rotate" }, { type: "crop", rect: { x: 0, y: 0, w: 0.5, h: 1 } }],
        (w, h) => { made.push([w, h]); return fakeCanvas(w, h); });
    assert.deepEqual(made, [[20, 40], [10, 40]]);
    assert.deepEqual([out.width, out.height], [10, 40]);
});

test("pixelation leaves only block averages, so the original cannot be read back", () => {
    const image = { width: 4, height: 2, data: new Uint8ClampedArray(4 * 2 * 4) };
    // Distinct values in the first 2x2 block, a different block after it.
    const values = [0, 40, 80, 120, 200, 200, 200, 200];
    values.forEach((v, i) => image.data.set([v, v, v, 255], i * 4));
    pixelateImageData(image, 2);
    const at = i => image.data[i * 4];
    // Pixels 0,1,4,5 form the first block: average of 0,40,200,200 = 110.
    for (const i of [0, 1, 4, 5]) assert.equal(at(i), 110);
    for (const i of [2, 3, 6, 7]) assert.equal(at(i), 150);
    assert.ok(![0, 40].includes(at(0)));
});

test("a pixelate op rewrites the region's pixels with coarse blocks", () => {
    const canvas = fakeCanvas(64, 64);
    for (let i = 0; i < canvas.pixels.length; i += 4) canvas.pixels.set([i % 251, 7, 9, 255], i);
    const before = canvas.pixels.slice();
    renderOps(canvas, [{ type: "pixelate", rect: { x: 0, y: 0, w: 0.5, h: 0.5 } }], fakeCanvas);
    assert.ok(canvas.log.some(entry => entry[0] === "putImageData"));
    // Inside the region, neighbours within a block are now equal.
    assert.equal(canvas.pixels[0], canvas.pixels[4]);
    // Outside the region nothing changed.
    const outside = (40 * 64 + 40) * 4;
    assert.equal(canvas.pixels[outside], before[outside]);
});

test("annotations draw with a stroke width relative to the raster", () => {
    const small = fakeCanvas(200, 100), large = fakeCanvas(2000, 1000);
    const ops = [{ type: "pen", color: "#ff0000", points: [{ x: 0, y: 0 }, { x: 1, y: 1 }] }];
    renderOps(small, ops, fakeCanvas);
    renderOps(large, ops, fakeCanvas);
    const width = c => c.log.find(e => e[0] === "stroke")[2];
    assert.ok(width(large) > width(small));
    const text = fakeCanvas(100, 100);
    renderOps(text, [{ type: "text", text: "hi", color: "#000000", at: { x: 0.1, y: 0.1 } }, { type: "arrow", from: { x: 0, y: 0 }, to: { x: 1, y: 1 } }, { type: "rect", rect: { x: 0, y: 0, w: 1, h: 1 } }], fakeCanvas);
    assert.ok(text.log.some(e => e[0] === "fillText" && e[1] === "hi"));
    assert.ok(text.log.some(e => e[0] === "strokeRect"));
});

test("photos stay JPEG, screenshots stay PNG unless WebP is clearly smaller", () => {
    assert.deepEqual(exportPlan("image/jpeg"), { type: "image/jpeg", quality: 0.92, tryWebP: false });
    assert.equal(exportPlan("image/png").type, "image/png");
    assert.ok(exportPlan("image/bmp").tryWebP);
    assert.equal(exportName("shot_ab12.png", "image/webp"), "shot_ab12.webp");
    assert.equal(exportName("photo.jpeg", "image/jpeg"), "photo.jpg");
    assert.equal(exportName("", "image/png"), "image.png");
});

function encoder(sizes) {
    const calls = [];
    return {
        calls,
        encode: async (canvas, type, quality) => {
            calls.push([type, quality, canvas.width, canvas.height]);
            const size = sizes[type] ?? 100;
            return new Blob([new Uint8Array(size)], { type });
        },
    };
}

test("export renders at full resolution and keeps the smaller WebP", async () => {
    const e = encoder({ "image/png": 1000, "image/webp": 500 });
    const made = [];
    const out = await exportEdited({
        source: {}, width: 300, height: 200, sourceType: "image/png", ops: [{ type: "rotate" }],
        makeCanvas: (w, h) => { made.push([w, h]); return fakeCanvas(w, h); }, encode: e.encode, maxBytes: 10_000,
    });
    assert.deepEqual(made[0], [300, 200], "full resolution base");
    assert.deepEqual([out.width, out.height], [200, 300]);
    assert.equal(out.blob.type, "image/webp");
    const keep = await exportEdited({ source: {}, width: 10, height: 10, sourceType: "image/png", ops: [], makeCanvas: fakeCanvas, encode: encoder({ "image/png": 1000, "image/webp": 950 }).encode });
    assert.equal(keep.blob.type, "image/png", "a saving under 10% keeps PNG");
});

test("export bounds huge images and enforces the size limit", async () => {
    const e = encoder({ "image/jpeg": 50 });
    await exportEdited({ source: {}, width: 20_000, height: 10_000, sourceType: "image/jpeg", ops: [], makeCanvas: fakeCanvas, encode: e.encode, maxPixels: 1_000_000 });
    const [, , w, h] = e.calls[0];
    assert.ok(w * h <= 1_000_000 + 2000 && w * h > 990_000);
    await assert.rejects(exportEdited({ source: {}, width: 10, height: 10, sourceType: "image/png", ops: [], makeCanvas: fakeCanvas, encode: encoder({ "image/png": 5000, "image/webp": 4900 }).encode, maxBytes: 1000 }), ExportTooLargeError);
    const fallback = await exportEdited({ source: {}, width: 10, height: 10, sourceType: "image/png", ops: [], makeCanvas: fakeCanvas,
        encode: async (c, type, q) => new Blob([new Uint8Array(type === "image/webp" && q === 0.85 ? 500 : 5000)], { type }), maxBytes: 1000 });
    assert.equal(fallback.blob.size, 500, "a lossy WebP fallback is used when it fits");
});

const b64 = bytes => Buffer.from(bytes).toString("base64");
function webp(animated) {
    const bytes = new Uint8Array(30);
    bytes.set([...Buffer.from("RIFF")], 0);
    bytes.set([...Buffer.from("WEBPVP8X")], 8);
    bytes[20] = animated ? 0x02 : 0x00;
    return bytes;
}
function png(chunk) {
    const bytes = new Uint8Array(64);
    bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
    bytes.set([...Buffer.from("IHDR")], 12);
    bytes.set([...Buffer.from(chunk)], 37);
    return bytes;
}

test("animated WebP and APNG are not offered for editing", () => {
    assert.ok(isAnimatedImage("image/webp", webp(true)));
    assert.ok(!isAnimatedImage("image/webp", webp(false)));
    assert.ok(isAnimatedImage("image/png", png("acTL")));
    assert.ok(!isAnimatedImage("image/png", png("IDAT")));
    assert.ok(!isEditable({ dataURL: "data:image/webp;base64," + b64(webp(true)) }));
    assert.ok(isEditable({ dataURL: "data:image/webp;base64," + b64(webp(false)) }));
    assert.ok(!isEditable({ dataURL: "data:image/png;base64," + b64(png("acTL")) }));
    assert.ok(isEditable({ dataURL: "data:image/png;base64," + b64(png("IDAT")) }));
});

test("an aspect-locked resize never takes either side under the minimum", () => {
    // 1:1 crop on a 4:1 raster: fractional height is four times the width.
    const rect = { x: 0, y: 0, w: MIN_CROP, h: MIN_CROP * 4 };
    const shrunk = nudgeCrop(rect, "ArrowUp", { resize: true, aspect: 1, rasterAspect: 4 });
    assert.ok(shrunk.w >= MIN_CROP && shrunk.h >= MIN_CROP);
    assert.deepEqual(shrunk, rect, "the step is ignored");
});

test("redaction is an opaque black fill of the region", () => {
    const canvas = fakeCanvas(100, 50);
    renderOps(canvas, [{ type: "redact", rect: { x: 0.25, y: 0.25, w: 0.5, h: 0.5 } }], fakeCanvas);
    // Covers whole pixels outward: 12.5..37.5 becomes rows 12..37.
    assert.deepEqual(canvas.log.find(e => e[0] === "fillRect"), ["fillRect", "#000000", 1, 25, 12, 50, 26]);
});

test("annotation sizes match between the small preview and the export, also after a crop", () => {
    // A 4000x3000 original shown at a quarter of its size, then cropped.
    const preview = sizeUnit(4000, 3000, 0.25), full = sizeUnit(4000, 3000, 1);
    assert.equal(full / preview, 4);
    const ops = [{ type: "crop", rect: { x: 0, y: 0, w: 0.1, h: 0.1 } }, { type: "pen", color: "#fff", points: [{ x: 0, y: 0 }, { x: 1, y: 1 }] }];
    const small = renderOps(fakeCanvas(1000, 750), ops, fakeCanvas, preview);
    const large = renderOps(fakeCanvas(4000, 3000), ops, fakeCanvas, full);
    const width = c => c.log.find(e => e[0] === "stroke")[2];
    // Same size relative to the raster: 4x the pixels at 4x the resolution.
    assert.ok(Math.abs(width(large) / large.width - width(small) / small.width) < 1e-9);
    assert.equal(strokeWidth(0.5), 1, "never thinner than one pixel");
});

test("a 12000x8000 export stays within 50 MP after rounding", async () => {
    const e = encoder({ "image/jpeg": 50 });
    await exportEdited({ source: {}, width: 12_000, height: 8_000, sourceType: "image/jpeg", ops: [], makeCanvas: fakeCanvas, encode: e.encode });
    const [, , w, h] = e.calls[0];
    assert.ok(w * h <= MAX_EXPORT_PIXELS, `${w}x${h}`);
    assert.ok(Math.abs(w / h - 1.5) < 0.001, "aspect kept");
});
