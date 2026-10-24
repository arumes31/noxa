// files.js — encrypted chat attachments, compatible with the desktop
// (client/chat_attachments.go, internal/e2ee EncryptFileChunk):
//   blob   "VCXGCM1\n" then per 64 KiB chunk: uint32 BE length || nonce12 ||
//          AES-256-GCM(chunk, key_i, aad = "attachment" || uint64 BE i)
//   key_i  HKDF-SHA256(master, salt none, "voicx:message:file:attachment" || uint64 BE i)
//   name   first 32 hex of SHA-256(blob) + ".vcx"
//   token  [file:<name>#<base64 master>#nx1;s=<plain bytes>#<display name>]
// Bytes move through /ft: the file-transfer framing over a WebSocket,
// authorized by the single-use token from MsgFileTransferInit.
import { hkdf } from "@noble/hashes/hkdf.js";
import { sha256 } from "@noble/hashes/sha2.js";

import { fromBase64, toBase64, utf8 } from "./crypto.js";

export const MAX_ATTACHMENT_BYTES = 25 << 20;
const HEADER = utf8("VCXGCM1\n");
const CHUNK = 64 * 1024;
const GCM_OVERHEAD = 12 + 16;
const MsgFileTransferInit = 25;
const MsgFileTransferInitResponse = 26;
const Frame = Object.freeze({ init: 1, chunk: 2, digest: 3, status: 4 });

function uint64(index) {
    const out = new Uint8Array(8);
    new DataView(out.buffer).setBigUint64(0, BigInt(index));
    return out;
}

function concat(...parts) {
    const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let offset = 0;
    for (const part of parts) {
        out.set(part, offset);
        offset += part.length;
    }
    return out;
}

function hex(bytes) {
    return Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
}

async function chunkKey(master, index, usage) {
    const raw = hkdf(sha256, master, undefined, concat(utf8("voicx:message:file:attachment"), uint64(index)), 32);
    return crypto.subtle.importKey("raw", raw, "AES-GCM", false, [usage]);
}

/** Seals plaintext bytes; returns {blob, master}. */
export async function sealAttachment(plain, master = crypto.getRandomValues(new Uint8Array(32))) {
    const parts = [HEADER];
    for (let offset = 0, index = 0; offset < plain.length; offset += CHUNK, index++) {
        const nonce = crypto.getRandomValues(new Uint8Array(12));
        const key = await chunkKey(master, index, "encrypt");
        const sealed = new Uint8Array(await crypto.subtle.encrypt(
            { name: "AES-GCM", iv: nonce, additionalData: concat(utf8("attachment"), uint64(index)) }, key, plain.subarray(offset, offset + CHUNK)));
        const length = new Uint8Array(4);
        new DataView(length.buffer).setUint32(0, 12 + sealed.length);
        parts.push(length, nonce, sealed);
    }
    return { blob: concat(...parts), master };
}

/** Opens a sealed attachment; throws on tampering or a wrong key. */
export async function openAttachment(blob, master, maxBytes = MAX_ATTACHMENT_BYTES) {
    if (blob.length < HEADER.length || !HEADER.every((b, i) => blob[i] === b)) throw new Error("unsupported attachment format");
    const view = new DataView(blob.buffer, blob.byteOffset, blob.byteLength);
    const parts = [];
    let total = 0;
    for (let offset = HEADER.length, index = 0; offset < blob.length; index++) {
        if (offset + 4 > blob.length) throw new Error("invalid attachment chunk framing");
        const length = view.getUint32(offset);
        offset += 4;
        if (length < GCM_OVERHEAD || length > CHUNK + GCM_OVERHEAD || offset + length > blob.length) throw new Error("invalid attachment chunk framing");
        const chunk = blob.subarray(offset, offset + length);
        offset += length;
        const key = await chunkKey(master, index, "decrypt");
        let plain;
        try {
            plain = new Uint8Array(await crypto.subtle.decrypt(
                { name: "AES-GCM", iv: chunk.subarray(0, 12), additionalData: concat(utf8("attachment"), uint64(index)) }, key, chunk.subarray(12)));
        } catch {
            throw new Error("attachment decryption failed");
        }
        total += plain.length;
        if (total > maxBytes) throw new Error("attachment exceeds the size limit");
        parts.push(plain);
    }
    return concat(...parts);
}

export function storageName(blob) {
    return `${hex(sha256(blob)).slice(0, 32)}.vcx`;
}

/** The chat body token for an uploaded attachment. */
export function fileToken(storage, master, size, name) {
    const display = String(name || "file").replace(/[\]#]/g, "_");
    return `[file:${storage}#${toBase64(master)}#nx1;s=${size}#${display}]`;
}

const TOKEN = /\[file:([^\]]+)\]/g;

/** Finds attachment tokens in a message body: [{token, storage, master, size, name}]. */
export function parseFileTokens(body) {
    const out = [];
    for (const match of String(body || "").matchAll(TOKEN)) {
        const fields = match[1].split("#");
        if (fields.length < 3) continue;
        let size = 0;
        let name = fields.slice(2).join("#");
        if (/^nx1(;[a-z]{1,8}=[A-Za-z0-9_.-]+)*$/.test(fields[2] || "") && fields.length >= 4) {
            const sizeField = fields[2].split(";").find(f => f.startsWith("s="));
            size = Number(sizeField?.slice(2)) || 0;
            name = fields.slice(3).join("#");
        }
        let master;
        try { master = fromBase64(fields[1]); } catch { continue; }
        if (master.length !== 32) continue;
        out.push({ token: match[0], storage: fields[0], master, size, name });
    }
    return out;
}

