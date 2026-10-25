import { escapeHTML } from "./markdown.js";
import { isSafeImageDataURL } from "./safe-media.js";
import { splitNameMeta } from "./attachment-meta.js";

// parseFileRef mirrors client/chat_attachments.go. Zero or one separator remains a plain
// legacy reference so historical messages stay readable; only two separators
// carry an attachment storage key and display name. A sealed reference may
// carry a metadata segment before the name (attachment-meta.js); meta is null
// when it does not, and the name never includes it.
export function parseFileRef(capture) {
    if (typeof capture !== "string") {
        return { storage: "", key: "", name: "", valid: false, legacy: false, meta: null };
    }
    const first = capture.indexOf("#");
    if (first < 0) return { storage: capture, key: "", name: capture, valid: true, legacy: true, meta: null };
    const second = capture.indexOf("#", first + 1);
    if (second < 0) return { storage: capture, key: "", name: capture, valid: true, legacy: true, meta: null };
    const key = capture.slice(first + 1, second);
    // Like the Go parser, only a keyed (sealed) reference has a segment.
    const { name, meta } = key ? splitNameMeta(capture.slice(second + 1)) : { name: capture.slice(second + 1), meta: null };
    return {
        storage: capture.slice(0, first),
        key,
        name,
        valid: true,
        legacy: false,
        meta,
    };
}

function emojiURL(cache, name) {
    if (cache instanceof Map) return cache.get(name);
    return cache?.[name];
}

// transformCustomEmoji only changes text nodes in already-safe markdown HTML.
// The URL is independently validated so a compromised emoji response cannot
// turn the renderer into an arbitrary-URL or markup injection sink.
// onMissing (optional) is told once about every referenced emoji whose image
// is still loading ("" in the cache) or was never requested (absent); such
// tokens become a pending placeholder the renderer can swap once the image
// arrives. Rejected URLs stay plain text and are not reported.
export function transformCustomEmoji(html, emojis, cache, onMissing) {
    let output = typeof html === "string" ? html : "";
    if (!Array.isArray(emojis) || emojis.length === 0) return output;
    if (output.indexOf(":") === -1) return output;

    const foundEmojis = [];
    for (const entry of emojis) {
        const name = typeof entry === "string" ? entry : entry?.name;
        if (typeof name !== "string" || name === "") continue;
        const token = `:${name}:`;
        if (output.includes(token)) {
            foundEmojis.push({name, token});
        }
    }

    if (foundEmojis.length === 0) return output;

    for (const {name, token} of foundEmojis) {
        const url = emojiURL(cache, name);
        const pending = url === "" || url === undefined;
        if (!pending && !isSafeImageDataURL(url)) continue;
        const image = pending
            ? `<span class="md-emoji-pending" data-emoji="${escapeHTML(name)}">:${escapeHTML(name)}:</span>`
            : `<img class="md-emoji" src="${escapeHTML(url)}" alt=":${escapeHTML(name)}:">`;
        let replaced = false;
        const parts = output.split(/(<[^>]*>)/g);
        let literalDepth = 0;
        for (let index = 0; index < parts.length; index++) {
            const part = parts[index];
            if (part.startsWith("<")) {
                const tag = part.match(/^<\s*(\/?)\s*(code|pre)\b/i);
                if (tag) {
                    if (tag[1]) literalDepth = Math.max(0, literalDepth - 1);
                    else if (!/\/\s*>$/.test(part)) literalDepth++;
                }
            } else if (literalDepth === 0) {
                if (parts[index].includes(token)) {
                    parts[index] = part.split(token).join(image);
                    replaced = true;
                }
            }
        }
        output = parts.join("");
        if (pending && replaced && typeof onMissing === "function") onMissing(name);
    }
    return output;
}
