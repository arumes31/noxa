// 1c-iii-b item 0, tightened by Task 4 review P1-1: own-account trust never
// comes from the server-relayed claim fingerprint, nor from a bare anchor
// without the verified desktop link statement (own-root-anchor.test.mjs
// covers the anchored cases).
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { DirectMessages } = await import("../src/core/direct.js");
const { keyFingerprint, generateIdentity, publicKeyPEM } = await import("../src/core/crypto.js");
const fixture = JSON.parse(readFileSync(new URL("./fixtures/interop.json", import.meta.url)));

const session = uid => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: generateIdentity(), uniqueID: uid, deviceID: uid + "-dev", nickname: uid });
const lookup = async meta => {
    const uid = "self-" + Math.random().toString(36).slice(2);
    const dm = new DirectMessages(session(uid), meta);
    // The server presents its own key as this account's first device.
    dm.conn.request = async () => ({ unique_id: uid, public_key: "", devices: [{ device_id: "d1", kind: "desktop", first_device: true, ed25519_public_key: fixture.public_key_pem, x25519_public_key: fixture.peer_x25519_public }] });
    return (await dm.devicesOf(uid)).trust;
};

test("neither a server-supplied approver fingerprint nor a bare anchor makes the server's own-account key trusted", async () => {
    const serverFP = keyFingerprint(fixture.public_key_pem);
    const realDesktop = keyFingerprint(publicKeyPEM(generateIdentity().edPublic));
    assert.equal(await lookup({ approverFingerprint: serverFP, bindingAnchor: realDesktop }), "unavailable", "anchor mismatch: not trusted");
    assert.equal(await lookup({ approverFingerprint: realDesktop, bindingAnchor: serverFP }), "unavailable", "an anchor without the verified statement");
    assert.equal(await lookup({ approverFingerprint: serverFP }), "unavailable", "links without an anchor no longer trust the claim fingerprint");
});
