// tool-links.js — Home's header links to Search and the Inbox, next to the
// screen's own actions; Saved messages is linked from the Inbox (savedLink)
// so Home's header stays usable at 360 px. The quick switcher of the wide
// layout lists all tool pages. Kept apart from home.js so the Home chunk
// does not load the tools themselves.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { addSwitcherSource } from "./layout.js";
import { headerActions } from "./message-view.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

export function toolLinks(...extra) {
    let unread = 0;
    for (const entry of app.unread.values()) if (entry.count) unread++;
    for (const group of app.groups?.list.values() || []) if (group.unread_count && !app.unread.has(`conversation:${group.id}`)) unread++;
    const inbox = h("a", { class: "icon-btn thread-badge", href: "#/inbox", "aria-label": unread ? t("inbox.titleCount", { count: unread }) : t("inbox.title") },
        icon("inbox"), unread ? h("span", { class: "badge" }, String(unread)) : null);
    return headerActions(
        h("a", { class: "icon-btn", href: "#/search", "aria-label": t("search.title") }, icon("search")),
        inbox,
        ...extra);
}

/** The Inbox header's link to Saved messages. */
export function savedLink() {
    return h("a", { class: "icon-btn", href: "#/saved", "aria-label": t("saved.title") }, icon("bookmark"));
}

// Tool pages in the wide layout's quick switcher (Ctrl/Cmd+K).
addSwitcherSource(() => [
    ["#/search", t("search.title"), ["find", "suchen"]],
    ["#/inbox", t("inbox.title"), ["unread", "ungelesen"]],
    ["#/saved", t("saved.title"), ["bookmarks", "gespeichert"]],
    ["#/activity", t("activity.title"), ["mentions", "pokes", "erwähnungen"]],
].map(([hash, label, keywords]) => ({ kind: "page", typed: true, label, keywords, run: () => { location.hash = hash; } })));
