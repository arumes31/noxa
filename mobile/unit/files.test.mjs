import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { sha256 } from "@noble/hashes/sha2.js";

import { fromBase64, toBase64 } from "../src/core/crypto.js";
import { fileToken, openAttachment, parseFileTokens, sealAttachment, storageName } from "../src/core/files.js";

const fixture = JSON.parse(readFileSync(new URL("./fixtures/interop.json", import.meta.url)));

test("a Go-sealed attachment opens", async () => {
    const plain = await openAttachment(fromBase64(fixture.attachment_blob), fromBase64(fixture.attachment_key));
    assert.equal(plain.length, 70000);
    assert.equal(toBase64(sha256(plain)), fixture.attachment_plain_sha256);
    await assert.rejects(openAttachment(fromBase64(fixture.attachment_blob), new Uint8Array(32)));
});

test("attachments seal, name and round-trip like the desktop", async () => {
    const plain = new Uint8Array(150000).map((_, i) => i % 251);
    const { blob, master } = await sealAttachment(plain);
    assert.deepEqual(await openAttachment(blob, master), plain);
    assert.match(storageName(blob), /^[0-9a-f]{32}\.vcx$/);
    const token = fileToken(storageName(blob), master, plain.length, "photo#1].png");
    const [ref] = parseFileTokens(`look ${token} here`);
    assert.equal(ref.storage, storageName(blob));
    assert.deepEqual(ref.master, master);
    assert.equal(ref.size, 150000);
    assert.equal(ref.name, "photo_1_.png");
    // Tampering is detected.
    blob[blob.length - 1] ^= 1;
    await assert.rejects(openAttachment(blob, master));
});
