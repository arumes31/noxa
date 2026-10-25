// attachments.js — message bodies with encrypted attachments: text plus a
// chip per [file:…] token. Images open inline on tap; other files download.
import { fetchAttachment, parseFileTokens } from "../core/files.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { audioMime } from "../core/voice-notes.js";
import { voiceNotePlayer } from "./voice-note.js";

const IMAGE = /\.(png|jpe?g|gif|webp|avif)$/i;
const MIME = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", avif: "image/avif" };

function formatSize(bytes) {
    if (!bytes) return "";
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function chip(ref, { conn, channelID }) {
    // Voice messages (and other audio) play inline.
    const mime = audioMime(ref.name);
    if (mime) return voiceNotePlayer(() => fetchAttachment(conn, channelID, ref).then(plain => new Blob([plain], { type: mime })), ref.name, ref);
    const isImage = IMAGE.test(ref.name);
    const label = h("span", { class: "file-name" }, ref.name);
    const size = h("span", { class: "file-size" }, formatSize(ref.size));
    const node = h("button", { class: "file-chip", type: "button", "aria-label": t(isImage ? "file.show" : "file.download", { name: ref.name }) },
        h("span", { class: "file-icon", "aria-hidden": "true" }, isImage ? "🖼" : "📄"), label, size);
    node.onclick = async () => {
        node.disabled = true;
        size.textContent = t("file.loading");
        try {
            const plain = await fetchAttachment(conn, channelID, ref);
            const ext = ref.name.split(".").pop().toLowerCase();
            const url = URL.createObjectURL(new Blob([plain], { type: MIME[ext] || "application/octet-stream" }));
            if (isImage) {
                node.replaceWith(h("img", { class: "file-image", src: url, alt: ref.name }));
            } else {
                const link = h("a", { href: url, download: ref.name });
                document.body.append(link);
                link.click();
                link.remove();
                setTimeout(() => URL.revokeObjectURL(url), 60000);
                size.textContent = formatSize(ref.size);
                node.disabled = false;
            }
        } catch (error) {
            size.textContent = formatSize(ref.size);
            node.disabled = false;
            toast(t("common.error", { error: error.message }), "warn");
        }
    };
    return node;
}

/** Text nodes and attachment chips for a message body. */
export function messageBody(text, context) {
    const refs = parseFileTokens(text);
    if (!refs.length) return [text];
    const out = [];
    let rest = text;
    for (const ref of refs) {
        const at = rest.indexOf(ref.token);
        const before = rest.slice(0, at).trim();
        if (before) out.push(before);
        out.push(chip(ref, context));
        rest = rest.slice(at + ref.token.length);
    }
    if (rest.trim()) out.push(rest.trim());
    return out;
}
