import assert from "node:assert/strict";
import test from "node:test";
import { clipboardFiles, preparePastedFiles, webpFromPaste } from "../src/paste-images.js";

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
const png = (bytes = 100, name = "shot.png") => new File([new Uint8Array(bytes)], name, { type: "image/png" });

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
