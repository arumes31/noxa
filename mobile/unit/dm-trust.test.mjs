import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { fromBase64, toBase64 } from "../src/core/crypto.js";
import { ed25519FromPEM, parseKeyRecord, trustedDevices } from "../src/core/dm-trust.js";

const fixture = JSON.parse(readFileSync(new URL("./fixtures/interop.json", import.meta.url)));

test("a Go-signed key record verifies and names its keys", () => {
    const record = parseKeyRecord(fixture.key_record, fixture.unique_id);
    assert.ok(record);
    assert.deepEqual(record.signingKey, ed25519FromPEM(fixture.public_key_pem));
    assert.equal(toBase64(record.encryptionKey), fixture.x25519_public);
    assert.equal(parseKeyRecord(fixture.key_record, "someone-else"), null);
    // Any change to the body breaks the signature.
    const body = JSON.parse(new TextDecoder().decode(fromBase64(fixture.key_record.slice("noxa-dm-key-v1:".length))));
    body.encryption_key = toBase64(new Uint8Array(32).fill(7));
    const forged = "noxa-dm-key-v1:" + toBase64(new TextEncoder().encode(JSON.stringify(body)));
    assert.equal(parseKeyRecord(forged, fixture.unique_id), null);
});

function devices({ signature = fixture.approval_signature, x = fixture.x25519_public, approver = "dev-desktop" } = {}) {
    return [
        { device_id: "dev-desktop", kind: "desktop", first_device: true, ed25519_public_key: fixture.public_key_pem, x25519_public_key: fixture.peer_x25519_public },
        { device_id: "dev-phone", kind: "web", ed25519_public_key: fixture.public_key_pem, x25519_public_key: x, approved_by: approver, approval_signature: signature },
    ];
}

test("devices chain to the pinned identity through approval signatures", () => {
    const pinned = ed25519FromPEM(fixture.public_key_pem);
    const trusted = trustedDevices(fixture.approval_principal, devices(), pinned);
    assert.deepEqual(trusted.map(d => d.deviceID), ["dev-desktop", "dev-phone"]);
    assert.equal(trusted[1].x25519B64, fixture.x25519_public);

    // A forged approval, a changed key or an unknown approver is dropped.
    const forged = toBase64(new Uint8Array(64).fill(1));
    assert.deepEqual(trustedDevices(fixture.approval_principal, devices({ signature: forged }), pinned).map(d => d.deviceID), ["dev-desktop"]);
    assert.deepEqual(trustedDevices(fixture.approval_principal, devices({ x: fixture.peer_x25519_public }), pinned).map(d => d.deviceID), ["dev-desktop"]);
    assert.deepEqual(trustedDevices(fixture.approval_principal, devices({ approver: "dev-ghost" }), pinned).map(d => d.deviceID), ["dev-desktop"]);
    // Another principal's transcript does not verify.
    assert.deepEqual(trustedDevices("other-uid", devices(), pinned).map(d => d.deviceID), ["dev-desktop"]);
    // A first device that is not the pinned identity trusts nothing.
    assert.deepEqual(trustedDevices(fixture.approval_principal, devices(), new Uint8Array(32)), []);
});
