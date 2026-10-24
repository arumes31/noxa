// share-store.js — content shared into the app from Android's share sheet
// (Web Share Target). The service worker receives the POST to /app/share,
// keeps text and files here, sealed like the other stores, and redirects to
// #/share, where the user picks a conversation. Only the latest share is kept.
import { openDB, run, seal, Stores, unseal } from "./db.js";

const KEY = "pending";
export const MAX_SHARED_FILES = 10;

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** Joins the share target's title, text and url fields into one message text. */
export function shareText({ title = "", text = "", url = "" } = {}) {
    const parts = [];
    for (const part of [title, text, url].map(p => String(p || "").trim())) {
        if (part && !parts.some(existing => existing.includes(part))) parts.push(part);
    }
    return parts.join("\n");
}

/** Stores a share: {text, files: File[]} (files beyond the limit are dropped). */
export async function putShare({ text = "", files = [] }) {
    const db = await openDB();
    try {
        const sealedFiles = [];
        for (const file of files.slice(0, MAX_SHARED_FILES)) {
            const record = await seal(db, new Uint8Array(await file.arrayBuffer()));
            sealedFiles.push({ name: file.name || "shared", type: file.type || "", record });
        }
        const value = { at: Date.now(), text: await seal(db, encoder.encode(text)), files: sealedFiles };
        await run(db, Stores.share, "readwrite", store => store.put(value, KEY));
    } finally {
        db.close();
    }
}

/** The waiting share as {at, text, files: File[]}, or null. */
export async function getShare() {
    const db = await openDB();
    try {
        const value = await run(db, Stores.share, "readonly", store => store.get(KEY));
        if (!value) return null;
        const files = [];
        for (const file of value.files || []) files.push(new File([await unseal(db, file.record)], file.name, { type: file.type }));
        return { at: value.at, text: decoder.decode(await unseal(db, value.text)), files };
    } finally {
        db.close();
    }
}

export async function clearShare() {
    const db = await openDB();
    try {
        await run(db, Stores.share, "readwrite", store => store.delete(KEY));
    } finally {
        db.close();
    }
}
