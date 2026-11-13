// attachments.js — encrypted attachments in message bodies. Images load
// inline (setting inlineImages) and open in the lightbox; videos load on tap
// into a native player; voice messages play inline; other files download. Decrypted media are cached by
// location, storage name and key because the chat screens rebuild their log on every change.
import { app } from "../app.js";
import { fetchAttachment } from "../core/files.js";
import { toBase64 } from "../core/crypto.js";
import { formatSize, mediaKind, mimeOf } from "../core/message-format.js";
import * as settings from "../core/settings.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { audioMime } from "../core/voice-notes.js";
import { voiceNotePlayer } from "./voice-note.js";
import "./rich-strings.js";

const MAX_CACHED = 80;
const MAX_CACHED_BYTES = 150 * 1024 * 1024;
const cache = new Map(); // location|storage|key -> Promise<{url, blob}>, least recently used first; .entry once decrypted
let cacheEpoch = 0;

// Drops least recently used entries over either cap. An entry still loading,
// still shown (image, paused GIF, video) or just requested stays cached.
function evict(keep) {
    let bytes = 0;
    for (const pending of cache.values()) bytes += pending.entry?.blob.size || 0;
    if (cache.size <= MAX_CACHED && bytes <= MAX_CACHED_BYTES) return;
    const shown = new Set([...document.querySelectorAll("img, video")].flatMap(node => [node.src, node.dataset.animated]));
    for (const [key, pending] of cache) {
        if (cache.size <= MAX_CACHED && bytes <= MAX_CACHED_BYTES) break;
        const entry = pending.entry;
        if (key === keep || !entry || shown.has(entry.url)) continue;
        cache.delete(key);
        bytes -= entry.blob.size;
        URL.revokeObjectURL(entry.url);
    }
}

// Cached plaintext is found only with the same location, storage name AND
// attachment key that decrypted it, never by storage name alone.
function cacheKey(ref, channelID) {
    const master = ref.master instanceof Uint8Array ? toBase64(ref.master) : String(ref.master ?? "");
    return `${channelID}|${ref.storage}|${master}`;
}

/** {url, blob} of a decrypted attachment, fetched once per location, name and key. */
export function attachmentURL(ref, { conn, channelID }) {
    const key = cacheKey(ref, channelID);
    const hit = cache.get(key);
    if (hit) {
        cache.delete(key);
        cache.set(key, hit);
        return hit;
    }
    const epoch = cacheEpoch;
    const pending = fetchAttachment(conn, channelID, ref)
        .then(plain => {
            // A decryption that finishes after sign-out publishes nothing.
            if (epoch !== cacheEpoch) throw new Error("attachment cache cleared");
            const blob = new Blob([plain], { type: mimeOf(ref.name) });
            const entry = { url: URL.createObjectURL(blob), blob };
            if (cache.get(key) === pending) { pending.entry = entry; evict(key); }
            return entry;
        })
        .catch(error => { if (cache.get(key) === pending) cache.delete(key); throw error; });
    cache.set(key, pending);
    evict(key);
    return pending;
}

/** Drops every decrypted attachment and revokes its URL (sign-out, account replacement). */
export function clearAttachmentCache() {
    cacheEpoch++;
    for (const pending of cache.values()) pending.then(entry => URL.revokeObjectURL(entry.url)).catch(() => {});
    cache.clear();
}
app.addEventListener("signedout", clearAttachmentCache);
app.addEventListener("account-replaced", clearAttachmentCache);
// Settings → Clear cached images also drops the decrypted in-memory copies.
app.addEventListener("clear-media-cache", clearAttachmentCache);

let observer = null;
/** Runs load() once the node scrolls near the viewport. */
function whenVisible(node, load) {
    if (typeof IntersectionObserver !== "function") return load();
    observer ||= new IntersectionObserver(entries => {
        for (const entry of entries) {
            if (!entry.isIntersecting) continue;
            observer.unobserve(entry.target);
            entry.target.noxaLoad?.();
        }
    }, { rootMargin: "300px 0px" });
    node.noxaLoad = load;
    observer.observe(node);
}

