import test from "node:test";
import assert from "node:assert/strict";
import { animatedChatImageSource } from "../src/chat-animation-source.js";

const source = (type, bytes) => `data:image/${type};base64,${Buffer.from(bytes).toString("base64")}`;
const pngSignature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
function chunk(type, payload = Buffer.alloc(0)) {
    const bytes = Buffer.alloc(payload.length + 12);
    bytes.writeUInt32BE(payload.length); bytes.write(type, 4); payload.copy(bytes, 8);
    return bytes; // CRC is irrelevant to the header-only animation inspection.
}
const png = (...chunks) => source("png", Buffer.concat([pngSignature, ...chunks]));

test("WebP checks the VP8X animation flag, not MIME or a string inside pixels", () => {
    const bytes = Buffer.alloc(30);
    bytes.write("RIFF"); bytes.writeUInt32LE(22, 4); bytes.write("WEBPVP8X", 8); bytes.writeUInt32LE(10, 16);
    assert.equal(animatedChatImageSource(source("webp", bytes)), false);
    bytes[20] = 2;
    assert.equal(animatedChatImageSource(source("webp", bytes)), true);
    bytes.write("VP8 ", 12); bytes.write("ANIM", 20);
    assert.equal(animatedChatImageSource(source("webp", bytes)), false);
    bytes.write("VP8L", 12);
    assert.equal(animatedChatImageSource(source("webp", bytes)), false);
});

test("APNG checks chunk boundaries before IDAT and skips large metadata without decoding it", () => {
    const header = chunk("IHDR", Buffer.alloc(13));
    assert.equal(animatedChatImageSource(png(header, chunk("acTL", Buffer.alloc(8)), chunk("IDAT"))), true);
    assert.equal(animatedChatImageSource(png(header, chunk("tEXt", Buffer.from("acTL")), chunk("IDAT"))), false);
    assert.equal(animatedChatImageSource(png(header, chunk("IDAT", Buffer.from("acTL")))), false);
    const metadata = chunk("iCCP", Buffer.alloc(1024 * 1024));
    const original = globalThis.atob;
    let decoded = 0;
    globalThis.atob = value => { decoded += value.length; return original(value); };
    try {
        assert.equal(animatedChatImageSource(png(header, metadata, chunk("acTL", Buffer.alloc(8)))), true);
        assert.ok(decoded < 150, "metadata bytes must not be decoded just to find animation");
    } finally { globalThis.atob = original; }
});

test("ambiguous or oversized headers are conservatively managed with bounded work", () => {
    assert.equal(animatedChatImageSource("data:image/png;base64,not!base64"), true);
    assert.equal(animatedChatImageSource("data:image/webp,%52%49%46%46"), true);
    const overlong = png(...Array.from({ length: 140 }, () => chunk("tEXt")), chunk("IDAT"));
    assert.equal(animatedChatImageSource(overlong), true);
    const broken = chunk("tEXt"); broken.writeUInt32BE(0xffffffff);
    assert.equal(animatedChatImageSource(png(broken)), true);
});

test("remote/extensionless images retain conservative handling without new network requests", () => {
    for (const value of ["https://cdn.example/image", "blob:https://noxa.example/test", "data:image/gif;base64,x", "data:image/apng;base64,x"]) {
        assert.equal(animatedChatImageSource(value), true);
    }
    for (const value of ["", "data:image/jpeg;base64,x", "data:image/svg+xml,x", "data:image/avif;base64,x"]) {
        assert.equal(animatedChatImageSource(value), false);
    }
});
