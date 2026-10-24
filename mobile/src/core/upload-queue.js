// upload-queue.js — one queue for every upload of this tab: channel files
// from the file browser and (sealed) chat attachments from the composers.
// Uploads run one at a time, report progress and can be cancelled,
// retried and dismissed. ui/upload-sheet.js shows the queue.
//
//     import { enqueueUpload } from "../core/upload-queue.js";
//     const item = enqueueUpload(file, { channelID: 4, sealed: true });
//     const { token } = await item.done; // the [file:…] token for the message
//
// target: {channelID, folder?, sealed?, name?}. Plain targets store the file
// as-is under name (default file.name) in channelID/folder, like the desktop
// file browser. sealed targets encrypt it as a chat attachment and resolve
// with its token. item.done resolves once the upload succeeds and rejects
// when it is cancelled or dismissed unfinished; an error keeps it pending so
// a retry can still settle it (a retry after cancel starts a new item.done).
import { fileToken, MAX_ATTACHMENT_BYTES, sealAttachment, storageName, uploadStream } from "./files.js";

let getConn = () => null;
let sequence = 0;
const items = [];
const events = new EventTarget();
let running = null;

/** The control connection uploads use (set by the app once at start). */
export function setUploadConnection(provider) {
    getConn = provider;
}

function changed(item) {
    events.dispatchEvent(new CustomEvent("change", { detail: item }));
}

/** Calls listener(item) whenever the queue or an item changes; returns stop. */
export function onUploadsChanged(listener) {
    const handler = event => listener(event.detail);
    events.addEventListener("change", handler);
    return () => events.removeEventListener("change", handler);
}

/** Every item still listed, oldest first. */
export function uploads() {
    return items.slice();
}

/** Items waiting or running. */
export function pendingUploads() {
    return items.filter(item => item.status === "queued" || item.status === "active");
}

export function enqueueUpload(file, target) {
    if (!file) throw new Error("no file");
    const item = {
        id: `up-${++sequence}`,
        file,
        target: { channelID: Number(target?.channelID) || 0, folder: target?.folder || "", sealed: !!target?.sealed, name: target?.name || file.name || "file" },
        name: target?.name || file.name || "file",
        total: file.size || 0,
        sent: 0,
        status: "queued", // queued | active | done | error | canceled
        error: "",
        result: null,
        controller: null,
    };
    settle(item);
    items.push(item);
    changed(item);
    pump();
    return item;
}

function settle(item) {
    item.settled = false;
    item.done = new Promise((resolve, reject) => {
        item.resolve = value => { item.settled = true; resolve(value); };
        item.reject = error => { item.settled = true; reject(error); };
    });
    item.done.catch(() => {}); // callers that do not await must not see unhandled rejections
}

function canceledError() {
    return Object.assign(new Error("upload canceled"), { name: "AbortError" });
}

async function transfer(item, signal) {
    const conn = getConn();
    if (!conn?.open) throw new Error("not connected");
    const { channelID, folder, sealed, name } = item.target;
    const onProgress = sent => { item.sent = sent; changed(item); };
    if (!sealed) {
        await uploadStream(conn, { channelID, folder }, name, item.file, { onProgress, signal });
        return { channelID, folder, name };
    }
    if (item.file.size > MAX_ATTACHMENT_BYTES) throw new Error("attachment exceeds 25 MiB");
    const plain = new Uint8Array(await item.file.arrayBuffer());
    const { blob, master } = await sealAttachment(plain);
    const storage = storageName(blob);
    item.total = blob.length;
    await uploadStream(conn, channelID, storage, new Blob([blob]), { onProgress, signal });
    return { channelID, storage, token: fileToken(storage, master, plain.length, name) };
}

async function pump() {
    if (running) return;
    const item = items.find(entry => entry.status === "queued");
    if (!item) return;
    running = item;
    item.status = "active";
    item.sent = 0;
    item.error = "";
    item.controller = new AbortController();
    changed(item);
    try {
        item.result = await transfer(item, item.controller.signal);
        item.status = "done";
        item.sent = item.total;
        item.resolve(item.result);
    } catch (error) {
        if (item.status === "active") {
            item.status = item.controller.signal.aborted ? "canceled" : "error";
            item.error = item.status === "canceled" ? "" : error.message;
        }
    }
    item.controller = null;
    running = null;
    changed(item);
    pump();
}

export function cancelUpload(id) {
    const item = items.find(entry => entry.id === id);
    if (!item || (item.status !== "queued" && item.status !== "active")) return;
    const wasActive = item.status === "active";
    item.status = "canceled";
    item.controller?.abort();
    item.reject(canceledError());
    if (!wasActive) changed(item);
}

/** Puts a failed or cancelled upload back in the queue. */
export function retryUpload(id) {
    const item = items.find(entry => entry.id === id);
    if (!item || (item.status !== "error" && item.status !== "canceled")) return;
    item.status = "queued";
    item.sent = 0;
    item.error = "";
    if (item.settled) settle(item);
    changed(item);
    pump();
}

/** Removes an item from the list (cancelling it first if needed). */
export function dismissUpload(id) {
    const index = items.findIndex(entry => entry.id === id);
    if (index < 0) return;
    const [item] = items.splice(index, 1);
    if (item.status === "queued" || item.status === "active") {
        item.status = "canceled";
        item.controller?.abort();
    }
    if (!item.settled) item.reject(canceledError());
    changed(item);
}

/** Removes every finished item. */
export function clearFinished() {
    for (const item of items.filter(entry => entry.status === "done")) dismissUpload(item.id);
}

/** Test support. */
export function _resetUploads() {
    for (const item of items.splice(0)) item.controller?.abort();
    running = null;
}
