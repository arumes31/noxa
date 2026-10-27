// link-preview.js — the preview card under a message's first link. The
// server fetches the page (it never hands out remote URLs: thumbnails are
// data URLs it re-encoded), so the phone never contacts the linked site.
// Failures show nothing.
import { previewRequest, safeURL } from "../core/message-format.js";
import * as settings from "../core/settings.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import "./rich-strings.js";

const results = new Map(); // url -> result, or null after a failure
const collapsed = new Set(); // urls the user folded away

export function previewsEnabled(session, conversation) {
    return conversation === "channel" && !!session?.capabilities?.has("server_link_previews_v1") && !!settings.get("linkPreviews");
}

function details(result) {
    const out = [];
    if (result.live_state === "live" || result.live_state === "offline") out.push(t(`rich.preview.${result.live_state}`));
    if (result.live_state === "scheduled" && result.starts_at && Number.isFinite(Date.parse(result.starts_at))) {
        out.push(t("rich.preview.scheduled", { time: new Date(result.starts_at).toLocaleString() }));
    }
    if (result.category) out.push(t("rich.preview.category", { category: String(result.category).slice(0, 100) }));
    return out;
}

function fill(card, url, result) {
    const text = (result.description || result.text || "").trim();
    if (!result.title && !text && !result.thumbnail) {
        card.remove();
        return;
    }
    let host = "";
    try { host = new URL(url).hostname.replace(/^www\./, ""); } catch { /* not shown */ }
    const body = h("div", { class: "lp-body", id: `lp-${Math.random().toString(36).slice(2)}` },
        typeof result.thumbnail === "string" && result.thumbnail.length <= 280000 && /^data:image\/jpeg;base64,[A-Za-z0-9+/]+={0,2}$/.test(result.thumbnail) ? h("img", { class: "lp-thumb", src: result.thumbnail, alt: "", loading: "lazy" }) : null,
        h("div", { class: "lp-text" },
            result.title ? h("a", { class: "lp-title", href: safeURL(url) || "#", target: "_blank", rel: "noopener noreferrer" }, result.title.slice(0, 300)) : null,
            result.author ? h("span", { class: "lp-author" }, result.author.slice(0, 200)) : null,
            text ? h("span", { class: "lp-desc" }, text.slice(0, 500)) : null,
            details(result).map(line => h("span", { class: "lp-detail", dataset: { state: result.live_state || "" } }, line))));
    const toggle = h("button", { class: "lp-toggle", type: "button", "aria-controls": body.id });
    const update = () => {
        const folded = collapsed.has(url);
        body.hidden = folded;
        toggle.setAttribute("aria-expanded", String(!folded));
        toggle.setAttribute("aria-label", folded ? t("rich.preview.expand") : t("rich.preview.collapse"));
        toggle.textContent = folded ? "▸" : "▾";
    };
    toggle.onclick = () => {
        collapsed.has(url) ? collapsed.delete(url) : collapsed.add(url);
        update();
    };
    update();
    card.replaceChildren(h("div", { class: "lp-head" }, h("span", { class: "lp-site" }, host), toggle), body);
    card.hidden = false;
}

/** A card element that fills itself, or null when previews are off. */
export function linkPreview(session, url, conversation) {
    const request = previewRequest(url);
    if (!request || !previewsEnabled(session, conversation)) return null;
    if (results.has(url) && !results.get(url)) return null;
    const card = h("div", { class: "link-preview", role: "group", "aria-label": t("rich.preview.label"), hidden: true });
    if (results.has(url)) {
        fill(card, url, results.get(url));
        return card;
    }
    session.linkPreview(request.provider, request.resource, conversation).then(result => {
        results.set(url, result || null);
        if (result) fill(card, url, result);
        else card.remove();
    }, () => {
        results.set(url, null);
        card.remove();
    });
    return card;
}
