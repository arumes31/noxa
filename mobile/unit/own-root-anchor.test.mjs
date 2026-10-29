// Task 4 review P1-1 / P2-2 / P3-9: this account's own root key comes only
// from local evidence: the verified desktop link statement (members, also
// after a backup restore) or the local identity (guests), never from the
// server's self-signed key record.
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, getSealed, putSealed } = await import("../src/core/db.js");
const { DirectMessages, ownAnchor } = await import("../src/core/direct.js");
const { generateIdentity, keyFingerprint, publicKeyPEM, sign, toBase64 } = await import("../src/core/crypto.js");
const { approvalTranscript, signKeyRecord } = await import("../src/core/dm-trust.js");
const { linkBindingTranscript, verifyLinkBinding } = await import("../src/core/link-binding.js");
const { exportBackup, importBackup } = await import("../src/core/backup.js");

const BINDING = "tls-sha256:" + "ab".repeat(32);
let n = 0;

/** A device entry; approver = [id, identity] signs its approval. */
function device(id, key, { first = false, approver = null, principal } = {}) {
    const d = { device_id: id, kind: first ? "desktop" : "web", ed25519_public_key: publicKeyPEM(key.edPublic), x25519_public_key: toBase64(key.xPublic) };
    if (first) d.first_device = true;
    if (approver) {
        d.approved_by = approver[0];
        d.approval_signature = toBase64(sign(approvalTranscript(principal, approver[0], d.ed25519_public_key, d.x25519_public_key), approver[1].edSecret));
    }
    return d;
}

/** The desktop-signed link statement for the PWA's keys. */
function statement(principal, approverID, approver, pwa) {
    const transcript = linkBindingTranscript(principal, approverID, publicKeyPEM(pwa.edPublic), toBase64(pwa.xPublic), BINDING);
    return { binding: BINDING, signature: toBase64(sign(transcript, approver.edSecret)), approverDeviceID: approverID, approverKey: publicKeyPEM(approver.edPublic), principal };
}

function linkedMeta(principal, approverID, approver, pwa) {
    return { linkBinding: statement(principal, approverID, approver, pwa), bindingAnchor: keyFingerprint(publicKeyPEM(approver.edPublic)) };
}

/** A DirectMessages for the PWA whose server answers every KeyRequest with answer. */
function pwaDM(uid, pwa, meta, answer) {
    const session = Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: pwa, uniqueID: uid, deviceID: "pwa", nickname: "me" });
    const dm = new DirectMessages(session, meta);
    dm.conn.request = async () => ({ unique_id: uid, ...answer });
    return dm;
}

const ids = entry => entry.devices.map(d => d.deviceID).sort();
const fingerprintOf = key => keyFingerprint(publicKeyPEM(key.edPublic));

test("P1-1: a server-chosen own root with its own first device and the PWA approved by it is never trusted or pinned", async () => {
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity(), attacker = generateIdentity(), evil = generateIdentity();
    const dm = pwaDM(uid, pwa, linkedMeta(uid, "deskA", root, pwa), {
        public_key: signKeyRecord(uid, attacker.xPublic, attacker.edSecret),
        devices: [device("evil", attacker, { first: true }), device("evil-2", evil, { approver: ["evil", attacker], principal: uid }),
            device("pwa", pwa, { approver: ["evil", attacker], principal: uid })],
    });
    const own = await dm.devicesOf(uid);
    assert.notEqual(own.trust, "ok");
    assert.equal(own.block?.code, "server_binding_mismatch", "secure messaging is blocked with the re-link explanation");
    assert.deepEqual(own.devices, [], "no own-device fanout target, so no copy for the attacker's device");
    assert.equal(own.signingKey, undefined);
    assert.equal(await getSealed(Stores.pins, uid), undefined, "the attacker root is never pinned");
    assert.equal(await dm.historicalSenderKey(uid, "evil-2", toBase64(evil.xPublic), [device("evil-2", evil)]), null);
});

test("P1-1: listing the real approving desktop as approved by a server-chosen root is refused", async () => {
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity(), attacker = generateIdentity();
    // The attacker root holds its own private key, so it can "approve" the
    // real desktop's public keys: a chain to the anchor proves nothing.
    const dm = pwaDM(uid, pwa, linkedMeta(uid, "deskA", root, pwa), {
        public_key: signKeyRecord(uid, attacker.xPublic, attacker.edSecret),
        devices: [device("evil", attacker, { first: true }), device("deskA", root, { approver: ["evil", attacker], principal: uid }),
            device("pwa", pwa, { approver: ["deskA", root], principal: uid })],
    });
    const own = await dm.devicesOf(uid);
    assert.equal(own.block?.code, "server_binding_mismatch");
    assert.deepEqual(own.devices, []);
    assert.equal(await getSealed(Stores.pins, uid), undefined);
});

