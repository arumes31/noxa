// Adopted review repros (1c-iii-a review P2-1, P3-4): a malicious server can
// no longer override the QR approver anchor, and an anchor-less statement
// (typed code, empty fingerprint) is never trusted.
import test from "node:test";
import assert from "node:assert/strict";
import { generateIdentity, keyFingerprint, publicKeyPEM, sign, toBase64 } from "../src/core/crypto.js";
import { linkBindingTranscript, verifyLinkBinding } from "../src/core/link-binding.js";
import { parseLinkFragment } from "../src/core/device.js";

test("a server-injected a= before the desktop's is refused (P2-1)", () => {
    const desktop = generateIdentity(), evil = generateIdentity();
    const realFP = keyFingerprint(publicKeyPEM(desktop.edPublic)), evilFP = keyFingerprint(publicKeyPEM(evil.edPublic));
    const url = `https://srv/link#id=L&k=${"A".repeat(43)}&a=${encodeURIComponent(evilFP)}` + "&a=" + encodeURIComponent(realFP);
    assert.equal(parseLinkFragment(url.slice(url.indexOf("#"))), null);
});

test("typed-code / empty-fingerprint statements signed by a server key are refused (P3-4)", () => {
    const evil = generateIdentity(), device = generateIdentity();
    const evilPEM = publicKeyPEM(evil.edPublic), b = "tls-sha256:" + "ee".repeat(32);
    const sig = toBase64(sign(linkBindingTranscript("alice", "x", publicKeyPEM(device.edPublic), toBase64(device.xPublic), b), evil.edSecret));
    const st = { binding: b, signature: sig, approverDeviceID: "x", approverKey: evilPEM, principal: "alice" };
    assert.throws(() => verifyLinkBinding(st, { identity: device, principal: "alice", approverFingerprint: "" }), e => e.code === "server_binding_mismatch");
    assert.throws(() => verifyLinkBinding(st, { identity: device, principal: "alice" }), e => e.code === "server_binding_mismatch");
});