function imageElement(ref, { url, blob }) {
    const open = () => import("./lightbox.js").then(({ openLightbox }) => openLightbox({ url, blob, name: ref.name }));
    const img = h("img", { class: "file-image", src: url, alt: ref.name, tabindex: "0", title: t("rich.openImage", { name: ref.name }),
        onclick: open, onkeydown: event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(); } } });
    if (/\.gif$/i.test(ref.name)) pauseWhenHidden(img, url);
    return img;
}

let gifObserver = null;
/**
 * An animated GIF that scrolls off screen is swapped for a still of its
 * current frame, so animations out of view stop costing CPU and battery.
 */
function pauseWhenHidden(img, url) {
    if (typeof IntersectionObserver !== "function") return;
    gifObserver ||= new IntersectionObserver(entries => {
        for (const { target, isIntersecting } of entries) {
            if (isIntersecting) {
                if (target.dataset.paused) { target.src = target.dataset.animated; delete target.dataset.paused; }
                continue;
            }
            if (target.dataset.paused || !target.complete || !target.naturalWidth) continue;
            try {
                const canvas = document.createElement("canvas");
                canvas.width = target.naturalWidth;
                canvas.height = target.naturalHeight;
                canvas.getContext("2d").drawImage(target, 0, 0);
                target.dataset.paused = "1";
                target.src = canvas.toDataURL("image/png");
            } catch { /* tainted or detached: keep animating */ }
        }
    }, { rootMargin: "100px 0px" });
    img.dataset.animated = url;
    gifObserver.observe(img);
}

function chip(ref, kind) {
    const size = h("span", { class: "file-size" }, formatSize(ref.size));
    const label = kind === "image" ? t("file.show", { name: ref.name }) : kind === "video" ? t("rich.playVideo", { name: ref.name }) : t("file.download", { name: ref.name });
    const node = h("button", { class: "file-chip", type: "button", "aria-label": label },
        h("span", { class: "file-icon", "aria-hidden": "true" }, kind === "image" ? "🖼" : kind === "video" ? "🎬" : "📄"),
        h("span", { class: "file-name" }, ref.name), size);
    return { node, size };
}

/** The element for one [file:…] token of a message. */
export function attachmentNode(ref, context) {
    // Voice messages (and other audio) play inline (ui/voice-note.js).
    const audio = audioMime(ref.name);
    if (audio) return voiceNotePlayer(() => fetchAttachment(context.conn, context.channelID, ref).then(plain => new Blob([plain], { type: audio })), ref.name, ref);
    const kind = mediaKind(ref.name);
    const { node, size } = chip(ref, kind);
    const loading = () => { node.disabled = true; node.setAttribute("aria-busy", "true"); size.textContent = t("file.loading"); };
    const done = () => { node.disabled = false; node.removeAttribute("aria-busy"); size.textContent = formatSize(ref.size); };
    const show = async () => {
        loading();
        try {
            const entry = await attachmentURL(ref, context);
            if (kind === "image") node.replaceWith(imageElement(ref, entry));
            else node.replaceWith(h("video", { class: "file-video", src: entry.url, controls: true, playsinline: true, preload: "metadata", "aria-label": ref.name }));
        } catch (error) {
            done();
            toast(t("common.error", { error: error.message }), "warn");
        }
    };
    if (kind) {
        node.onclick = show;
        if (kind === "image" && settings.get("inlineImages")) whenVisible(node, show);
        return node;
    }
    node.onclick = async () => {
        loading();
        try {
            const plain = await fetchAttachment(context.conn, context.channelID, ref);
            const url = URL.createObjectURL(new Blob([plain], { type: mimeOf(ref.name) }));
            const link = h("a", { href: url, download: ref.name });
            document.body.append(link);
            link.click();
            link.remove();
            setTimeout(() => URL.revokeObjectURL(url), 60000);
        } catch (error) {
            toast(t("common.error", { error: error.message }), "warn");
        }
        done();
    };
    return node;
}

/** Text nodes and attachment chips for a message body (now the rich view). */
export { messageBody } from "./rich-text.js";
