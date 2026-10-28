// 1c-iii-a item 1: the PWA's server binding comes only from the desktop-signed
// device-link statement (DeviceLinkBindingTranscript), relayed by the server.
import test from "node:test";
import assert from "node:assert/strict";
import { fromBase64, generateIdentity, keyFingerprint, publicKeyPEM, sign, toBase64 } from "../src/core/crypto.js";
import { linkBindingFromStatus, linkBindingTranscript, verifyLinkBinding } from "../src/core/link-binding.js";
import { claimLink, parseLinkFragment, waitForDecision } from "../src/core/device.js";

// Produced by Go (internal/netproto/device_link_binding_test.go vector).
const GO = {
    approverKey: "-----BEGIN PUBLIC KEY-----\nebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ=\n-----END PUBLIC KEY-----\n",
    deviceRaw: "C7w0aldmfDgBIL2cf9flHSxf3+o3zS9b9AWyxr9vLXg=",
    signature: "Kak/fnXht9tXtYMk67oi+hFrNyhG2QLcfsvCO26opePdVM1yBjor8LcXwXBXMDVp88OziP/ijwmCpi8dYzghAw==",
    binding: "tls-sha256:" + "ab".repeat(32),
};
const goDevice = { edPublic: fromBase64(GO.deviceRaw), xPublic: new Uint8Array(32) };
const goStatement = { binding: GO.binding, signature: GO.signature, approverDeviceID: "desk", approverKey: GO.approverKey, principal: "alice" };

test("a Go-signed device-link statement verifies in the PWA (interop)", () => {
    assert.equal(verifyLinkBinding(goStatement, { identity: goDevice, principal: "alice", approverFingerprint: keyFingerprint(GO.approverKey) }), GO.binding);
    // The relayed link_status shape (Go []byte -> base64 string).
    const relayed = linkBindingFromStatus({ state: "approved", server_binding: GO.binding, binding_signature: GO.signature, approver_device_id: "desk", approver_key: GO.approverKey, principal: "alice" });
    assert.deepEqual(relayed, goStatement);
    assert.equal(linkBindingFromStatus({ state: "approved" }), null, "an older desktop sends no statement");
});

test("every mismatch blocks: other keys, account, approver, binding, signature", () => {
    const mismatch = e => e.code === "server_binding_mismatch";
    assert.throws(() => verifyLinkBinding(goStatement, { identity: generateIdentity() }), mismatch);
    assert.throws(() => verifyLinkBinding(goStatement, { identity: goDevice, principal: "bob" }), mismatch);
    assert.throws(() => verifyLinkBinding(goStatement, { identity: goDevice, approverFingerprint: "0000 0000 0000 0000" }), mismatch);
    assert.throws(() => verifyLinkBinding({ ...goStatement, binding: "tls-sha256:" + "cd".repeat(32) }, { identity: goDevice }), mismatch);
    assert.throws(() => verifyLinkBinding({ ...goStatement, binding: "tls-sha256:" + "AB".repeat(32) }, { identity: goDevice }), mismatch);
    assert.throws(() => verifyLinkBinding({ ...goStatement, approverDeviceID: "other" }, { identity: goDevice }), mismatch);
    assert.throws(() => verifyLinkBinding({ ...goStatement, signature: "!!" }, { identity: goDevice }), mismatch);
    const forger = generateIdentity();
    const forged = toBase64(sign(linkBindingTranscript("alice", "desk", publicKeyPEM(goDevice.edPublic), toBase64(goDevice.xPublic), GO.binding), forger.edSecret));
    assert.throws(() => verifyLinkBinding({ ...goStatement, signature: forged }, { identity: goDevice }), mismatch, "signed by a key other than the stated approver");
    assert.throws(() => verifyLinkBinding(null, { identity: goDevice }), e => e.code === "server_binding_unavailable");
});

test("the QR fragment carries the desktop's own fingerprint", () => {
    const k = "A".repeat(43);
    assert.deepEqual(parseLinkFragment(`#id=L&k=${k}&a=abcd+ef01+2345+6789`), { linkID: "L", secret: new Uint8Array(32), approver: "abcd ef01 2345 6789" });
    assert.deepEqual(parseLinkFragment(`#id=L&k=${k}`), { linkID: "L", secret: new Uint8Array(32) });
});

test("a claim answered for another approver than the scanned QR names is refused", async () => {
    const conn = { request: async () => ({ action: "link_claim", state: "claimed", link_id: "L", approver_fingerprint: "1111 1111 1111 1111" }) };
    const link = { linkID: "L", secret: new Uint8Array(32), approver: "2222 2222 2222 2222" };
    await assert.rejects(claimLink(conn, generateIdentity(), link), e => e.code === "approver_mismatch");
    const ok = await claimLink(conn, generateIdentity(), { ...link, approver: "1111 1111 1111 1111" });
    assert.equal(ok.approverFingerprint, "1111 1111 1111 1111");
});

test("the approved link_status result (with the statement) reaches the caller", async () => {
    let decided = null;
    const approved = { action: "link_status", state: "approved", server_binding: GO.binding, binding_signature: GO.signature, approver_device_id: "desk", approver_key: GO.approverKey, principal: "alice" };
    const conn = { open: true, request: async () => approved, close() {} };
    const state = await waitForDecision(generateIdentity(), "L", { open: async () => conn, interval: 1, onDecided: r => { decided = r; } });
    assert.equal(state, "approved");
    assert.deepEqual(linkBindingFromStatus(decided), goStatement);
});
