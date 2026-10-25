// inbox.js — Inbox (#/inbox): unread channels, direct messages and groups,
// and the unread mentions among them, newest first. Each row opens its
// conversation (a mention at the message); "Mark all read" clears them here
// and on the account's other devices.
import { app, markRead } from "../app.js";
import { buildInbox } from "../core/inbox.js";
import { excerpt } from "../core/search.js";
import { t } from "../i18n.js";
import { avatar, clock, h } from "./dom.js";
import { header } from "./common.js";
import { headerActions } from "./message-view.js";
import { savedLink } from "./tool-links.js";
import { jumpAfterOpen } from "./chat-search.js";
import { conversations } from "./tool-sources.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

function inboxData(session) {
    return buildInbox({
        unread: app.unread,
        conversations: conversations(session).map(c => ({ ...c, messages: c.entries })),
    });
}

/** Total unread conversations, for a badge on Home. */
export function inboxCount(session = app.session) {
    return session ? inboxData(session).items.length : 0;
}

export function renderInbox(root) {
    const session = app.session;
    const body = h("div", { class: "screen-body" });
    const markAll = h("button", { class: "btn header-text-btn", type: "button" }, t("inbox.markAll"));

    function render() {
        const { items, mentions } = inboxData(session);
        markAll.disabled = !items.length;
        const rows = items.map(item => h("li", {}, h("a", { class: "row tool-row", href: item.href },
            item.kind === "dm" ? avatar(item.title, "md") : h("span", { class: "hash" }, item.kind === "group" ? "◇" : "#"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, item.title),
                h("span", { class: "row-sub" }, item.last ? `${item.last.from}: ${excerpt(item.last.text, "", 60)}` : t("inbox.newMessages"))),
            h("span", { class: "row-main", style: "flex:0 0 auto;align-items:flex-end;gap:4px" },
                item.last ? h("span", { class: "row-meta" }, clock(item.last.at)) : null,
                h("span", { class: item.mentions ? "badge badge-mention" : "badge", "aria-label": t("home.unread", { count: item.count }) }, item.mentions ? `@${item.count}` : String(item.count))))));
        const mentionRows = mentions.map(({ tag, title, href, message }) => h("li", {}, h("a", { class: "row tool-row", href, onclick: () => jumpAfterOpen(tag, message.id) },
            avatar(message.from, "sm"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, t("inbox.mentionIn", { from: message.from, conversation: title })),
                h("span", { class: "row-sub" }, excerpt(message.text, "", 80))),
            h("span", { class: "row-meta" }, clock(message.at)))));
        body.replaceChildren(
            mentionRows.length ? h("p", { class: "section-label" }, t("inbox.mentions")) : null,
            mentionRows.length ? h("ul", { class: "list", "aria-label": t("inbox.mentions") }, mentionRows) : null,
            h("p", { class: "section-label" }, t("inbox.unread")),
            rows.length ? h("ul", { class: "list", "aria-label": t("inbox.unread") }, rows) : h("p", { class: "hint" }, t("inbox.empty")));
    }

    markAll.onclick = () => {
        for (const tag of [...app.unread.keys()]) markRead(tag);
        for (const group of app.groups?.list.values() || []) {
            if (!group.unread_count) continue;
            app.groups.markRead(group.id);
            group.unread_count = 0;
        }
        render();
    };
    const events = [[app, "unread"], [app, "dm"], [app, "groups"], [session, "messages"]];
    let timer = 0;
    const later = () => { clearTimeout(timer); timer = setTimeout(render, 100); };
    for (const [target, type] of events) target.addEventListener(type, later);
    root.replaceChildren(h("section", { class: "screen chat-tools" },
        header(t("inbox.title"), { back: "#/home", action: headerActions(savedLink(), markAll) }),
        body));
    render();
    return () => {
        clearTimeout(timer);
        for (const [target, type] of events) target.removeEventListener(type, later);
    };
}
