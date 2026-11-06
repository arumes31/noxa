import assert from "node:assert/strict";
import test from "node:test";
import { MAX_CONVERT_PIXELS, clipboardFiles, imageHeaderSize, preparePastedFiles, webpFromPaste } from "../src/paste-images.js";

function encoder(size, type = "image/webp") {
    const calls = [];
    return {
        calls,
        options: {
            decode: async () => ({ width: 4, height: 3, close() { calls.push("close"); } }),
            makeCanvas: (w, h) => ({
                getContext: () => ({ drawImage: () => calls.push(`draw ${w}x${h}`) }),
                convertToBlob: async opts => { calls.push(opts); return new Blob([new Uint8Array(size)], { type }); },
            }),
        },
    };
}
// A file of the given size that starts with a PNG signature and IHDR size.
function pngBytes(bytes, width = 4, height = 3) {
    const data = new Uint8Array(Math.max(bytes, 24));
    data.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
    const view = new DataView(data.buffer);
    view.setUint32(16, width);
    view.setUint32(20, height);
    return data;
}
const png = (bytes = 100, name = "shot.png", width = 4, height = 3) => new File([pngBytes(bytes, width, height)], name, { type: "image/png" });

test("a pasted PNG becomes a clearly smaller high-quality WebP", async () => {
    const e = encoder(40);
    const out = await webpFromPaste(png(), e.options);
    assert.equal(out.type, "image/webp");
    assert.equal(out.name, "shot.webp");
    assert.equal(out.size, 40);
    assert.deepEqual(e.calls, ["draw 4x3", { type: "image/webp", quality: 0.92 }, "close"]);
});

test("the original is kept when WebP is not smaller or encoding is unsupported", async () => {
    const file = png(100);
    assert.equal(await webpFromPaste(file, encoder(100).options), file);
    assert.equal(await webpFromPaste(file, encoder(95).options), file, "a saving under 10% keeps the original");
    assert.equal(await webpFromPaste(file, encoder(10, "image/png").options), file, "no WebP encoder: canvas falls back to PNG");
    assert.equal(await webpFromPaste(file, { decode: async () => { throw new Error("corrupt"); } }), file);
});

test("GIF, JPEG and WebP pass through untouched", async () => {
    const e = encoder(1);
    for (const type of ["image/gif", "image/jpeg", "image/webp", "application/pdf"]) {
        const file = new File([new Uint8Array(50)], "x", { type });
        assert.equal(await webpFromPaste(file, e.options), file);
    }
    assert.deepEqual(e.calls, []);
});

test("clipboard items without files are named; order is preserved", async () => {
    const blob = new Blob([new Uint8Array(5)], { type: "image/png" });
    const files = clipboardFiles({ files: [], items: [{ kind: "string", type: "text/plain" }, { kind: "file", type: "image/png", getAsFile: () => blob }] }, 123);
    assert.equal(files.length, 1);
    assert.equal(files[0].name, "clipboard-123.png");
    const text = new File(["t"], "a.txt", { type: "text/plain" });
    const out = await preparePastedFiles([text, png(100, "b.png")], encoder(20).options);
    assert.deepEqual(out.map(f => f.name), ["a.txt", "b.webp"]);
});

test("images above the pixel limit, or without a known header, are not decoded", async () => {
    const e = encoder(1);
    const huge = png(100, "huge.png", 10_000, 10_000);
    assert.ok(10_000 * 10_000 > MAX_CONVERT_PIXELS);
    assert.equal(await webpFromPaste(huge, e.options), huge);
    const unknown = new File([new Uint8Array(100)], "x.png", { type: "image/png" });
    assert.equal(await webpFromPaste(unknown, e.options), unknown);
    assert.deepEqual(e.calls, [], "nothing was decoded");
});

test("header sizes are read from PNG and BMP files", async () => {
    assert.deepEqual(await imageHeaderSize(png(40, "a.png", 640, 480)), { width: 640, height: 480 });
    const bmp = new Uint8Array(54);
    bmp.set([0x42, 0x4d]);
    const view = new DataView(bmp.buffer);
    view.setInt32(18, 800, true);
    view.setInt32(22, -600, true); // top-down BMP
    assert.deepEqual(await imageHeaderSize(new File([bmp], "a.bmp", { type: "image/bmp" })), { width: 800, height: 600 });
    assert.equal(await imageHeaderSize(new File([new Uint8Array(4)], "short.png")), null);
});