test("P1-1: honest first-desktop link: the anchored root is pinned and the PWA is trusted", async () => {
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity(), phone = generateIdentity();
    const dm = pwaDM(uid, pwa, linkedMeta(uid, "deskA", root, pwa), {
        public_key: signKeyRecord(uid, root.xPublic, root.edSecret),
        devices: [device("deskA", root, { first: true }), device("pwa", pwa, { approver: ["deskA", root], principal: uid }),
            device("phone", phone, { approver: ["pwa", pwa], principal: uid })],
    });
    const own = await dm.devicesOf(uid);
    assert.equal(own.trust, "ok");
    assert.equal(own.block, undefined);
    assert.equal(own.fingerprint, fingerprintOf(root));
    assert.deepEqual(ids(own), ["deskA", "phone", "pwa"]);
    assert.equal((await getSealed(Stores.pins, uid)).signingKey, toBase64(root.edPublic));
    assert.equal(await dm.canSend(), true);
});

test("P1-1: honest secondary-desktop link: a second desktop of the account approves with the account key", async () => {
    // A desktop signs in with the account's identity key (the principal is
    // derived from it): a second desktop is its own device record holding
    // that same key, approved by the first one.
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity();
    const devices = [device("deskA", root, { first: true }), device("deskB", root, { approver: ["deskA", root], principal: uid }),
        device("pwa", pwa, { approver: ["deskB", root], principal: uid })];
    const dm = pwaDM(uid, pwa, linkedMeta(uid, "deskB", root, pwa), { public_key: signKeyRecord(uid, root.xPublic, root.edSecret), devices });
    const own = await dm.devicesOf(uid);
    assert.equal(own.trust, "ok");
    assert.equal(own.block, undefined);
    assert.equal(own.fingerprint, fingerprintOf(root));
    assert.deepEqual(ids(own), ["deskA", "deskB", "pwa"]);
    assert.equal(await dm.canSend(), true);

    // An approver with a key other than the root (only possible from a
    // non-desktop client) cannot be chained to the root without trusting the
    // server: blocked with the re-link explanation, never pinned.
    const uid2 = "alice" + ++n, other = generateIdentity();
    const chained = [device("deskA", root, { first: true }), device("deskB", other, { approver: ["deskA", root], principal: uid2 }),
        device("pwa", pwa, { approver: ["deskB", other], principal: uid2 })];
    const blocked = await pwaDM(uid2, pwa, linkedMeta(uid2, "deskB", other, pwa), { public_key: signKeyRecord(uid2, root.xPublic, root.edSecret), devices: chained }).devicesOf(uid2);
    assert.equal(blocked.block?.code, "server_binding_mismatch");
    assert.deepEqual(blocked.devices, []);
    assert.equal(await getSealed(Stores.pins, uid2), undefined);
});

test("P1-1: a stale own pin never outranks the anchored root", async () => {
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity(), attacker = generateIdentity();
    await putSealed(Stores.pins, uid, { signingKey: toBase64(attacker.edPublic), devices: ["evil"], at: 1 });
    const dm = pwaDM(uid, pwa, linkedMeta(uid, "deskA", root, pwa), {
        public_key: signKeyRecord(uid, root.xPublic, root.edSecret),
        devices: [device("deskA", root, { first: true }), device("pwa", pwa, { approver: ["deskA", root], principal: uid })],
    });
    const own = await dm.devicesOf(uid);
    assert.equal(own.trust, "ok");
    assert.deepEqual(ids(own), ["deskA", "pwa"]);
    assert.equal((await getSealed(Stores.pins, uid)).signingKey, toBase64(root.edPublic));
});

test("P1-1: without a verified link statement nothing of the own account is trusted (no server-relayed fallback)", async () => {
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity();
    const answer = { public_key: signKeyRecord(uid, root.xPublic, root.edSecret),
        devices: [device("deskA", root, { first: true }), device("pwa", pwa, { approver: ["deskA", root], principal: uid })] };
    // Typed-code link or an older backup: only the server-relayed fingerprint.
    const legacy = await pwaDM(uid, pwa, { approverFingerprint: fingerprintOf(root) }, answer).devicesOf(uid);
    assert.equal(legacy.trust, "unavailable");
    assert.equal(legacy.block?.code, "server_binding_unavailable");
    // A statement for another device's keys does not verify either.
    const wrong = await pwaDM(uid, pwa, linkedMeta(uid, "deskA", root, generateIdentity()), answer).devicesOf(uid);
    assert.equal(wrong.block?.code, "server_binding_unavailable");
    assert.equal(ownAnchor(linkedMeta("mallory", "deskA", root, pwa), pwa, uid), null, "a statement for another account");
});

