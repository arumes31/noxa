// history-envelope.js — mirror of internal/netproto/history_envelope.go.
// A history chunk's sealed plaintext is `<header JSON>\n<records JSON array>`;
// the header binds purpose, source/target device, transfer ID, seq/total and
// the records' SHA-256, signed by the source device's noXa key. Own-device DM
// copies use the same box keys but are never signed, so neither can be
// replayed as the other; DM receivers refuse text that starts like a header.
import { sha256 } from "@noble/hashes/sha2.js";
import { fromBase64, sign, toBase64, utf8 } from "./crypto.js";
import { verifySignature } from "./olm-keys.js";

export const HISTORY_ENVELOPE_VERSION = "noxa-history-v1";
export const HISTORY_ENVELOPE_PREFIX = `{"v":"${HISTORY_ENVELOPE_VERSION}"`;
export const HISTORY_ENVELOPE_RESERVE = 1024;
const MAX_CHUNKS = 256;
const hex = b => Array.from(b, x => x.toString(16).padStart(2, "0")).join("");
const field = s => typeof s === "string" && s.length > 0 && s.length <= 128 && !/[\r\n]/.test(s);

export function historyChunkTranscript(h) {
    if (h?.v !== HISTORY_ENVELOPE_VERSION || !field(h.source) || !field(h.target) || !field(h.transfer) ||
        !Number.isSafeInteger(h.seq) || !Number.isSafeInteger(h.total) || h.seq < 0 || h.total < 1 || h.total > MAX_CHUNKS || h.seq >= h.total ||
        typeof h.digest !== "string" || !/^[0-9a-f]{64}$/.test(h.digest)) throw new Error("invalid history chunk envelope");
    return utf8(["noxa-history-chunk-v1", h.source, h.target, h.transfer, String(h.seq), String(h.total), h.digest].join("\n"));
}

/** Builds the plaintext for one chunk (records: the JSON array string). */
export function encodeHistoryChunk({ source, target, transfer, seq, total }, records, edSecret) {
    const h = { v: HISTORY_ENVELOPE_VERSION, source, target, transfer, seq, total, digest: hex(sha256(utf8(records))) };
    h.sig = toBase64(sign(historyChunkTranscript(h), edSecret));
    return JSON.stringify(h) + "\n" + records;
}

/**
 * Opens an envelope for this device. Returns the records array, or null for
 * anything else (a bare array, a DM copy, another target/source/position, a
 * wrong digest or signature).
 */
export function decodeHistoryChunk(plain, expected) {
    return decodeHistoryEnvelope(plain, expected)?.records ?? null;
}

/** As decodeHistoryChunk, returning {header, records} (the signed header names the transfer). */
export function decodeHistoryEnvelope(plain, { source, target, seq, total, signingKey }) {
    if (typeof plain !== "string" || !plain.startsWith(HISTORY_ENVELOPE_PREFIX)) return null;
    const i = plain.indexOf("\n");
    if (i < 0) return null;
    try {
        const h = JSON.parse(plain.slice(0, i)), records = plain.slice(i + 1);
        const transcript = historyChunkTranscript(h);
        if (h.source !== source || h.target !== target || h.seq !== seq || h.total !== total || h.digest !== hex(sha256(utf8(records))) ||
            !verifySignature(fromBase64(h.sig), transcript, signingKey)) return null;
        const list = JSON.parse(records);
        return Array.isArray(list) ? { header: h, records: list } : null;
    } catch { return null; }
}

export const isHistoryEnvelope = text => typeof text === "string" && text.startsWith(HISTORY_ENVELOPE_PREFIX);
