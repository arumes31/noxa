import assert from "node:assert/strict";
import { test } from "node:test";
import decode from "jsqr";
import { SafetyScan, SafetyScheme, buildSafetyPayload, checkSafetyScan, fingerprintPairDigest } from "../src/core/safety-qr.js";
import { qrModules } from "../src/ui/qr-image.js";

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

test("both phones compute the same digest, so codes for the pair match", async () => {
    const alice = { uniqueID: "alice", fingerprint: "aaaa bbbb cccc dddd" };
    const bob = { uniqueID: "bob", fingerprint: "1111 2222 3333 4444" };
    const seenByAlice = await fingerprintPairDigest(alice, bob);
    const seenByBob = await fingerprintPairDigest(bob, alice);
    const bobCode = buildSafetyPayload({ scheme: SafetyScheme.fingerprint, me: "bob", peer: "alice", digest: seenByBob });
    assert.equal(checkSafetyScan(bobCode, { scheme: SafetyScheme.fingerprint, me: "alice", peer: "bob", digest: seenByAlice }), SafetyScan.match);
    // A substituted key on Bob's side changes his digest: Alice sees a mismatch.
    const substituted = await fingerprintPairDigest({ ...bob, fingerprint: "9999 2222 3333 4444" }, alice);
    const forged = buildSafetyPayload({ scheme: SafetyScheme.fingerprint, me: "bob", peer: "alice", digest: substituted });
    assert.equal(checkSafetyScan(forged, { scheme: SafetyScheme.fingerprint, me: "alice", peer: "bob", digest: seenByAlice }), SafetyScan.mismatch);
});

test("a desktop code is reported as another scheme", () => {
    const desktop = buildSafetyPayload({ scheme: SafetyScheme.pairwise, me: "bob", peer: "alice", digest: "ab".repeat(32) });
    assert.equal(checkSafetyScan(desktop, { scheme: SafetyScheme.fingerprint, me: "alice", peer: "bob", digest: "ab".repeat(32) }), SafetyScan.otherScheme);
});
