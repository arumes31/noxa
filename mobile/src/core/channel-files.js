// channel-files.js — the channel file browser's protocol and list logic,
// as the desktop's files-ui.js and client/files.go use it: FileList with
// virtual folders and quota, FileRename (rename, move to a folder or
// another channel), FileDelete, FileVersions and FileLink (a temporary
// download link). Channel files are stored as uploaded — plain /ft
// transfers under their real name, not the sealed attachment format; rows
// that ARE sealed chat attachments (".vcx") can only be opened in the chat.
import { sha256 } from "@noble/hashes/sha2.js";

import { Msg } from "./frame.js";
import { downloadBlob, uploadStream } from "./files.js";

/** Largest download the browser holds in memory before saving. */
export const MAX_DOWNLOAD_BYTES = 512 << 20;

export function hex(bytes) {
    return Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
}

/** 1536 -> "1.5 KB". */
export function formatBytes(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return "–";
    if (bytes < 1024) return `${bytes} B`;
    const units = ["KB", "MB", "GB", "TB"];
    let value = bytes / 1024;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
    return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`;
}

/** SHA-256 hex of bytes — the checksum the listing shows. */
export function checksum(bytes) {
    return hex(sha256(bytes));
}

/** Whether downloaded bytes match the listing's checksum. */
export function verifyChecksum(bytes, expected) {
    return !!expected && checksum(bytes) === String(expected).toLowerCase();
}

/** A chat attachment sealed with a key only the chat message holds. */
export function isSealed(entry) {
    return !!entry?.encrypted || String(entry?.name || "").toLowerCase().endsWith(".vcx");
}

/** "", "a", "a/b" — no leading, trailing or doubled slashes. */
export function normalizeFolder(folder) {
    return String(folder || "").split("/").map(part => part.trim()).filter(part => part && part !== "." && part !== "..").join("/");
}

/** The direct subfolders of folder, from the channel's folder list. */
export function subfolders(folders, folder) {
    const prefix = folder ? `${folder}/` : "";
    const out = new Set();
    for (const path of folders || []) {
        if (!path.startsWith(prefix)) continue;
        const rest = path.slice(prefix.length);
        if (rest) out.add(rest.split("/")[0]);
    }
    return [...out].sort((a, b) => a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }));
}

/** Breadcrumb parts of a folder: [{label, folder}], root first. */
export function breadcrumbs(folder) {
    const parts = folder ? folder.split("/") : [];
    return parts.map((label, i) => ({ label, folder: parts.slice(0, i + 1).join("/") }));
}

const byName = (a, b) => String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: "base" });

/** Filters by name and sorts by "name" | "size" | "date", like the desktop. */
export function visibleEntries(entries, { filter = "", sort = "name", descending = false } = {}) {
    const query = filter.trim().toLowerCase();
    return (entries || []).filter(entry => String(entry.name || "").toLowerCase().includes(query)).sort((a, b) => {
        let order = 0;
        if (sort === "size") order = (Number(a.size) || 0) - (Number(b.size) || 0);
        if (sort === "date") order = (new Date(a.uploaded_at || 0).getTime() || 0) - (new Date(b.uploaded_at || 0).getTime() || 0);
        return (order || byName(a.name || "", b.name || "")) * (descending ? -1 : 1);
    });
}

/** Quota use in percent (0–100), or null when unlimited. */
export function quotaPercent(used, quota) {
    if (!(quota > 0)) return null;
    return Math.min(100, Math.round(((used || 0) / quota) * 100));
}

/** "a/b/name" -> {folder: "a/b", name: "name"}. */
export function splitPath(path) {
    const clean = String(path || "").replace(/^\/+|\/+$/g, "");
    const at = clean.lastIndexOf("/");
    return at < 0 ? { folder: "", name: clean } : { folder: normalizeFolder(clean.slice(0, at)), name: clean.slice(at + 1) };
}

const LINK_PATH = /^\/dl\/([0-9a-f]{32})$/;

/**
 * The URL of a FileLinkResponse, or "" when the reply is unusable. Mirrors
 * the desktop's file-links.js: only https, only /dl/<32 hex>; without a
 * public origin the link uses this page's host and the health port.
 */
export function buildFileLink(response, host = globalThis.location?.hostname || "") {
    if (!response || typeof response.path !== "string" || !LINK_PATH.test(response.path)) return "";
    if (String(response.scheme || "").toLowerCase() !== "https") return "";
    try {
        let url;
        if (response.public_origin) {
            if (typeof response.public_origin !== "string" || /[\\\s?#]/.test(response.public_origin)) return "";
            url = new URL(response.public_origin);
            if (url.protocol !== "https:" || !url.hostname || url.username || url.password || url.search || url.hash || (url.pathname !== "" && url.pathname !== "/")) return "";
        } else {
            const port = response.health_port;
            if (!host || !Number.isInteger(port) || port <= 0 || port > 65535) return "";
            url = new URL("https://placeholder.invalid");
            url.hostname = host;
            url.port = String(port);
        }
        url.pathname = response.path;
        return url.href;
    } catch {
        return "";
    }
}

/** Requests of one channel's files over the control connection. */
export class ChannelFiles {
    constructor(conn, channelID) {
        this.conn = conn;
        this.channelID = channelID;
    }

    /** {entries, folders, used_bytes, quota_bytes} of one folder. */
    async list(folder = "") {
        const result = await this.conn.request(Msg.FileList, { channel_id: this.channelID, folder }, Msg.FileListResponse);
        return { entries: result.entries || [], folders: result.folders || [], used: result.used_bytes || 0, quota: result.quota_bytes || 0 };
    }

    #mutate(type, message) {
        return this.conn.request(type, { ...message, ack_requested: true }, Msg.FileMutationSaved, {
            accept: saved => saved.operation === type && saved.name === message.name && saved.channel_id === message.channel_id,
            timeout: 15000,
        });
    }

    /** Renames or moves a file; newChannelID 0 keeps the channel. */
    rename(folder, name, { newName = name, newFolder = folder, newChannelID = 0 } = {}) {
        return this.#mutate(Msg.FileRename, {
            channel_id: this.channelID, folder, name, new_name: newName, new_folder: normalizeFolder(newFolder),
            ...(newChannelID && newChannelID !== this.channelID ? { new_channel_id: newChannelID } : {}),
        });
    }

    delete(folder, name) {
        return this.#mutate(Msg.FileDelete, { channel_id: this.channelID, folder, name });
    }

    /** Older versions of a file, newest first; each downloads by its own name. */
    async versions(folder, name) {
        const result = await this.conn.request(Msg.FileVersions, { channel_id: this.channelID, folder, name }, Msg.FileVersionsResponse);
        return result.entries || [];
    }

    /** {url, expiresAt, sessionBound} of a temporary download link. */
    async link(folder, name) {
        const response = await this.conn.request(Msg.FileLink, { channel_id: this.channelID, folder, name }, Msg.FileLinkResponse);
        const url = buildFileLink(response);
        if (!url) throw new Error("the server sent an unusable link");
        return { url, expiresAt: response.expires_at * 1000, sessionBound: !!response.session_bound };
    }

    /** The file's bytes; the transfer itself checks the server's digest. */
    download(folder, name, { onProgress, signal, maxBytes = MAX_DOWNLOAD_BYTES } = {}) {
        return downloadBlob(this.conn, { channelID: this.channelID, folder }, name, maxBytes, undefined, { onProgress, signal });
    }

    upload(folder, file, { name = file.name, onProgress, signal } = {}) {
        return uploadStream(this.conn, { channelID: this.channelID, folder }, name, file, { onProgress, signal });
    }
}
