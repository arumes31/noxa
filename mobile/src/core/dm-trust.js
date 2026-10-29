// dm-trust.js — which devices of an account may receive direct messages.
//
// An account's identity is the Ed25519 key that signs its noxa-dm-key-v1
// record (internal/e2ee/key_record.go); this app pins it on first contact.
// The account's first device must hold that key, and every other device
// must carry an approval signature by an already trusted device over
// DeviceApprovalTranscript (internal/netproto/devices.go).
import nacl from "tweetnacl";
import {
    validCurve,
    validSigning,
    validText,
    verifySignature,
} from "./olm-keys.js";

import { fromBase64, toBase64, utf8 } from "./crypto.js";

const RECORD_PREFIX = "noxa-dm-key-v1:";

/** JSON string encoding as Go's encoding/json writes it (HTML-safe). */
function goJSONString(text) {
    return JSON.stringify(text).replace(
        /[<>&\u2028\u2029]/g,
        (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`,
    );
}

/**
 * Parses and verifies a signed key record for uid. Returns
 * {signingKey, encryptionKey} (raw bytes) or null.
 */
export function parseKeyRecord(encoded, uid) {
    if (
        typeof encoded !== "string" ||
        encoded.length > 2048 ||
        !encoded.startsWith(RECORD_PREFIX)
    )
        return null;
    let record;
    try {
        record = JSON.parse(
            new TextDecoder().decode(
                fromBase64(encoded.slice(RECORD_PREFIX.length)),
            ),
        );
    } catch {
        return null;
    }
    if (
        !record ||
        record.uid !== uid ||
        typeof record.signing_key !== "string" ||
        typeof record.encryption_key !== "string"
    )
        return null;
    let signingKey, encryptionKey, signature;
    try {
        signingKey = fromBase64(record.signing_key);
        encryptionKey = fromBase64(record.encryption_key);
        signature = fromBase64(record.signature || "");
    } catch {
        return null;
    }
    if (
        signingKey.length !== 32 ||
        encryptionKey.length !== 32 ||
        signature.length !== 64
    )
        return null;
    const payload = `{"UID":${goJSONString(record.uid)},"SigningKey":"${toBase64(signingKey)}","EncryptionKey":"${toBase64(encryptionKey)}"}`;
    const message = new Uint8Array([
        ...utf8("noxa/dm-identity/v1\0"),
        ...utf8(payload),
    ]);
    if (!nacl.sign.detached.verify(message, signature, signingKey)) return null;
    return { signingKey, encryptionKey };
}

/**
 * Signs this identity's DM key record (mirror of internal/e2ee SignKeyRecord):
 * a self-rooted identity (a guest) publishes it so contacts and its own
 * device chain can verify its devices. edSecret is the 64-byte Ed25519 secret.
 */
export function signKeyRecord(uid, xPublic, edSecret) {
    const signingKey = edSecret.slice(32, 64);
    const payload = `{"UID":${goJSONString(uid)},"SigningKey":"${toBase64(signingKey)}","EncryptionKey":"${toBase64(xPublic)}"}`;
    const signature = nacl.sign.detached(new Uint8Array([...utf8("noxa/dm-identity/v1\0"), ...utf8(payload)]), edSecret);
    const record = JSON.stringify({ uid, signing_key: toBase64(signingKey), encryption_key: toBase64(xPublic), signature: toBase64(signature) });
    return RECORD_PREFIX + toBase64(utf8(record));
}

/** Raw Ed25519 key of the identity text format (PEM around the raw key). */
export function ed25519FromPEM(pem) {
    const body = String(pem)
        .replace(/-----(BEGIN|END) PUBLIC KEY-----/g, "")
        .replace(/\s+/g, "");
    try {
        const raw = fromBase64(body);
        return raw.length === 32 ? raw : null;
    } catch {
        return null;
    }
}

export function approvalTranscript(
    principal,
    approverID,
    edPublicPEM,
    xPublicB64,
) {
    return utf8(
        `noxa-device-approval-v1\n${principal}\n${approverID}\n${edPublicPEM}\n${xPublicB64}`,
    );
}

function equalBytes(a, b) {
    return a && b && a.length === b.length && a.every((v, i) => v === b[i]);
}

/**
 * Returns the trusted devices of principal's directory listing: the first
 * device if it holds the pinned signing key, then devices approved along a
 * chain of trusted devices. Each result has {deviceID, kind, x25519 (raw)}.
 */
export function trustedDevices(
    principal,
    devices,
    pinnedSigningKey,
    authenticatedEncryptionKey,
) {
    if (
        !validText(principal) ||
        !Array.isArray(devices) ||
        devices.length > 64 ||
        !authenticatedEncryptionKey
    )
        return [];
    const byID = new Map(),
        valid = new Map();
    let first = null;
    for (const d of devices) {
        if (!d || !validText(d.device_id) || byID.has(d.device_id)) return [];
        byID.set(d.device_id, d);
        if (d.first_device) {
            if (first) return [];
            first = d;
        }
        if (
            typeof d.ed25519_public_key !== "string" ||
            d.ed25519_public_key.length > 256 ||
            typeof d.x25519_public_key !== "string" ||
            d.x25519_public_key.length !== 44
        )
            continue;
        const signingKey = ed25519FromPEM(d.ed25519_public_key);
        let x25519;
        try {
            x25519 = fromBase64(d.x25519_public_key);
        } catch {
            continue;
        }
        if (
            d.revoked_at ||
            !signingKey ||
            toBase64(x25519) !== d.x25519_public_key ||
            !validSigning(toBase64(signingKey).replace(/=+$/, "")) ||
            !validCurve(toBase64(x25519).replace(/=+$/, ""))
        )
            continue;
        valid.set(d.device_id, {
            deviceID: d.device_id,
            kind: d.kind,
            signingKey,
            x25519,
            x25519B64: toBase64(x25519),
            first: !!d.first_device,
        });
    }
    const root = first && valid.get(first.device_id);
    if (
        !root ||
        !equalBytes(root.signingKey, pinnedSigningKey) ||
        !equalBytes(root.x25519, authenticatedEncryptionKey)
    )
        return [];
    const trusted = new Set([first.device_id]);
    for (let changed = true; changed; ) {
        changed = false;
        for (const d of devices) {
            if (
                trusted.has(d.device_id) ||
                !trusted.has(d.approved_by) ||
                !valid.has(d.device_id)
            )
                continue;
            let signature;
            try {
                signature = fromBase64(d.approval_signature || "");
            } catch {
                continue;
            }
            if (
                signature.length === 64 &&
                toBase64(signature) === d.approval_signature &&
                verifySignature(
                    signature,
                    approvalTranscript(
                        principal,
                        d.approved_by,
                        d.ed25519_public_key,
                        d.x25519_public_key,
                    ),
                    valid.get(d.approved_by).signingKey,
                )
            ) {
                trusted.add(d.device_id);
                changed = true;
            }
        }
    }
    return [
        root,
        ...devices
            .filter(
                (d) =>
                    d.device_id !== first.device_id && trusted.has(d.device_id),
            )
            .map((d) => valid.get(d.device_id)),
    ];
}
