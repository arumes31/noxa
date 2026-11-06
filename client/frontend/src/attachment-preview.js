// attachment-preview.js — which attachments get an inline preview (575) and
// how text is decoded for one. Text is only ever shown through textContent;
// a file that does not decode as UTF-8 text stays a download.
export const AUDIO_MIME = Object.freeze({
    mp3: "audio/mpeg", m4a: "audio/mp4", aac: "audio/aac", ogg: "audio/ogg", oga: "audio/ogg",
    opus: "audio/ogg", wav: "audio/wav", flac: "audio/flac",
});
export const TEXT_EXTS = Object.freeze(new Set([
    "txt", "md", "log", "csv", "tsv", "json", "yaml", "yml", "toml", "ini", "conf", "cfg", "xml",
    "go", "js", "mjs", "ts", "tsx", "jsx", "py", "rb", "rs", "java", "kt", "c", "h", "cpp", "hpp", "cs",
    "php", "sh", "bash", "ps1", "sql", "css", "scss", "html", "lua", "swift", "diff", "patch",
]));
// Text previews decode at most this much (base64 characters, ~384 KB).
export const TEXT_MAX_B64 = 512 * 1024;
export const TEXT_PREVIEW_LINES = 40;

/** "audio", "text" or "" for a file extension. */
export function previewKind(ext) {
    const key = String(ext || "").toLowerCase();
    if (Object.hasOwn(AUDIO_MIME, key)) return "audio";
    if (TEXT_EXTS.has(key)) return "text";
    return "";
}

/**
 * Decodes base64 file content as text for a preview. Returns null for
 * binary content (NUL bytes or invalid UTF-8). lines is the total count;
 * head holds at most maxLines of them.
 */
export function decodeTextPreview(b64, maxLines = TEXT_PREVIEW_LINES) {
    let bytes;
    try {
        bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
    } catch {
        return null;
    }
    if (bytes.subarray(0, 8192).includes(0)) return null;
    let text;
    try {
        text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    } catch {
        return null;
    }
    text = text.replace(/^﻿/, "").replace(/\r\n?/g, "\n");
    const all = text.endsWith("\n") ? text.slice(0, -1).split("\n") : text.split("\n");
    return { text, lines: all.length, head: all.slice(0, maxLines).join("\n"), truncated: all.length > maxLines };
}
