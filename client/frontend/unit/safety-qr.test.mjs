import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
    SafetyScan, SafetyScheme, buildSafetyPayload, checkSafetyScan, fingerprintPairDigest, parseSafetyPayload,
} from "../src/safety-qr.js";
import { DESKTOP_PACKAGES, frontendNotices } from "../../../tools/frontend-notices.mjs";

const digest = "ab".repeat(32);
const alice = { scheme: SafetyScheme.pairwise, me: "alice", peer: "bob", digest };
const bob = { scheme: SafetyScheme.pairwise, me: "bob", peer: "alice", digest };

test("a payload names the scheme, both accounts and the digest", () => {
    const text = buildSafetyPayload(alice);
    assert.equal(text, `noxa-sn:v1:o:alice:bob:${digest}`);
    assert.deepEqual(parseSafetyPayload(text), { scheme: "o", from: "alice", to: "bob", digest });
    assert.equal(buildSafetyPayload({ ...alice, digest: digest.toUpperCase() }), text, "digests are lower case");
});

test("malformed input is rejected when building and parsing", () => {
    for (const bad of [{ scheme: "x" }, { me: "" }, { peer: "a:b" }, { digest: "ab" }, { digest: "zz".repeat(32) }, { me: "a".repeat(129) }]) {
        assert.throws(() => buildSafetyPayload({ ...alice, ...bad }), TypeError);
    }
    for (const text of [null, 7, "", "hello", `noxa-sn:v2:o:a:b:${digest}`, `noxa-sn:v1:o:a:b:${digest}:extra`,
        `noxa-sn:v1:o:a::${digest}`, `noxa-sn:v1:q:a:b:${digest}`, `noxa-sn:v1:o:a:b:${digest.slice(2)}`, "x".repeat(500)]) {
        assert.equal(parseSafetyPayload(text), null, String(text).slice(0, 40));
    }
});

test("scanning the contact's own code matches in both directions", () => {
    assert.equal(checkSafetyScan(buildSafetyPayload(bob), alice), SafetyScan.match);
    assert.equal(checkSafetyScan(buildSafetyPayload(alice), bob), SafetyScan.match);
});

test("a different digest is a mismatch, never a match", () => {
    const other = buildSafetyPayload({ ...bob, digest: "cd".repeat(32) });
    assert.equal(checkSafetyScan(other, alice), SafetyScan.mismatch);
    const oneNibble = buildSafetyPayload({ ...bob, digest: `${digest.slice(0, 63)}c` });
    assert.equal(checkSafetyScan(oneNibble, alice), SafetyScan.mismatch);
});

test("codes for another pair, my own code, other schemes and junk are told apart", () => {
    assert.equal(checkSafetyScan(buildSafetyPayload(alice), alice), SafetyScan.wrongPair, "my own code");
    assert.equal(checkSafetyScan(buildSafetyPayload({ ...bob, peer: "carol" }), alice), SafetyScan.wrongPair);
    assert.equal(checkSafetyScan(buildSafetyPayload({ ...bob, me: "mallory" }), alice), SafetyScan.wrongPair);
    assert.equal(checkSafetyScan(buildSafetyPayload({ ...bob, scheme: SafetyScheme.fingerprint }), alice), SafetyScan.otherScheme);
    assert.equal(checkSafetyScan("https://example.com", alice), SafetyScan.invalid);
});

test("the fingerprint digest is the same on both sides and changes with any key", async () => {
    const a = { uniqueID: "alice", fingerprint: "1234 abcd 5678 ef90" };
    const b = { uniqueID: "bob", fingerprint: "0000 1111 2222 3333" };
    const ab = await fingerprintPairDigest(a, b);
    assert.match(ab, /^[0-9a-f]{64}$/);
    assert.equal(await fingerprintPairDigest(b, a), ab);
    assert.equal(await fingerprintPairDigest({ ...a, fingerprint: "1234ABCD5678EF90" }, b), ab, "grouping and case do not matter");
    assert.notEqual(await fingerprintPairDigest({ ...a, fingerprint: "1234 abcd 5678 ef91" }, b), ab);
    assert.notEqual(await fingerprintPairDigest({ ...a, uniqueID: "alicia" }, b), ab);
    await assert.rejects(fingerprintPairDigest({ ...a, fingerprint: "" }, b), TypeError);
});

test("the desktop and mobile copies of the module are identical", () => {
    const desktop = readFileSync(fileURLToPath(new URL("../src/safety-qr.js", import.meta.url)), "utf8");
    const mobile = readFileSync(fileURLToPath(new URL("../../../mobile/src/core/safety-qr.js", import.meta.url)), "utf8");
    assert.equal(mobile.replace(/\r\n/g, "\n"), desktop.replace(/\r\n/g, "\n"));
});

test("third-party notices cover the desktop's QR decoder", () => {
    const desktop = frontendNotices(fileURLToPath(new URL("../node_modules/", import.meta.url)), DESKTOP_PACKAGES);
    assert.match(desktop, /^jsqr 1\.4\.0 \(Apache-2\.0\)$/m);
    assert.match(desktop, /Apache License/);
});

test("a native detector that rejects frames hands over to jsQR; a failed load retries", async () => {
    const { createQRDecoder } = await import("../src/qr-scan.js");
    let detects = 0, loads = 0, failLoad = true;
    const win = { BarcodeDetector: class {
        static async getSupportedFormats() { return ["qr_code"]; }
        async detect() { detects++; throw new Error("not supported on this platform"); }
    } };
    const loadJsQR = async () => {
        loads++;
        if (failLoad) { failLoad = false; throw new Error("chunk failed"); }
        return { default: () => ({ data: "decoded by jsQR" }) };
    };
    const decode = await createQRDecoder({ win, loadJsQR });
    const video = { videoWidth: 4, videoHeight: 4 };
    const frame = { canvas: {}, context: { drawImage() {}, getImageData: () => ({ data: new Uint8ClampedArray(64), width: 4, height: 4 }) } };
    await assert.rejects(decode(video, frame), /chunk failed/);
    assert.equal(await decode(video, frame), "decoded by jsQR", "the load is retried after a failure");
    assert.equal(await decode(video, frame), "decoded by jsQR");
    assert.equal(detects, 1, "the native detector is not asked again");
    assert.equal(loads, 2);
});

test("a working native detector is used without loading jsQR", async () => {
    const { createQRDecoder } = await import("../src/qr-scan.js");
    const win = { BarcodeDetector: class {
        static async getSupportedFormats() { return ["qr_code"]; }
        async detect() { return [{ rawValue: "native" }]; }
    } };
    const decode = await createQRDecoder({ win, loadJsQR: async () => { throw new Error("must not load"); } });
    assert.equal(await decode({}, {}), "native");
});
