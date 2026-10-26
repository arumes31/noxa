import { fromBase64, toBase64, utf8, sign } from "./crypto.js";
import {
    rawBase64,
    validCurve,
    validSigning,
    validText,
    verifySignature,
} from "./olm-keys.js";
import { trustedDevices } from "./dm-trust.js";
export const OLM_DIRECTORY_VERSION = "noxa-olm-directory-v1";
export function serverBinding(verifiedFingerprint) {
    if (
        typeof verifiedFingerprint === "string" &&
        /^(?:[0-9a-fA-F]{2}:){31}[0-9a-fA-F]{2}$/.test(verifiedFingerprint)
    )
        verifiedFingerprint = verifiedFingerprint.replaceAll(":", "");
    if (
        typeof verifiedFingerprint !== "string" ||
        !/^[0-9a-fA-F]{64}$/.test(verifiedFingerprint)
    )
        throw new Error("authenticated server fingerprint required");
    return `tls-sha256:${verifiedFingerprint.toLowerCase()}`;
}
export function keyNumber(id) {
    const bytes = rawBase64(id, 8);
    let n = 0n;
    for (const b of bytes) n = (n << 8n) | BigInt(b);
    if (n > 9223372036854775807n) throw new Error("key ID too large");
    return n;
}
export function bundleTranscript(b) {
    if (
        !b ||
        b.version !== OLM_DIRECTORY_VERSION ||
        b.protocol !== "noxa-olm-v1" ||
        !/^tls-sha256:[0-9a-f]{64}$/.test(b.server) ||
        !validText(b.principal) ||
        !validText(b.device_id) ||
        !Number.isSafeInteger(b.account_epoch) || b.account_epoch < 1 ||
        !Number.isSafeInteger(b.generation) ||
        b.generation < 1 ||
        !Number.isSafeInteger(b.issued_at) ||
        b.issued_at < 1 ||
        !Number.isSafeInteger(b.expires_at) ||
        b.expires_at <= b.issued_at ||
        b.expires_at - b.issued_at > 7 * 86400 ||
        !validCurve(b.identity) ||
        !validSigning(b.signing) ||
        !Array.isArray(b.one_time_keys) ||
        b.one_time_keys.length < 1 ||
        b.one_time_keys.length > 100
    )
        throw new Error("invalid Olm bundle");
    const fields = [
        b.version,
        b.protocol,
        b.server,
        b.principal,
        b.device_id,
        String(b.account_epoch),
        String(b.generation),
        String(b.issued_at),
        String(b.expires_at),
        b.identity,
        b.signing,
        String(b.one_time_keys.length),
    ];
    const ids = new Set(),
        keys = new Set();
    for (const k of b.one_time_keys) {
        keyNumber(k.id);
        if (
            ids.has(k.id) ||
            keys.has(k.key) ||
            k.key === b.identity ||
            !validCurve(k.key)
        )
            throw new Error("invalid one-time key");
        ids.add(k.id);
        keys.add(k.key);
        fields.push(k.id, k.key);
    }
    return utf8(fields.map((f) => `${utf8(f).length}:${f}`).join(""));
}
export function signBundle(bundle, noxaSecret) {
    // Sign with the EXISTING noXa device identity, never the Olm signing key.
    return {
        ...bundle,
        signature: toBase64(sign(bundleTranscript(bundle), noxaSecret)),
    };
}
export function verifyOlmClaim(
    bundle,
    {
        server,
        principal,
        deviceID,
        pinnedOlmIdentity = "",
        accountEpoch = 0,
        claimUntil = 0,
        keyID,
        rootSigning,
        rootEncryption,
        devices,
        now = Math.floor(Date.now() / 1000),
    },
) {
    if (!Number.isSafeInteger(now) || now < 1)
        throw new Error("invalid verification time");
    const device = trustedDevices(
        principal,
        devices,
        rootSigning,
        rootEncryption,
    ).find((d) => d.deviceID === deviceID);
    if (!device) throw new Error("untrusted device directory");
    const transcript = bundleTranscript(bundle);
    let signature;
    try {
        signature = fromBase64(bundle.signature);
    } catch {
        throw new Error("invalid signature");
    }
    if (
        signature.length !== 64 ||
        toBase64(signature) !== bundle.signature ||
        bundle.server !== server ||
        bundle.principal !== principal ||
        bundle.device_id !== deviceID ||
        bundle.issued_at > now + 300 ||
        (claimUntil ? (!Number.isSafeInteger(claimUntil)||claimUntil<=now||claimUntil>bundle.expires_at+3600) : bundle.expires_at<=now) ||
        (accountEpoch && bundle.account_epoch!==accountEpoch) ||
        !verifySignature(signature, transcript, device.signingKey)
    )
        throw new Error("invalid Olm bundle");
    if (pinnedOlmIdentity && pinnedOlmIdentity !== bundle.identity)
        throw new Error(
            "Olm account identity changed; explicit approval required",
        );
    const key = bundle.one_time_keys.find((k) => k.id === keyID);
    if (!key) throw new Error("unlisted one-time key");
    return key;
}

// Only for already admitted delivery evidence. The key must come from the
// independently verified noXa device chain; this never approves a new pin.
export function verifyBundleEvidence(bundle,{trustedSigningKey,server,principal,deviceID,accountEpoch,pinnedOlmIdentity}){
 const signature=fromBase64(bundle.signature);
 if(bundle.server!==server||bundle.principal!==principal||bundle.device_id!==deviceID||bundle.account_epoch!==accountEpoch||bundle.identity!==pinnedOlmIdentity||signature.length!==64||toBase64(signature)!==bundle.signature||!verifySignature(signature,bundleTranscript(bundle),trustedSigningKey))throw new Error("invalid admitted bundle evidence");
 return bundle;
}
