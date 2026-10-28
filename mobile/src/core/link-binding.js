// link-binding.js — the PWA's noxa-olm-v1 server binding.
//
// A browser cannot see the TLS certificate, so it never derives the binding
// itself and never takes it from the server: the approving desktop (which pins
// the certificate) signs DeviceLinkBindingTranscript (internal/netproto) for
// exactly this device's keys, the server relays it on the approved
// link_status, and this module verifies it at link time and again whenever a
// secure session opens. Any mismatch blocks secure messaging.
import { fromBase64, keyFingerprint, publicKeyPEM, toBase64, utf8 } from "./crypto.js";
import { ed25519FromPEM } from "./dm-trust.js";
import { verifySignature } from "./olm-keys.js";

const BINDING = /^tls-sha256:[0-9a-f]{64}$/;

/** DeviceLinkBindingTranscript (internal/netproto/devices.go). */
export function linkBindingTranscript(principal, approverDeviceID, edPublicPEM, xPublicB64, serverBinding) {
    return utf8(`noxa-device-link-binding-v1\n${principal}\n${approverDeviceID}\n${edPublicPEM}\n${xPublicB64}\n${serverBinding}`);
}

function refuse(code, message) {
    return Object.assign(new Error(message), { code });
}

/** The statement fields of an approved link_status result (or null). */
export function linkBindingFromStatus(result) {
    if (!result?.server_binding) return null;
    return {
        binding: result.server_binding,
        signature: typeof result.binding_signature === "string" ? result.binding_signature : toBase64(Uint8Array.from(result.binding_signature || [])),
        approverDeviceID: result.approver_device_id || "",
        approverKey: result.approver_key || "",
        principal: result.principal || "",
    };
}

/**
 * Verifies a stored or freshly relayed statement against this device's own
 * keys and returns the binding. principal (the signed-in account) and
 * approverFingerprint (shown while linking / named by the scanned QR) are
 * checked when given. Throws {code: "server_binding_unavailable"} without a
 * statement and {code: "server_binding_mismatch"} for anything that does not
 * verify.
 */
export function verifyLinkBinding(statement, { identity, principal = "", approverFingerprint = "" } = {}) {
    if (!statement?.binding) throw refuse("server_binding_unavailable", "secure messaging needs this device to be linked from a desktop again (no confirmed server identity)");
    const mismatch = () => refuse("server_binding_mismatch", "the server identity confirmed by your desktop does not match; secure messaging is blocked until this device is linked again");
    const s = statement;
    if (!BINDING.test(s.binding) || !s.approverDeviceID || !s.principal || typeof s.approverKey !== "string") throw mismatch();
    const approver = ed25519FromPEM(s.approverKey);
    if (!approver) throw mismatch();
    if (principal && s.principal !== principal) throw mismatch();
    if (approverFingerprint && keyFingerprint(s.approverKey) !== approverFingerprint) throw mismatch();
    let signature;
    try { signature = fromBase64(s.signature); } catch { throw mismatch(); }
    const transcript = linkBindingTranscript(s.principal, s.approverDeviceID, publicKeyPEM(identity.edPublic), toBase64(identity.xPublic), s.binding);
    if (signature.length !== 64 || !verifySignature(signature, transcript, approver)) throw mismatch();
    return s.binding;
}
