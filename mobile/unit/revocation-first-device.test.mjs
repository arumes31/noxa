// The first device is the account identity: no revocation signed by another
// device removes it, whatever device ID or key the entry carries, and a
// device already pinned as revoked signs no further revocations.
import assert from "node:assert/strict";
import { test } from "node:test";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { DirectMessages } = await import("../src/core/direct.js");
const { generateIdentity, publicKeyPEM, toBase64, sign } = await import("../src/core/crypto.js");
const { signKeyRecord, approvalTranscript, revocationTranscript, trustedDevices, withoutRevokedPins } = await import("../src/core/dm-trust.js");

const session = Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: generateIdentity(), uniqueID: "me-first", deviceID: "me-dev", nickname: "me" });

function account(uid) {
    const root = generateIdentity(), laptop = generateIdentity(), phone = generateIdentity();
    const pem = id => publicKeyPEM(id.edPublic), x = id => toBase64(id.xPublic);
    const approved = (deviceID, id) => ({ device_id: deviceID, kind: "desktop", ed25519_public_key: pem(id), x25519_public_key: x(id), approved_by: "root",
        approval_signature: toBase64(sign(approvalTranscript(uid, "root", pem(id), x(id)), root.edSecret)) });
    const devices = [{ device_id: "root", kind: "desktop", first_device: true, ed25519_public_key: pem(root), x25519_public_key: x(root) },
        approved("laptop", laptop), approved("phone", phone)];
    const revocation = (signerID, signer, deviceID, target) => ({ device_id: deviceID, ed25519_public_key: pem(target), x25519_public_key: x(target), revoked_at: 5, reason: "user",
        signer_device_id: signerID, signed_at: 5, signature: toBase64(sign(revocationTranscript(uid, signerID, deviceID, pem(target), x(target), 5, "user"), signer.edSecret)) });
    return { uid, root, laptop, phone, devices, revocation, record: signKeyRecord(uid, root.xPublic, root.edSecret) };
}

function client() {
    const dm = new DirectMessages(session);
    dm.conn.request = async (_type, msg) => ({ unique_id: msg.unique_id, ...dm.respond });
    return dm;
}
const ids = entry => entry.devices.map(d => d.deviceID);

test("a revocation by another device that carries the first device's key is ignored", async () => {
    const a = account("first-a");
    const dm = client();
    dm.respond = { public_key: a.record, devices: a.devices, revocations: [a.revocation("laptop", a.laptop, "other-id", a.root)] };
    assert.deepEqual(ids(await dm.devicesOf(a.uid)), ["root", "laptop", "phone"]);
    dm.respond = { public_key: a.record, devices: a.devices };
    assert.deepEqual(ids(await dm.devicesOf(a.uid, { refresh: true })), ["root", "laptop", "phone"]);
});

test("the first device still revokes itself and other devices", async () => {
    const a = account("first-b");
    const dm = client();
    dm.respond = { public_key: a.record, devices: a.devices, revocations: [a.revocation("root", a.root, "laptop", a.laptop)] };
    assert.deepEqual(ids(await dm.devicesOf(a.uid)), ["root", "phone"]);
    const b = account("first-c");
    dm.respond = { public_key: b.record, devices: b.devices, revocations: [b.revocation("root", b.root, "root", b.root)] };
    assert.equal(ids(await dm.devicesOf(b.uid)).includes("root"), false);
});

test("a stored pin with the first device's key no longer removes it", () => {
    const a = account("first-d");
    const trusted = trustedDevices(a.uid, a.devices, a.root.edPublic, a.root.xPublic);
    const stored = [{ device_id: "other-id", key: toBase64(a.root.edPublic), signer: "laptop" }, { device_id: "root", key: toBase64(a.root.edPublic) }];
    assert.deepEqual(withoutRevokedPins(trusted, stored).map(d => d.deviceID), ["root", "laptop", "phone"]);
    assert.deepEqual(withoutRevokedPins(trusted, [{ device_id: "root", key: toBase64(a.root.edPublic), signer: "root" }]).map(d => d.deviceID), ["laptop", "phone"]);
});

test("a device pinned as revoked signs no further revocations when listed again", async () => {
    const a = account("first-e");
    const dm = client();
    dm.respond = { public_key: a.record, devices: a.devices, revocations: [a.revocation("root", a.root, "laptop", a.laptop)] };
    assert.deepEqual(ids(await dm.devicesOf(a.uid)), ["root", "phone"]);
    dm.respond = { public_key: a.record, devices: a.devices, revocations: [a.revocation("laptop", a.laptop, "phone", a.phone)] };
    assert.deepEqual(ids(await dm.devicesOf(a.uid, { refresh: true })), ["root", "phone"]);
});
