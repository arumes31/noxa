// attachments.js — encrypted attachments in message bodies. Images load
// inline (setting inlineImages) and open in the lightbox; videos load on tap
// into a native player; voice messages play inline; other files download. Decrypted media are cached by
// storage name because the chat screens rebuild their log on every change.
import { fetchAttachment } from "../core/files.js";
import { formatSize, mediaKind, mimeOf } from "../core/message-format.js";
import * as settings from "../core/settings.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { audioMime } from "../core/voice-notes.js";
import { voiceNotePlayer } from "./voice-note.js";
import "./rich-strings.js";

const MAX_CACHED = 80;
const cache = new Map(); // storage -> Promise<{url, blob}>

/** {url, blob} of a decrypted attachment, fetched once per storage name. */
export function attachmentURL(ref, { conn, channelID }) {
    if (!cache.has(ref.storage)) {
        const pending = fetchAttachment(conn, channelID, ref)
            .then(plain => {
                const blob = new Blob([plain], { type: mimeOf(ref.name) });
                return { url: URL.createObjectURL(blob), blob };
            })
            .catch(error => { cache.delete(ref.storage); throw error; });
        cache.set(ref.storage, pending);
        if (cache.size > MAX_CACHED) {
            const [oldest, url] = cache.entries().next().value;
            cache.delete(oldest);
            url.then(entry => URL.revokeObjectURL(entry.url)).catch(() => {});
        }
    }
    return cache.get(ref.storage);
}

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
