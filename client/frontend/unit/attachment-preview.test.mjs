import assert from "node:assert/strict";
import test from "node:test";
import { decodeTextPreview, previewKind } from "../src/attachment-preview.js";

const b64 = text => Buffer.from(text).toString("base64");

test("audio and text extensions get previews, others do not", () => {
    assert.equal(previewKind("MP3"), "audio");
    assert.equal(previewKind("opus"), "audio");
    assert.equal(previewKind("go"), "text");
    assert.equal(previewKind("log"), "text");
    for (const ext of ["exe", "zip", "pdf", "", undefined, "toString"]) assert.equal(previewKind(ext), "");
});

test("text previews keep the first lines and report the total", () => {
    const content = Array.from({ length: 50 }, (_, i) => `line ${i + 1}`).join("\r\n") + "\r\n";
    const preview = decodeTextPreview(b64("\uFEFF" + content), 3);
    assert.equal(preview.head, "line 1\nline 2\nline 3");
    assert.equal(preview.lines, 50);
    assert.equal(preview.truncated, true);
    assert.ok(!preview.text.startsWith("\uFEFF"));
    const short = decodeTextPreview(b64("<script>alert(1)</script>"));
    assert.equal(short.head, "<script>alert(1)</script>", "markup stays literal text");
    assert.equal(short.truncated, false);
});

test("binary or malformed content is not previewed", () => {
    assert.equal(decodeTextPreview(Buffer.from([0x50, 0x4b, 0x03, 0x04, 0, 0, 1]).toString("base64")), null);
    assert.equal(decodeTextPreview(Buffer.from([0xff, 0xfe, 0xfd]).toString("base64")), null, "invalid UTF-8");
    assert.equal(decodeTextPreview("%%%not-base64"), null);
});
