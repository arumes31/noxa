// chat-export.js — transcripts of a loaded conversation and the
// passphrase-encrypted export container.
//
// The desktop's .noxachat container (client/chat_export.go) uses argon2id
// and XSalsa20-Poly1305; argon2 is not in WebCrypto and the PWA takes no new
// dependency for it, so the phone writes its own JSON container instead:
//
//   {"format":"noxa-chat-export","v":1,"kdf":"pbkdf2-sha256","iter":600000,
//    "cipher":"aes-256-gcm","salt":"<b64 16 bytes>","iv":"<b64 12 bytes>",
//    "data":"<b64 ciphertext+tag>"}
//
// key = PBKDF2-HMAC-SHA256(passphrase UTF-8, salt, iter, 32 bytes); data is
// AES-256-GCM over the UTF-8 export JSON with no additional data.
import { fromBase64, toBase64 } from "./crypto.js";

export const EXPORT_ITERATIONS = 600000;
export const EXPORT_FORMAT = "noxa-chat-export";
const validIterations = value => Number.isInteger(value) && value >= 1000 && value <= 10000000;

function pad(n) {
    return String(n).padStart(2, "0");
}

/** "2026-10-24 13:05:09" in local time, as the desktop transcript. */
export function stamp(seconds) {
    const d = new Date(seconds * 1000);
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

const TAGS = { e2e: "[end-to-end encrypted] ", server: "[server encrypted] " };

/**
 * One transcript line per message, oldest first. Messages are normalized:
 * {id, from, fromUniqueID, sentAt, text, deleted, edited, encryption}.
 * Unopened bodies keep a placeholder so gaps stay visible.
 */
export function transcript(messages, { title = "", complete = true } = {}) {
    const lines = [];
    const unreadable = messages.filter(m => !m.deleted && typeof m.text !== "string").length;
    if (title) lines.push(`# noXa export — ${title}`);
    if (!complete || unreadable) lines.push(`# ${messages.length} messages, ${unreadable} unreadable (no key), ${complete ? "complete" : "TRUNCATED: only the loaded history"}`);
    for (const m of messages) {
        let body = m.deleted ? "(deleted)" : typeof m.text === "string" ? m.text : "(could not be decrypted)";
        if (m.edited && !m.deleted) body += " (edited)";
        const tag = m.deleted ? "" : TAGS[m.encryption] || "[unverified] ";
        lines.push(`[${stamp(m.sentAt)}] ${tag}${m.from || m.fromUniqueID || "?"}: ${body}`);
    }
    return lines.length ? lines.join("\n") + "\n" : "";
}

/** The structured export: metadata plus the normalized messages. */
export function exportJSON(messages, { title = "", kind = "", complete = true, exportedAt = Date.now() / 1000 } = {}) {
    return JSON.stringify({
        format: "noxa-chat-transcript", v: 1, title, kind, complete, exported_at: Math.floor(exportedAt),
        messages: messages.map(m => ({
            id: m.id ?? null, from: m.from || "", from_unique_id: m.fromUniqueID || "", sent_at: m.sentAt,
            text: m.deleted ? null : typeof m.text === "string" ? m.text : null,
            deleted: !!m.deleted, edited: !!m.edited, encryption: m.encryption || "",
        })),
    }, null, 2);
}

async function deriveKey(passphrase, salt, iterations, subtle) {
    const material = await subtle.importKey("raw", new TextEncoder().encode(passphrase), "PBKDF2", false, ["deriveKey"]);
    return subtle.deriveKey({ name: "PBKDF2", hash: "SHA-256", salt, iterations }, material, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
}

/** Encrypts text with a passphrase; returns the container as a JSON string. */
export async function sealExport(text, passphrase, { iterations = EXPORT_ITERATIONS, subtle = globalThis.crypto.subtle } = {}) {
    if (!passphrase) throw new Error("a passphrase is required for an encrypted export");
    if (!validIterations(iterations)) throw new Error("unsupported export iteration count");
    const salt = globalThis.crypto.getRandomValues(new Uint8Array(16));
    const iv = globalThis.crypto.getRandomValues(new Uint8Array(12));
    const key = await deriveKey(passphrase, salt, iterations, subtle);
    const data = new Uint8Array(await subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(text)));
    return JSON.stringify({ format: EXPORT_FORMAT, v: 1, kdf: "pbkdf2-sha256", iter: iterations, cipher: "aes-256-gcm", salt: toBase64(salt), iv: toBase64(iv), data: toBase64(data) });
}

/** Reverses sealExport; throws on a wrong passphrase or a foreign file. */
export async function openExport(container, passphrase, { subtle = globalThis.crypto.subtle } = {}) {
    const box = typeof container === "string" ? JSON.parse(container) : container;
    if (box?.format !== EXPORT_FORMAT || box.v !== 1 || box.kdf !== "pbkdf2-sha256" || box.cipher !== "aes-256-gcm" || !validIterations(box.iter)) throw new Error("not a noXa encrypted chat export");
    const key = await deriveKey(passphrase, fromBase64(box.salt), box.iter, subtle);
    try {
        return new TextDecoder().decode(await subtle.decrypt({ name: "AES-GCM", iv: fromBase64(box.iv) }, key, fromBase64(box.data)));
    } catch {
        throw new Error("wrong passphrase or corrupt export");
    }
}

/** A file name for an export: noxa-<title>-<date>.<ext>. */
export function exportFileName(title, ext, now = new Date()) {
    const slug = String(title || "chat").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "-").replace(/^-+|-+$/g, "").slice(0, 40) || "chat";
    return `noxa-${slug}-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}.${ext}`;
}
