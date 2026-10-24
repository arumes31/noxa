// common.js — pieces shared by the signed-in screens: navigation, headers,
// scope names and unread badges.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";

export function nav(active) {
    const item = (route, name, label) => h("a", { href: `#/${route}`, "aria-current": active === route ? "page" : null, "aria-label": label }, icon(name), h("span", {}, label));
    return h("nav", { class: "nav", "aria-label": "Main" },
        item("home", "home", t("nav.home")),
        item("servers", "servers", t("nav.servers")),
        item("voice", "voice", t("nav.voice")),
        item("you", "you", t("nav.you")));
}

export function header(title, { back, sub } = {}) {
    return h("header", { class: "screen-header" },
        back ? h("a", { class: "icon-btn", href: back, "aria-label": t("common.back") }, icon("back")) : h("span"),
        h("h1", {}, title, sub ? h("span", { class: "sub" }, sub) : null),
        h("span"));
}

export function scopeName(session, scope) {
    if (scope === 0) return t("channels.global");
    return `# ${session.channels().find(c => c.id === scope)?.name || scope}`;
}

/** Unread count badge; mentions are highlighted. */
export function unreadBadge(scope) {
    const entry = app.unread.get(scope);
    if (!entry?.count) return null;
    return h("span", { class: entry.mentions ? "badge badge-mention" : "badge", "aria-label": t("home.unread", { count: entry.count }) },
        entry.mentions ? `@${entry.count}` : String(entry.count));
}

export function lastMessage(session, scope) {
    const list = session.messages.get(scope) || [];
    return list[list.length - 1];
}
