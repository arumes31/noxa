// crypto.js — the noXa wire cryptography, byte-compatible with the Go code:
//   identity keys   Ed25519, PEM "PUBLIC KEY" around the raw 32 bytes
//   channel bodies  base64(nonce24 || secretbox(text, scopeKey))
//   direct messages base64(nonce24 || box(text, recipientPub, senderSecret))
//   scope keys      box.SealAnonymous(key, x25519Public)
import nacl from "tweetnacl";
import { blake2b } from "@noble/hashes/blake2.js";
import { hmac } from "@noble/hashes/hmac.js";
import { sha256 } from "@noble/hashes/sha2.js";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function toBase64(bytes) {
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary);
}

export function fromBase64(text) {
    const binary = atob(text);
    const out = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
    return out;
}

export function toBase64URL(bytes) {
    return toBase64(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function fromBase64URL(text) {
    const padded = text.replace(/-/g, "+").replace(/_/g, "/");
    return fromBase64(padded + "=".repeat((4 - (padded.length % 4)) % 4));
}

function hex(bytes) {
    return Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
}

/** PEM text of a raw Ed25519 public key, exactly as Go's pem.EncodeToMemory writes it. */
export function publicKeyPEM(raw) {
    return `-----BEGIN PUBLIC KEY-----\n${toBase64(raw)}\n-----END PUBLIC KEY-----\n`;
}

/** The key-derived unique ID: base64(base64(sha256(raw public key))). */
export function uniqueIDFromPublicKey(raw) {
    return toBase64(encoder.encode(toBase64(sha256(raw))));
}

/** Comparison fingerprint: first 16 hex of SHA-256 over the trimmed PEM, grouped by 4. */
export function keyFingerprint(pem) {
    const h = hex(sha256(encoder.encode(pem.trim()))).slice(0, 16);
    return `${h.slice(0, 4)} ${h.slice(4, 8)} ${h.slice(8, 12)} ${h.slice(12, 16)}`;
}

export function generateIdentity() {
    const signing = nacl.sign.keyPair();
    const encryption = nacl.box.keyPair();
    return {
        edPublic: signing.publicKey,
        edSecret: signing.secretKey,
        xPublic: encryption.publicKey,
        xSecret: encryption.secretKey,
    };
}

export function sign(message, edSecret) {
    return nacl.sign.detached(message, edSecret);
}

/** Opens box.SealAnonymous: epk || box(m, nonce=blake2b-24(epk || pk), epk, sk). */
export function openSealed(sealed, xPublic, xSecret) {
    if (sealed.length < 32 + nacl.box.overheadLength) return null;
    const ephemeral = sealed.subarray(0, 32);
    const nonceInput = new Uint8Array(64);
    nonceInput.set(ephemeral);
    nonceInput.set(xPublic, 32);
    const nonce = blake2b(nonceInput, { dkLen: 24 });
    return nacl.box.open(sealed.subarray(32), nonce, ephemeral, xSecret);
}

export function sealScope(text, key) {
    const nonce = nacl.randomBytes(24);
    const sealed = nacl.secretbox(encoder.encode(text), nonce, key);
    const blob = new Uint8Array(24 + sealed.length);
    blob.set(nonce);
    blob.set(sealed, 24);
    return toBase64(blob);
}

export function openScope(blobB64, key) {
    const raw = fromBase64(blobB64);
    if (raw.length < 24) return null;
    const plain = nacl.secretbox.open(raw.subarray(24), raw.subarray(0, 24), key);
    return plain ? decoder.decode(plain) : null;
}

export function sealDM(text, recipientPublic, senderSecret) {
    const nonce = nacl.randomBytes(24);
    const sealed = nacl.box(encoder.encode(text), nonce, recipientPublic, senderSecret);
    const blob = new Uint8Array(24 + sealed.length);
    blob.set(nonce);
    blob.set(sealed, 24);
    return toBase64(blob);
}

export function openDM(blobB64, senderPublic, recipientSecret) {
    const raw = fromBase64(blobB64);
    if (raw.length < 24) return null;
    const plain = nacl.box.open(raw.subarray(24), raw.subarray(0, 24), senderPublic, recipientSecret);
    return plain ? decoder.decode(plain) : null;
}

// Device linking (internal/netproto/devices.go).

export function linkClaimTranscript(linkID, edPublicPEM, xPublicB64) {
    return encoder.encode(`noxa-device-link-claim-v1\n${linkID}\n${edPublicPEM}\n${xPublicB64}`);
}

export function linkProof(linkKey, transcript) {
    return hmac(sha256, linkKey, transcript);
}

export function linkSAS(linkKey, transcript) {
    const prefix = encoder.encode("noxa-device-link-sas-v1\n");
    const input = new Uint8Array(prefix.length + transcript.length);
    input.set(prefix);
    input.set(transcript, prefix.length);
    const sum = hmac(sha256, linkKey, input);
    const n = ((sum[0] << 8) | sum[1]) % 100;
    return String(n).padStart(2, "0");
}

/** Hidden codes are typed with or without the dash, in any case. */
export function normalizeLinkCode(code) {
    return code.toUpperCase().replace(/[\s-]/g, "");
}

export function utf8(text) {
    return encoder.encode(text);
}
