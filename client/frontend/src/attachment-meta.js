// attachment-meta.js mirrors client/chat_attachment_meta.go: the optional
// metadata segment of an attachment token,
//
//     [file:<storage>#<key>#nx1;s=<bytes>;d=<ms>;w=<peaks>#<display name>]
//
// s is the plaintext size, d a voice note's duration in milliseconds and w its
// waveform (up to 64 levels, one base64url character 0..63 each). The token
// lives only inside the sealed message body, so the metadata is exactly as
// private as the file key. Tokens without the segment carry no metadata and
// the UI simply shows none.
import { currentLanguage } from "./i18n.js";

const SEGMENT = /^nx1(;[a-z]{1,8}=[A-Za-z0-9_.-]+)*$/;
const PEAKS = /^[A-Za-z0-9_-]{1,64}$/;
const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
const MAX_SEGMENT = 256;
const MAX_SIZE = 2 ** 40;
const MAX_DURATION = 6 * 60 * 60 * 1000;
export const WAVEFORM_BARS = 48;

const validSize = value => Number.isSafeInteger(value) && value > 0 && value <= MAX_SIZE;
const validDuration = value => Number.isSafeInteger(value) && value > 0 && value <= MAX_DURATION;

// parseAttachmentMeta returns {size, durationMs, peaks} (each null when
// absent or out of range), or null for anything that is not an nx1 segment.
export function parseAttachmentMeta(segment) {
    if (typeof segment !== "string" || segment.length > MAX_SEGMENT || !SEGMENT.test(segment)) return null;
    const meta = { size: null, durationMs: null, peaks: null };
    for (const field of segment.split(";").slice(1)) {
        const [key, value] = field.split("=");
        if (key === "s" && /^\d+$/.test(value) && validSize(Number(value))) meta.size = Number(value);
        else if (key === "d" && /^\d+$/.test(value) && validDuration(Number(value))) meta.durationMs = Number(value);
        else if (key === "w" && PEAKS.test(value)) meta.peaks = decodePeaks(value);
    }
    return meta;
}

// encodePeaks quantizes levels in 0..1 to one base64url character each.
export function encodePeaks(levels) {
    if (!Array.isArray(levels) || !levels.length) return "";
    return levels.slice(0, 64).map(level => {
        const value = Number.isFinite(level) ? Math.min(1, Math.max(0, level)) : 0;
        return ALPHABET[Math.round(value * 63)];
    }).join("");
}

export function decodePeaks(text) {
    if (typeof text !== "string" || !PEAKS.test(text)) return null;
    return [...text].map(character => ALPHABET.indexOf(character) / 63);
}

export function encodeAttachmentMeta({ size, durationMs, peaks } = {}) {
    let out = "";
    if (validSize(size)) out += `;s=${size}`;
    if (validDuration(durationMs)) out += `;d=${durationMs}`;
    const waveform = Array.isArray(peaks) ? encodePeaks(peaks) : "";
    if (waveform) out += `;w=${waveform}`;
    return out ? "nx1" + out : "";
}

// splitNameMeta separates the segment from the name part of a parsed
// reference. A name that does not start with a valid segment is left whole.
export function splitNameMeta(rest) {
    const cut = rest.indexOf("#");
    if (cut < 0) return { name: rest, meta: null };
    const meta = parseAttachmentMeta(rest.slice(0, cut));
    return meta ? { name: rest.slice(cut + 1), meta } : { name: rest, meta: null };
}

// withAttachmentMeta adds fields (a voice note's duration and waveform) to a
// token the upload returned, keeping what it already carries (its size). It
// returns the token unchanged when it is not a sealed attachment token.
export function withAttachmentMeta(token, extra = {}) {
    const match = /^\[file:([^\]#]*)#([^\]#]*)#([^\]]*)\]$/.exec(String(token ?? ""));
    if (!match) return token;
    const { name, meta } = splitNameMeta(match[3]);
    const merged = { size: meta?.size ?? null, durationMs: meta?.durationMs ?? null, peaks: meta?.peaks ?? null };
    if (validDuration(extra.durationMs)) merged.durationMs = extra.durationMs;
    if (Array.isArray(extra.peaks) && extra.peaks.length) merged.peaks = extra.peaks;
    const segment = encodeAttachmentMeta(merged);
    return `[file:${match[1]}#${match[2]}#${segment ? segment + "#" : ""}${name}]`;
}

// formatFileSize renders bytes with Windows-style binary units: "940 B",
// "12 KB", "1.2 MB" ("1,2 MB" in German).
export function formatFileSize(bytes, language = currentLanguage()) {
    if (!validSize(bytes)) return "";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let value = bytes, unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
    const digits = unit === 0 || value >= 10 ? 0 : 1;
    const number = new Intl.NumberFormat(language === "de" ? "de-DE" : "en-GB", { maximumFractionDigits: digits }).format(value);
    return `${number} ${units[unit]}`;
}

// formatDuration renders milliseconds as m:ss (h:mm:ss from an hour).
export function formatDuration(ms) {
    if (!validDuration(ms)) return "";
    const total = Math.max(1, Math.round(ms / 1000));
    const hours = Math.floor(total / 3600), minutes = Math.floor(total / 60) % 60, seconds = total % 60;
    const ss = String(seconds).padStart(2, "0");
    return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${ss}` : `${minutes}:${ss}`;
}

// resamplePeaks stretches a stored waveform onto a player's bar count.
export function resamplePeaks(peaks, bars) {
    if (!Array.isArray(peaks) || !peaks.length || !(bars > 0)) return [];
    return Array.from({ length: bars }, (_, index) => peaks[Math.min(peaks.length - 1, Math.floor(index * peaks.length / bars))]);
}

// normalizePeaks scales levels so the loudest bar is full height; speech
// rarely reaches full scale, and a flat waveform would hide its shape.
export function normalizePeaks(levels) {
    if (!Array.isArray(levels) || !levels.length) return [];
    const finite = levels.map(level => (Number.isFinite(level) ? Math.max(0, level) : 0));
    const max = Math.max(...finite);
    return max > 0 ? finite.map(level => level / max) : finite;
}

// voicePreview is what a voice note shows before download: its duration
// ("0:14", "" when unknown) and one bar height in percent per stored peak
// ([] when the reference carries no waveform).
export function voicePreview(meta) {
    const bars = Array.isArray(meta?.peaks) ? meta.peaks.map(level => Math.max(12, Math.round(Math.min(1, Math.max(0, Number(level) || 0)) * 100))) : [];
    return { duration: formatDuration(meta?.durationMs), bars };
}