// --- Transfer over /ft ----------------------------------------------------------

function encodeRaw(type, payload) {
    const out = new Uint8Array(6 + payload.length);
    const view = new DataView(out.buffer);
    view.setUint32(0, payload.length + 2);
    view.setUint16(4, type);
    out.set(payload, 6);
    return out;
}

function encodeJSON(type, message) {
    return encodeRaw(type, utf8(JSON.stringify(message)));
}

/** A WebSocket reader that yields whole frames {type, payload}. */
function openTransferSocket(url) {
    return new Promise((resolve, reject) => {
        const socket = new WebSocket(url);
        socket.binaryType = "arraybuffer";
        let buffer = new Uint8Array(0);
        const frames = [];
        const waiters = [];
        let closed = null;
        const deliver = () => {
            while (frames.length && waiters.length) waiters.shift().resolve(frames.shift());
            if (closed) while (waiters.length) waiters.shift().reject(closed);
        };
        socket.onmessage = event => {
            buffer = concat(buffer, new Uint8Array(event.data));
            while (buffer.length >= 6) {
                const view = new DataView(buffer.buffer, buffer.byteOffset, buffer.byteLength);
                const length = view.getUint32(0);
                if (length < 2 || length > (1 << 20)) {
                    socket.close();
                    return;
                }
                if (buffer.length < 4 + length) break;
                frames.push({ type: view.getUint16(4), payload: buffer.slice(6, 4 + length) });
                buffer = buffer.slice(4 + length);
            }
            deliver();
        };
        socket.onclose = () => {
            closed = new Error("file transfer connection closed");
            deliver();
        };
        socket.onerror = () => reject(new Error("could not reach the file server"));
        socket.onopen = () => resolve({
            send: bytes => socket.send(bytes),
            next: () => new Promise((res, rej) => { waiters.push({ resolve: res, reject: rej }); deliver(); }),
            close: () => socket.close(),
        });
    });
}

function transferURL() {
    const { protocol, host } = globalThis.location;
    return `${protocol === "https:" ? "wss:" : "ws:"}//${host}/ft`;
}

/** A channel ID, or {channelID, folder} for a private group's folder. */
function where(location) {
    return typeof location === "object" ? { channel_id: location.channelID || 0, folder: location.folder || "" } : { channel_id: location };
}

async function initTransfer(conn, request) {
    return conn.request(MsgFileTransferInit, request, MsgFileTransferInitResponse);
}

function statusError(frame) {
    try {
        const status = JSON.parse(new TextDecoder().decode(frame.payload));
        return new Error(status.error || "file transfer failed");
    } catch {
        return new Error("file transfer failed");
    }
}

/** Uploads a sealed blob into a channel (0 = server chat) under storage. */
export async function uploadBlob(conn, location, storage, blob, url = transferURL()) {
    const init = await initTransfer(conn, { ...where(location), direction: "upload", name: storage, size: blob.length });
    const socket = await openTransferSocket(url);
    try {
        socket.send(encodeJSON(Frame.init, { token: init.token, transfer_id: init.transfer_id }));
        for (let offset = 0; offset < blob.length; offset += 32 * 1024) socket.send(encodeRaw(Frame.chunk, blob.subarray(offset, offset + 32 * 1024)));
        socket.send(encodeJSON(Frame.digest, { sha256: hex(sha256(blob)) }));
        const reply = await socket.next();
        if (reply.type !== Frame.status) throw new Error("unexpected file server reply");
        const status = JSON.parse(new TextDecoder().decode(reply.payload));
        if (!status.ok) throw new Error(status.error || "upload failed");
    } finally {
        socket.close();
    }
}

/** Downloads a file's bytes and checks the server's digest. */
export async function downloadBlob(conn, location, storage, maxBytes, url = transferURL()) {
    const init = await initTransfer(conn, { ...where(location), direction: "download", name: storage });
    const socket = await openTransferSocket(url);
    try {
        socket.send(encodeJSON(Frame.init, { token: init.token, transfer_id: init.transfer_id }));
        const parts = [];
        let total = 0;
        for (;;) {
            const frame = await socket.next();
            if (frame.type === Frame.chunk) {
                total += frame.payload.length;
                if (total > maxBytes) throw new Error("attachment exceeds the size limit");
                parts.push(frame.payload);
            } else if (frame.type === Frame.digest) {
                const blob = concat(...parts);
                const { sha256: expected } = JSON.parse(new TextDecoder().decode(frame.payload));
                if (hex(sha256(blob)) !== expected) throw new Error("file digest mismatch");
                return blob;
            } else {
                throw statusError(frame);
            }
        }
    } finally {
        socket.close();
    }
}

/** Seals, uploads and returns the chat token for a picked file. */
export async function uploadAttachment(conn, channelID, file) {
    if (file.size > MAX_ATTACHMENT_BYTES) throw new Error("attachment exceeds 25 MiB");
    const plain = new Uint8Array(await file.arrayBuffer());
    const { blob, master } = await sealAttachment(plain);
    const storage = storageName(blob);
    await uploadBlob(conn, channelID, storage, blob);
    return fileToken(storage, master, plain.length, file.name);
}

/** Downloads and opens an attachment referenced in a chat message. */
export async function fetchAttachment(conn, channelID, ref) {
    const maxWire = HEADER.length + MAX_ATTACHMENT_BYTES + Math.ceil(MAX_ATTACHMENT_BYTES / CHUNK) * (4 + GCM_OVERHEAD);
    const blob = await downloadBlob(conn, channelID, ref.storage, maxWire);
    return openAttachment(blob, ref.master);
}
