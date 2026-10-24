// home.js — Home (02): live voice rooms and recent conversations.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { clock, h } from "./dom.js";
import { header, lastMessage, nav, scopeName, unreadBadge } from "./common.js";

export function renderHome(root) {
    const session = app.session;
    const live = session.channels().filter(c => !c.textOnly && c.clients.length);
    const scopes = session.chatScopes()
        .map(scope => ({ scope, last: lastMessage(session, scope) }))
        .filter(entry => entry.last)
        .sort((a, b) => b.last.sentAt - a.last.sentAt);
    root.replaceChildren(h("section", { class: "screen" },
        header(t("home.title")),
        h("div", { class: "screen-body" },
            live.map(channel => h("a", { class: "card row", href: "#/voice", style: "border-color:var(--border-strong)" },
                h("div", { class: "row-main" },
                    h("span", { class: "row-title" }, t("home.live", { channel: channel.name })),
                    h("span", { class: "row-sub" }, channel.clients.map(c => c.nickname).join(", "))),
                h("span", { class: "badge" }, String(channel.clients.length)))),
            h("p", { class: "section-label" }, t("home.recent")),
            scopes.length ? h("ul", { class: "list" }, scopes.map(({ scope, last }) => h("li", {},
                h("a", { class: "row", href: `#/chat/${scope}` },
                    scope === 0 ? h("span", { class: "hash" }, "◎") : h("span", { class: "hash" }, "#"),
                    h("span", { class: "row-main" },
                        h("span", { class: "row-title" }, scopeName(session, scope).replace(/^# /, "#")),
                        h("span", { class: "row-sub" }, `${last.from}: ${last.text ?? t("chat.locked")}`)),
                    unreadBadge(scope) || h("span", { class: "row-meta" }, clock(last.sentAt))))))
                : h("p", { class: "hint" }, t("home.empty")))),
        nav("home"));
}
