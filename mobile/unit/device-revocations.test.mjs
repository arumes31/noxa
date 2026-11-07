import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import nacl from "tweetnacl";

import { fromBase64, toBase64 } from "../src/core/crypto.js";
import { ed25519FromPEM, mergeRevocationPins, revocationTranscript, verifyRevocations, withoutRevokedPins } from "../src/core/dm-trust.js";

// Signed by Go (internal/netproto/revocation_interop_test.go).
const fixture = JSON.parse(readFileSync(new URL("./fixtures/revocation.json", import.meta.url)));

const signer = { deviceID: fixture.signer_device_id, signingKey: ed25519FromPEM(fixture.signer_public_key_pem), first: true };
const goRevocation = {
    device_id: fixture.device_id, ed25519_public_key: fixture.device_public_key_pem, x25519_public_key: fixture.x25519_public_key,
    revoked_at: fixture.signed_at, reason: "user", signer_device_id: fixture.signer_device_id, signed_at: fixture.signed_at, signature: fixture.signature,
};

test("the revocation transcript matches Go byte for byte", () => {
    const mine = revocationTranscript(fixture.principal, fixture.signer_device_id, fixture.device_id, fixture.device_public_key_pem,
        fixture.x25519_public_key, fixture.signed_at, "user");
    assert.equal(new TextDecoder().decode(mine), fixture.transcript);
});

test("a Go-signed revocation verifies; tampered or foreign ones do not", () => {
    const ok = verifyRevocations(fixture.principal, [signer], [goRevocation]);
    assert.equal(ok.length, 1);
    assert.equal(ok[0].deviceID, "dev-phone");
    assert.deepEqual(ok[0].signingKey, ed25519FromPEM(fixture.device_public_key_pem));

    const other = { ...signer, deviceID: "dev-laptop", first: false };
    const cases = {
        "another account": verifyRevocations("someone-else", [signer], [goRevocation]),
        "replayed for another device": verifyRevocations(fixture.principal, [signer], [{ ...goRevocation, device_id: "dev-tablet" }]),
        "changed key": verifyRevocations(fixture.principal, [signer], [{ ...goRevocation, x25519_public_key: toBase64(new Uint8Array(32).fill(9)) }]),
        "changed time": verifyRevocations(fixture.principal, [signer], [{ ...goRevocation, signed_at: fixture.signed_at + 1 }]),
        "untrusted signer": verifyRevocations(fixture.principal, [other], [{ ...goRevocation, signer_device_id: "dev-laptop" }]),
        "server only": verifyRevocations(fixture.principal, [signer], [{ device_id: "old", ed25519_public_key: fixture.device_public_key_pem, revoked_at: 1, reason: "admin_reset" }]),
        "forged": verifyRevocations(fixture.principal, [signer], [{ ...goRevocation, signature: toBase64(new Uint8Array(64).fill(1)) }]),
    };
    for (const [name, result] of Object.entries(cases)) assert.equal(result.length, 0, name);
    // Duplicates count once.
    assert.equal(verifyRevocations(fixture.principal, [signer], [goRevocation, goRevocation]).length, 1);
});

test("only the first device may revoke itself", () => {
    const keys = nacl.sign.keyPair();
    const pem = `-----BEGIN PUBLIC KEY-----\n${toBase64(keys.publicKey)}\n-----END PUBLIC KEY-----\n`;
    const root = { deviceID: "root", signingKey: ed25519FromPEM(fixture.signer_public_key_pem), first: true };
    const laptop = { deviceID: "laptop", signingKey: keys.publicKey, first: false };
    const x = toBase64(new Uint8Array(32).fill(3));
    const sig = nacl.sign.detached(revocationTranscript("user-uid", "laptop", "root", fixture.signer_public_key_pem, x, 5, "user"), keys.secretKey);
    const byLaptop = { device_id: "root", ed25519_public_key: fixture.signer_public_key_pem, x25519_public_key: x, revoked_at: 5, reason: "user",
        signer_device_id: "laptop", signed_at: 5, signature: toBase64(sig) };
    assert.equal(verifyRevocations("user-uid", [root, laptop], [byLaptop]).length, 0);
    assert.ok(pem); // the laptop key is a valid device key
});

test("pins merge once and drop devices by ID or signing key", () => {
    const verified = verifyRevocations(fixture.principal, [signer], [goRevocation]);
    const first = mergeRevocationPins(undefined, verified);
    assert.equal(first.added.length, 1);
    const again = mergeRevocationPins(first.pins, verified);
    assert.equal(again.added.length, 0);
    assert.equal(again.pins.length, 1);

    const phoneKey = ed25519FromPEM(fixture.device_public_key_pem);
    const devices = [
        { deviceID: "dev-desktop", signingKey: signer.signingKey },
        { deviceID: "dev-phone-renamed", signingKey: phoneKey },
        { deviceID: "dev-phone", signingKey: new Uint8Array(32).fill(4) },
    ];
    assert.deepEqual(withoutRevokedPins(devices, first.pins).map(d => d.deviceID), ["dev-desktop"]);
    assert.equal(withoutRevokedPins(devices, []).length, 3);
    assert.ok(fromBase64(first.pins[0].key).length === 32);
});
