// dm-trust.js — which devices of an account may receive direct messages.
//
// An account's identity is the Ed25519 key that signs its noxa-dm-key-v1
// record (internal/e2ee/key_record.go); this app pins it on first contact.
// The account's first device must hold that key, and every other device
// must carry an approval signature by an already trusted device over
// DeviceApprovalTranscript (internal/netproto/devices.go).
import nacl from "tweetnacl";

import { fromBase64, toBase64, utf8 } from "./crypto.js";

const RECORD_PREFIX = "noxa-dm-key-v1:";

/** JSON string encoding as Go's encoding/json writes it (HTML-safe). */
function goJSONString(text) {
    return JSON.stringify(text).replace(/[<>&\u2028\u2029]/g, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
}

/**
 * Parses and verifies a signed key record for uid. Returns
 * {signingKey, encryptionKey} (raw bytes) or null.
 */
export function parseKeyRecord(encoded, uid) {
    if (typeof encoded !== "string" || encoded.length > 2048 || !encoded.startsWith(RECORD_PREFIX)) return null;
    let record;
    try {
        record = JSON.parse(new TextDecoder().decode(fromBase64(encoded.slice(RECORD_PREFIX.length))));
    } catch {
        return null;
    }
    if (!record || record.uid !== uid || typeof record.signing_key !== "string" || typeof record.encryption_key !== "string") return null;
    let signingKey, encryptionKey, signature;
    try {
        signingKey = fromBase64(record.signing_key);
        encryptionKey = fromBase64(record.encryption_key);
        signature = fromBase64(record.signature || "");
    } catch {
        return null;
    }
    if (signingKey.length !== 32 || encryptionKey.length !== 32 || signature.length !== 64) return null;
    const payload = `{"UID":${goJSONString(record.uid)},"SigningKey":"${toBase64(signingKey)}","EncryptionKey":"${toBase64(encryptionKey)}"}`;
    const message = new Uint8Array([...utf8("noxa/dm-identity/v1\0"), ...utf8(payload)]);
    if (!nacl.sign.detached.verify(message, signature, signingKey)) return null;
    return { signingKey, encryptionKey };
}

/** Raw Ed25519 key of the identity text format (PEM around the raw key). */
export function ed25519FromPEM(pem) {
    const body = String(pem).replace(/-----(BEGIN|END) PUBLIC KEY-----/g, "").replace(/\s+/g, "");
    try {
        const raw = fromBase64(body);
        return raw.length === 32 ? raw : null;
    } catch {
        return null;
    }
}

export function approvalTranscript(principal, approverID, edPublicPEM, xPublicB64) {
    return utf8(`noxa-device-approval-v1\n${principal}\n${approverID}\n${edPublicPEM}\n${xPublicB64}`);
}

function equalBytes(a, b) {
    return a && b && a.length === b.length && a.every((v, i) => v === b[i]);
}

/**
 * Returns the trusted devices of principal's directory listing: the first
 * device if it holds the pinned signing key, then devices approved along a
 * chain of trusted devices. Each result has {deviceID, kind, x25519 (raw)}.
 */
export function trustedDevices(principal, devices, pinnedSigningKey) {
    const byID = new Map((devices || []).map(d => [d.device_id, d]));
    const trusted = new Map();
    const first = (devices || []).find(d => d.first_device);
    if (!first || !pinnedSigningKey) return [];
    const firstKey = ed25519FromPEM(first.ed25519_public_key);
    if (!equalBytes(firstKey, pinnedSigningKey)) return [];
    trusted.set(first.device_id, first);
    // Approvals form a tree rooted at the first device; repeat until no
    // device is added, which also ends cycles.
    for (let changed = true; changed;) {
        changed = false;
        for (const device of byID.values()) {
            if (trusted.has(device.device_id) || !device.approved_by || !trusted.has(device.approved_by)) continue;
            const approver = ed25519FromPEM(trusted.get(device.approved_by).ed25519_public_key);
            let signature;
            try { signature = fromBase64(device.approval_signature || ""); } catch { continue; }
            if (!approver || signature.length !== 64) continue;
            const transcript = approvalTranscript(principal, device.approved_by, device.ed25519_public_key, device.x25519_public_key);
            if (nacl.sign.detached.verify(transcript, signature, approver)) {
                trusted.set(device.device_id, device);
                changed = true;
            }
        }
    }
    return [...trusted.values()].flatMap(d => {
        let x25519;
        try { x25519 = fromBase64(d.x25519_public_key || ""); } catch { return []; }
        return x25519.length === 32 ? [{ deviceID: d.device_id, kind: d.kind, x25519, x25519B64: toBase64(x25519), first: !!d.first_device }] : [];
    });
}
