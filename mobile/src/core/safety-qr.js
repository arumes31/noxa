// safety-qr.js — safety-number QR payloads (205), shared byte for byte by the
// desktop frontend and the mobile web app (mobile/src/core/safety-qr.js; a
// unit test keeps the copies identical). A payload names who shows it, whom
// it is for and a digest of the material both sides compare; it never holds
// a secret.
//
//   noxa-sn:v1:<scheme>:<shown by>:<shown to>:<64 hex digest>
//
// Schemes: "o" is the desktop's pairwise safety number (SHA-256 over both
// X25519 identity keys, the same digest the twelve digit groups come from);
// "f" pairs the account fingerprints the mobile app shows. The two verify
// different keys, so a code of the other scheme is reported, never compared.

export const SAFETY_QR_PREFIX = "noxa-sn:v1";
export const SafetyScheme = Object.freeze({ pairwise: "o", fingerprint: "f" });
export const SafetyScan = Object.freeze({
    match: "match", mismatch: "mismatch", wrongPair: "wrongPair", otherScheme: "otherScheme", invalid: "invalid",
});

const SCHEMES = new Set(Object.values(SafetyScheme));
const UNIQUE_ID = /^[A-Za-z0-9+/=_-]{1,128}$/;
const DIGEST = /^[0-9a-f]{64}$/;

/** The QR text for a pair; throws on malformed input. */
export function buildSafetyPayload({ scheme, me, peer, digest }) {
    const hex = String(digest || "").toLowerCase();
    if (!SCHEMES.has(scheme) || !UNIQUE_ID.test(me || "") || !UNIQUE_ID.test(peer || "") || !DIGEST.test(hex)) {
        throw new TypeError("invalid safety QR payload");
    }
    return `${SAFETY_QR_PREFIX}:${scheme}:${me}:${peer}:${hex}`;
}

/** { scheme, from, to, digest } for a scanned text, or null. */
export function parseSafetyPayload(text) {
    if (typeof text !== "string" || text.length > 400) return null;
    const parts = text.trim().split(":");
    if (parts.length !== 6 || `${parts[0]}:${parts[1]}` !== SAFETY_QR_PREFIX) return null;
    const [, , scheme, from, to, digest] = parts;
    if (!SCHEMES.has(scheme) || !UNIQUE_ID.test(from) || !UNIQUE_ID.test(to) || !DIGEST.test(digest)) return null;
    return { scheme, from, to, digest };
}

// Compares without stopping at the first difference.
function sameDigest(a, b) {
    if (a.length !== b.length) return false;
    let diff = 0;
    for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
    return diff === 0;
}

/**
 * Checks a scanned text against this pair. The code must be shown by the
 * peer, for me, in the same scheme; only then do the digests decide.
 */
export function checkSafetyScan(text, { scheme, me, peer, digest }) {
    const scanned = parseSafetyPayload(text);
    if (!scanned) return SafetyScan.invalid;
    if (scanned.scheme !== scheme) return SafetyScan.otherScheme;
    if (scanned.from !== peer || scanned.to !== me) return SafetyScan.wrongPair;
    return sameDigest(scanned.digest, String(digest || "").toLowerCase()) ? SafetyScan.match : SafetyScan.mismatch;
}

function normalizedFingerprint(value) {
    return String(value || "").replace(/[^0-9a-f]/gi, "").toLowerCase();
}

/**
 * Digest for the fingerprint scheme: both accounts with the fingerprint this
 * device shows for them, in a fixed order, so both sides compute the same
 * value when they see the same keys.
 */
export async function fingerprintPairDigest(a, b, subtle = globalThis.crypto?.subtle) {
    const entries = [a, b].map(({ uniqueID, fingerprint }) => `${uniqueID}=${normalizedFingerprint(fingerprint)}`).sort();
    if (entries.some(entry => entry.endsWith("="))) throw new TypeError("fingerprint unavailable");
    const data = new TextEncoder().encode(`noxa-safety-fp:v1|${entries.join("|")}`);
    const hash = new Uint8Array(await subtle.digest("SHA-256", data));
    return Array.from(hash, byte => byte.toString(16).padStart(2, "0")).join("");
}
