import assert from "node:assert/strict";
import { test } from "node:test";
import decode from "jsqr";
import { SafetyScan, SafetyScheme, buildSafetyPayload, checkSafetyScan, fingerprintPairDigest } from "../src/core/safety-qr.js";
import { qrModules } from "../src/ui/qr-image.js";
import { fileURLToPath } from "node:url";
import { MOBILE_PACKAGES, frontendNotices } from "../../tools/frontend-notices.mjs";

// Renders modules into RGBA pixels with a quiet zone, as a camera would see
// the code, and decodes them with the same jsQR the scanner uses.
function decodeModules(modules, scale = 4, quiet = 4) {
    const size = (modules.length + quiet * 2) * scale;
    const pixels = new Uint8ClampedArray(size * size * 4).fill(255);
    modules.forEach((row, y) => row.forEach((dark, x) => {
        if (!dark) return;
        for (let dy = 0; dy < scale; dy++) for (let dx = 0; dx < scale; dx++) {
            const at = (((y + quiet) * scale + dy) * size + (x + quiet) * scale + dx) * 4;
            pixels[at] = pixels[at + 1] = pixels[at + 2] = 0;
        }
    }));
    return decode(pixels, size, size)?.data ?? null;
}

test("a rendered safety code decodes back to its payload", async () => {
    const digest = await fingerprintPairDigest({ uniqueID: "YWxpY2U=", fingerprint: "1234 abcd 5678 ef90" }, { uniqueID: "Ym9i", fingerprint: "0000 1111 2222 3333" });
    const payload = buildSafetyPayload({ scheme: SafetyScheme.fingerprint, me: "Ym9i", peer: "YWxpY2U=", digest });
    const scanned = decodeModules(qrModules(payload));
    assert.equal(scanned, payload);
    assert.equal(checkSafetyScan(scanned, { scheme: SafetyScheme.fingerprint, me: "YWxpY2U=", peer: "Ym9i", digest }), SafetyScan.match);
});

test("third-party notices cover the QR libraries the app bundles", () => {
    const notices = frontendNotices(fileURLToPath(new URL("../node_modules/", import.meta.url)), MOBILE_PACKAGES);
    assert.match(notices, /^jsqr 1\.4\.0 \(Apache-2\.0\)$/m);
    assert.match(notices, /^qrcode-generator 1\.4\.4 \(MIT\)$/m);
    assert.match(notices, /Copyright \(c\) 2009 Kazuhiko Arase/);
    assert.match(notices, /Permission is hereby granted/);
});