test("P3-9: a guest pins its own root from its local identity, never from the server", async () => {
    const uid = "guest:g" + ++n, guest = generateIdentity(), attacker = generateIdentity();
    const forged = await pwaDM(uid, guest, { guest: true }, {
        public_key: signKeyRecord(uid, attacker.xPublic, attacker.edSecret), devices: [device("evil", attacker, { first: true })],
    }).devicesOf(uid);
    assert.equal(forged.fingerprint, fingerprintOf(guest));
    assert.deepEqual(forged.devices, []);
    const uid2 = "guest:g" + ++n;
    const honest = await pwaDM(uid2, guest, { guest: true }, {
        public_key: signKeyRecord(uid2, guest.xPublic, guest.edSecret), devices: [device("g", guest, { first: true })],
    }).devicesOf(uid2);
    assert.equal(honest.trust, "ok");
    assert.deepEqual(ids(honest), ["g"]);
    assert.equal((await getSealed(Stores.pins, uid2)).signingKey, toBase64(guest.edPublic));
});

test("P2-2: a backup carries the link statement; the restored anchor (not the server's) pins the own root and re-enables secure messaging", async () => {
    const uid = "alice" + ++n, root = generateIdentity(), pwa = generateIdentity(), attacker = generateIdentity();
    const meta = { deviceID: "pwa", uniqueID: uid, via: "link", approverFingerprint: fingerprintOf(attacker), ...linkedMeta(uid, "deskA", root, pwa) };
    const text = await exportBackup(pwa, meta, "correct horse", { iterations: 1000, server: "https://example.test" });
    assert.ok(!text.includes("deskA"), "the statement is inside the encrypted part");
    const restored = await importBackup(text, "correct horse");
    assert.deepEqual(restored.meta.linkBinding, meta.linkBinding);
    assert.equal(restored.meta.bindingAnchor, meta.bindingAnchor);
    // openRatchet's exact check succeeds after the restore.
    assert.equal(verifyLinkBinding(restored.meta.linkBinding, { identity: restored.identity, principal: uid, approverFingerprint: restored.meta.bindingAnchor }), BINDING);
    // The server-relayed approver fingerprint (here: the attacker's) is ignored.
    const forged = await pwaDM(uid, restored.identity, restored.meta, {
        public_key: signKeyRecord(uid, attacker.xPublic, attacker.edSecret),
        devices: [device("evil", attacker, { first: true }), device("pwa", pwa, { approver: ["evil", attacker], principal: uid })],
    }).devicesOf(uid);
    assert.equal(forged.block?.code, "server_binding_mismatch");
    const honest = await pwaDM(uid, restored.identity, restored.meta, {
        public_key: signKeyRecord(uid, root.xPublic, root.edSecret),
        devices: [device("deskA", root, { first: true }), device("pwa", pwa, { approver: ["deskA", root], principal: uid })],
    }).devicesOf(uid, { refresh: true });
    assert.equal(honest.trust, "ok");
    assert.deepEqual(ids(honest), ["deskA", "pwa"]);
});

test("P2-2: an older backup without the statement still restores; secure messaging then asks to link again", async () => {
    const pwa = generateIdentity();
    const text = await exportBackup(pwa, { deviceID: "pwa", uniqueID: "alice", approverFingerprint: "aaaa bbbb cccc dddd" }, "correct horse", { iterations: 1000 });
    const restored = await importBackup(text, "correct horse");
    assert.equal(restored.meta.linkBinding, undefined);
    assert.equal(restored.meta.uniqueID, "alice");
    assert.throws(() => verifyLinkBinding(restored.meta.linkBinding, { identity: restored.identity, principal: "alice", approverFingerprint: restored.meta.bindingAnchor || "" }),
        e => e.code === "server_binding_unavailable");
    // A statement that does not verify for the restored keys is dropped, not restored.
    const root = generateIdentity();
    const tampered = await exportBackup(pwa, { uniqueID: "alice", ...linkedMeta("alice", "deskA", root, generateIdentity()) }, "correct horse", { iterations: 1000 });
    const dropped = await importBackup(tampered, "correct horse");
    assert.equal(dropped.meta.linkBinding, undefined);
    assert.equal(dropped.meta.bindingAnchor, undefined);
});
