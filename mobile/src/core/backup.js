// backup.js — a passphrase-protected copy of this device's keys, so the same
// device can sign in again after the browser's storage was cleared. The
// desktop exports its identity as plain JSON (client/identity.go), so the web
// app defines its own container, built only from WebCrypto:
//
//   {
//     "v": 1, "type": "noxa-device-backup",
//     "kdf": "PBKDF2-SHA256", "iter": 600000,
//     "salt": base64(16 bytes), "iv": base64(12 bytes),
//     "data": base64(AES-256-GCM(key, iv, plaintext))
//   }
//
// key = PBKDF2-HMAC-SHA256(UTF-8 passphrase, salt, iter, 32 bytes). The
// plaintext is JSON: {server, server_name, device_id, unique_id, nickname,
// approver_fingerprint, via, ed25519_public, ed25519_secret, x25519_public,
// x25519_secret, link_binding, binding_anchor} with the keys in standard
// base64. link_binding is the desktop-signed server-binding statement and
// binding_anchor the approver fingerprint from the scanned QR: both are
// re-verified offline against the restored keys, so a restored device keeps
// secure messaging (and its own account root) without trusting the server;
// an older backup without them restores, and secure messaging then asks to
// link the device again. The server only signs a
// device in while its device record exists; a backup cannot bring back a
// device that was signed out.
import { fromBase64, toBase64 } from "./crypto.js";
import { verifyLinkBinding } from "./link-binding.js";

export const BACKUP_TYPE = "noxa-device-backup";
export const BACKUP_ITERATIONS = 600000;
const MIN_ITERATIONS = 1000;
const MAX_ITERATIONS = 10000000;
const SIZES = { edPublic: 32, edSecret: 64, xPublic: 32, xSecret: 32 };

const encoder = new TextEncoder();
const decoder = new TextDecoder();

function subtle() {
    const api = globalThis.crypto?.subtle;
    if (!api) throw new Error("this browser cannot encrypt backups");
    return api;
}

async function deriveKey(passphrase, salt, iterations) {
    const material = await subtle().importKey("raw", encoder.encode(passphrase), "PBKDF2", false, ["deriveKey"]);
    return subtle().deriveKey({ name: "PBKDF2", hash: "SHA-256", salt, iterations }, material, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
}

/** Encrypts the identity and its link metadata; returns the container's JSON text. */
export async function exportBackup(identity, meta, passphrase, { iterations = BACKUP_ITERATIONS, server = globalThis.location?.origin || "", serverName = "" } = {}) {
    if (typeof passphrase !== "string" || passphrase.length < 8) throw new Error("passphrase too short");
    const salt = globalThis.crypto.getRandomValues(new Uint8Array(16));
    const iv = globalThis.crypto.getRandomValues(new Uint8Array(12));
    const plain = encoder.encode(JSON.stringify({
        server, server_name: serverName, device_id: meta.deviceID || "", unique_id: meta.uniqueID || "", nickname: meta.nickname || "",
        approver_fingerprint: meta.approverFingerprint || "", via: meta.via || "",
        ...(meta.linkBinding && meta.bindingAnchor ? { link_binding: meta.linkBinding, binding_anchor: meta.bindingAnchor } : {}),
        ed25519_public: toBase64(identity.edPublic), ed25519_secret: toBase64(identity.edSecret),
        x25519_public: toBase64(identity.xPublic), x25519_secret: toBase64(identity.xSecret),
    }));
    const key = await deriveKey(passphrase, salt, iterations);
    const data = new Uint8Array(await subtle().encrypt({ name: "AES-GCM", iv }, key, plain));
    plain.fill(0);
    return JSON.stringify({ v: 1, type: BACKUP_TYPE, kdf: "PBKDF2-SHA256", iter: iterations, salt: toBase64(salt), iv: toBase64(iv), data: toBase64(data) }, null, 2);
}

/** Thrown for a wrong passphrase or a damaged file. */
export class BackupError extends Error {
    constructor(code) {
        super(code);
        this.code = code; // format | passphrase
    }
}

/** Decrypts a container. Resolves with {identity, meta, server, serverName}. */
export async function importBackup(text, passphrase) {
    let container;
    try { container = JSON.parse(text); } catch { throw new BackupError("format"); }
    if (!container || container.v !== 1 || container.type !== BACKUP_TYPE || container.kdf !== "PBKDF2-SHA256" ||
        !Number.isInteger(container.iter) || container.iter < MIN_ITERATIONS || container.iter > MAX_ITERATIONS) {
        throw new BackupError("format");
    }
    let salt, iv, data;
    try {
        salt = fromBase64(container.salt);
        iv = fromBase64(container.iv);
        data = fromBase64(container.data);
    } catch {
        throw new BackupError("format");
    }
    if (salt.length < 16 || iv.length !== 12 || data.length < 16) throw new BackupError("format");
    const key = await deriveKey(String(passphrase || ""), salt, container.iter);
    let plain;
    try {
        plain = new Uint8Array(await subtle().decrypt({ name: "AES-GCM", iv }, key, data));
    } catch {
        throw new BackupError("passphrase");
    }
    let body;
    try { body = JSON.parse(decoder.decode(plain)); } catch { throw new BackupError("format"); } finally { plain.fill(0); }
    const identity = {};
    for (const [field, name] of [["edPublic", "ed25519_public"], ["edSecret", "ed25519_secret"], ["xPublic", "x25519_public"], ["xSecret", "x25519_secret"]]) {
        try { identity[field] = fromBase64(body[name] || ""); } catch { throw new BackupError("format"); }
        if (identity[field].length !== SIZES[field]) throw new BackupError("format");
    }
    // The Ed25519 secret key carries its public half; they must agree.
    if (!identity.edSecret.subarray(32).every((b, i) => b === identity.edPublic[i])) throw new BackupError("format");
    const meta = { via: body.via || "backup", restored: true };
    if (body.device_id) meta.deviceID = String(body.device_id);
    if (body.unique_id) meta.uniqueID = String(body.unique_id);
    if (body.nickname) meta.nickname = String(body.nickname);
    if (body.approver_fingerprint) meta.approverFingerprint = String(body.approver_fingerprint);
    const statement = linkStatement(body.link_binding);
    if (statement && typeof body.binding_anchor === "string" && body.binding_anchor) {
        // Only a statement that still verifies for exactly these keys (and
        // its own anchor) is restored; anything else restores without it.
        try {
            verifyLinkBinding(statement, { identity, principal: meta.uniqueID || "", approverFingerprint: body.binding_anchor });
            meta.linkBinding = statement;
            meta.bindingAnchor = body.binding_anchor;
        } catch { /* restored without secure messaging: link again */ }
    }
    return { identity, meta, server: String(body.server || ""), serverName: String(body.server_name || "") };
}

/** The statement fields of a backed-up link binding, or null. */
function linkStatement(value) {
    if (!value || typeof value !== "object") return null;
    const fields = ["binding", "signature", "approverDeviceID", "approverKey", "principal"];
    if (!fields.every(f => typeof value[f] === "string")) return null;
    return Object.fromEntries(fields.map(f => [f, value[f]]));
}

/** A file name for the backup download. */
export function backupFileName(serverName = "noXa", date = new Date()) {
    const safe = String(serverName).replace(/[^\p{L}\p{N}._-]+/gu, "-").replace(/^-+|-+$/g, "").slice(0, 40) || "noXa";
    return `${safe}-device-keys-${date.toISOString().slice(0, 10)}.noxa-backup.json`;
}
