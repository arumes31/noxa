// tool-links.js — Home's header links to Search, Inbox and Saved messages,
// next to the screen's own action. Kept apart from home.js so the Home
// chunk does not load the tools themselves.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
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
        h("a", { class: "icon-btn", href: "#/saved", "aria-label": t("saved.title") }, icon("bookmark")),
        ...extra);
}
