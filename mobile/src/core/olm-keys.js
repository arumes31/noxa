import { ed25519, x25519 } from "@noble/curves/ed25519.js";
import { fromBase64, toBase64 } from "./crypto.js";
export const equalBytes = (a, b) =>
    !!a && !!b && a.length === b.length && a.every((v, i) => v === b[i]);
export function rawBase64(text, length) {
    if (
        typeof text !== "string" ||
        text.length !== Math.ceil((length * 8) / 6) ||
        !/^[A-Za-z0-9+/]+$/.test(text)
    )
        throw new Error("noncanonical key");
    const bytes = fromBase64(text);
    if (bytes.length !== length || toBase64(bytes).replace(/=+$/, "") !== text)
        throw new Error("noncanonical key");
    return bytes;
}
export function validCurve(text) {
    try {
        const bytes = rawBase64(text, 32);
        let n = 0n;
        for (let i = 31; i >= 0; i--) n = (n << 8n) | BigInt(bytes[i]);
        if (n >= (1n << 255n) - 19n) return false;
        const secret = new Uint8Array(32);
        secret[0] = 42;
        return x25519.getSharedSecret(secret, bytes).some((v) => v !== 0);
    } catch {
        return false;
    }
}
export function validSigning(text) {
    try {
        const bytes = rawBase64(text, 32);
        const p = ed25519.Point.fromBytes(bytes, false);
        return equalBytes(p.toBytes(), bytes) && !p.isSmallOrder();
    } catch {
        return false;
    }
}
export function validText(text) {
    return (
        typeof text === "string" &&
        text.length <= 128 &&
        new TextEncoder().encode(text).length > 0 &&
        new TextEncoder().encode(text).length <= 128 &&
        !/[\u0000-\u001f\u007f-\u009f\ud800-\udfff]/u.test(text)
    );
}
export function verifySignature(signature, transcript, key) {
    try {
        return ed25519.verify(signature, transcript, key, { zip215: false });
    } catch {
        return false;
    }
}
