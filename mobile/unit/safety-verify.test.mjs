import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, putSealed, getSealed, currentFence } = await import("../src/core/db.js");
const { generateIdentity, keyFingerprint, toBase64 } = await import("../src/core/crypto.js");
const { DirectMessages } = await import("../src/core/direct.js");

const session = () => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: generateIdentity(), uniqueID: "me", deviceID: "me-device", nickname: "Me" });
const pem = b64 => `-----BEGIN PUBLIC KEY-----\n${b64}\n-----END PUBLIC KEY-----\n`;

test("a scan verifies only the identity whose fingerprint it compared (205)", async () => {
    const dm = new DirectMessages(session(), {}, { fence: await currentFence() });
    await dm.ready;
    const scanned = toBase64(generateIdentity().edPublic);
    const replacement = toBase64(generateIdentity().edPublic);
    await putSealed(Stores.pins, "peer", { signingKey: replacement, verified: false });
    // The scan compared the earlier identity; its replacement stays unverified.
    await assert.rejects(dm.setVerified("peer", true, { fingerprint: keyFingerprint(pem(scanned)) }), /identity changed/);
    assert.equal((await getSealed(Stores.pins, "peer")).verified, false);
    await dm.setVerified("peer", true, { fingerprint: keyFingerprint(pem(replacement)) });
    assert.equal((await getSealed(Stores.pins, "peer")).verified, true);
    // Without a fingerprint (the manual switch) the call is unchanged.
    await dm.setVerified("peer", false);
    assert.equal((await getSealed(Stores.pins, "peer")).verified, false);
    dm.close();
});
