// A contact whose unique ID is derived from its pinned signing key (a
// key-login account) can never be given another key by the server.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { DirectMessages } = await import("../src/core/direct.js");
const { generateIdentity } = await import("../src/core/crypto.js");
const { keyCertifiesUID, signKeyRecord, ed25519FromPEM } = await import("../src/core/dm-trust.js"); // gitleaks:allow (import name, not a secret)
const fixture = JSON.parse(readFileSync(new URL("./fixtures/interop.json", import.meta.url)));

const session = uid => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: generateIdentity(), uniqueID: uid, deviceID: uid + "-dev", nickname: uid });

test("keyCertifiesUID matches the Go key-derived unique ID", () => {
    assert.equal(keyCertifiesUID(ed25519FromPEM(fixture.public_key_pem), fixture.unique_id), true);
    assert.equal(keyCertifiesUID(generateIdentity().edPublic, fixture.unique_id), false);
    assert.equal(keyCertifiesUID(null, fixture.unique_id), false);
});

async function lookups(uid, first) {
    const dm = new DirectMessages(session("me-" + Math.random().toString(36).slice(2)));
    let record = first;
    dm.conn.request = async () => ({ unique_id: uid, public_key: record, devices: [] });
    assert.equal((await dm.devicesOf(uid)).trust, "ok");
    const other = generateIdentity();
    record = signKeyRecord(uid, other.xPublic, other.edSecret);
    return { dm, entry: await dm.devicesOf(uid, { refresh: true }), other };
}

test("a key-derived contact refuses a substituted key without a change prompt", async () => {
    const { dm, entry } = await lookups(fixture.unique_id, fixture.key_record);
    assert.equal(entry.trust, "unavailable");
    await assert.rejects(dm.trustNewIdentity(fixture.unique_id, entry.fingerprint || "x"), /cannot be replaced/);
});

test("a password-account contact keeps the confirmable change", async () => {
    const owner = generateIdentity();
    const { entry } = await lookups("password-peer", signKeyRecord("password-peer", owner.xPublic, owner.edSecret));
    assert.equal(entry.trust, "changed");
});
