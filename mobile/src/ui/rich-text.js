// rich-text.js — a message body as DOM: attachment tokens first, then the
// chat's mini-markdown (core/markdown.js) on the text between them. Every
// string lands in textContent or an attribute; nothing is parsed as HTML.
// Links only ever point at http(s) URLs.
import { parseFileTokens } from "../core/files.js";
import { parseMarkdown } from "../core/markdown.js";
import { splitRoleMentions, visibleRoles } from "../core/mentions.js";
import { displayURL, previewLink, safeURL, splitFileTokens } from "../core/message-format.js";
import { t } from "../i18n.js";
import { attachmentNode } from "./attachments.js";
import { h, sheet, toast } from "./dom.js";
import { linkPreview } from "./link-preview.js";
import "./rich-strings.js";
import "./rich-text.css";

// --- Custom server emoji ---------------------------------------------------------

const customNames = new WeakMap(); // session -> Set of names

/** The custom emoji names known for a session (empty until loaded). */
export function customEmoji(session) {
    return session ? customNames.get(session) || new Set() : new Set();
}

/**
 * Loads the server's custom emoji names and calls onChange when they differ
 * from what was known (first load, or after an emoji_* event). Returns stop().
 */
export function watchCustomEmoji(session, onChange) {
    if (!session) return () => {};
    const load = () => session.emojiList().then(list => {
        const next = new Set(list.map(e => e.name));
        const known = customNames.get(session);
        if (known && known.size === next.size && [...next].every(n => known.has(n))) return;
        customNames.set(session, next);
        onChange();
    }).catch(() => {});
    load();
    session.addEventListener("emoji", load);
    return () => session.removeEventListener("emoji", load);
}

let emojiObserver = null;
function customEmojiNode(name, session) {
    const img = h("img", { class: "custom-emoji", alt: `:${name}:`, title: `:${name}:`, width: "20", height: "20", decoding: "async" });
    if (!session) return img;
    // Loaded when on screen: a long history may name many emoji.
    const load = () => session.emojiImage(name).then(url => { if (url.startsWith("data:image/")) img.src = url; }).catch(() => {});
    if (typeof IntersectionObserver !== "function") { load(); return img; }
    emojiObserver ||= new IntersectionObserver(entries => {
        for (const entry of entries) {
            if (!entry.isIntersecting) continue;
            emojiObserver.unobserve(entry.target);
            entry.target.noxaLoad?.();
        }
    }, { rootMargin: "200px 0px" });
    img.noxaLoad = load;
    emojiObserver.observe(img);
    return img;
}

// --- Markdown nodes ------------------------------------------------------------

async function showFullLink(url) {
    const choice = await sheet(t("rich.link"), url, [
        { label: t("rich.openLink"), value: "open", primary: true },
        { label: t("rich.copyLink"), value: "copy" },
        { label: t("common.close"), value: null },
    ]);
    if (choice === "open") window.open(url, "_blank", "noopener,noreferrer");
    if (choice === "copy") navigator.clipboard?.writeText(url).then(() => toast(t("rich.copied")), () => {});
}

function linkNode(url) {
    const href = safeURL(url);
    if (!href) return document.createTextNode(url);
    const text = displayURL(url);
    const link = h("a", { class: "md-link", href, target: "_blank", rel: "noopener noreferrer", title: url }, text);
    // Shortened links show the whole address on long press.
    if (text !== url) {
        link.setAttribute("aria-label", url);
        link.addEventListener("contextmenu", event => { event.preventDefault(); showFullLink(url); });
    }
    return link;
}

const WRAP = { strong: "strong", em: "em", u: "u", del: "s" };

/** DOM nodes for parsed markdown nodes. */
export function renderNodes(nodes, context = {}) {
    return nodes.map(node => renderNode(node, context));
}

/** Text with "<@&id>" role mentions shown as @RoleName in the role's colour (snapshot role chips). */
function roleMentionText(text, context) {
    const roles = new Map(visibleRoles(context.session?.clients?.() || []).map(role => [role.id, role]));
    const out = document.createDocumentFragment();
    for (const part of splitRoleMentions(text)) {
        if (part.type === "text") { out.append(part.text); continue; }
        const role = roles.get(part.id);
        const color = /^#[0-9a-f]{3,8}$/i.test(role?.color || "") ? role.color : "";
        out.append(h("span", { class: "md-mention role", dataset: { role: String(part.id) }, style: color ? `color:${color}` : null }, `@${role?.name || t("rich.role")}`));
    }
    return out;
}

function renderNode(node, context) {
    switch (node.type) {
        case "text": return node.text.includes("<@&") ? roleMentionText(node.text, context) : document.createTextNode(node.text);
        case "br": return h("br");
        case "inline-code": return h("code", { class: "md-code" }, node.code);
        case "code": return h("figure", { class: "md-pre" },
            node.language ? h("figcaption", { class: "md-lang" }, node.language) : null,
            h("pre", { tabindex: "0" }, h("code", {}, node.code)));
        case "link": return linkNode(node.url);
        case "emoji": return h("span", { class: "emoji", title: `:${node.name}:` }, node.char);
        case "custom-emoji": return customEmojiNode(node.name, context.session);
        case "mention": return h("span", { class: node.everyone ? "md-mention everyone" : "md-mention", dataset: { uid: node.uid || "" } }, `@${node.name}`);
        case "table": return h("div", { class: "md-table-wrap", tabindex: "0", role: "region", "aria-label": t("rich.table") },
            h("table", { class: "md-table" },
                h("thead", {}, h("tr", {}, node.header.map(cell => h("th", { scope: "col" }, renderNodes(cell, context))))),
                h("tbody", {}, node.rows.map(row => h("tr", {}, row.map(cell => h("td", {}, renderNodes(cell, context))))))));
        default:
            if (WRAP[node.type]) return h(WRAP[node.type], {}, renderNodes(node.children || [], context));
            return document.createTextNode(node.text || "");
    }
}

// --- Message bodies -----------------------------------------------------------

/**
 * The body of a message: markdown, attachments and (for the first link) a
 * preview card. context: {session, conn, channelID, preview}.
 */
export function richBody(text, context = {}) {
    const options = { customEmoji: customEmoji(context.session) };
    const segments = splitFileTokens(text, parseFileTokens(text));
    const out = h("span", { class: "rich" });
    const textNodes = [];
    for (const segment of segments) {
        if (segment.type === "file") {
            out.append(attachmentNode(segment.ref, context));
            continue;
        }
        const nodes = parseMarkdown(segment.text, options);
        textNodes.push(...nodes);
        out.append(...renderNodes(nodes, context));
    }
    const url = context.session && context.preview !== false ? previewLink(textNodes) : null;
    if (url) {
        const card = linkPreview(context.session, url);
        if (card) out.append(card);
    }
    return out;
}

/** Compatibility with the old attachments.messageBody: an array of nodes. */
export function messageBody(text, context) {
    return [richBody(text, context)];
}
