// home.js — Home (02): live voice rooms and recent conversations.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { clock, h } from "./dom.js";
import { dmHref, header, lastMessage, nav, scopeName, unreadBadge } from "./common.js";
import { avatar } from "./dom.js";

export function renderHome(root) {
    const session = app.session;
    const live = session.channels().filter(c => !c.textOnly && c.clients.length);
    const channelRows = session.chatScopes()
        .map(scope => ({ scope, last: lastMessage(session, scope) }))
        .filter(entry => entry.last)
        .map(({ scope, last }) => ({ at: last.sentAt, row: h("a", { class: "row", href: `#/chat/${scope}` },
            scope === 0 ? h("span", { class: "hash" }, "◎") : h("span", { class: "hash" }, "#"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, scopeName(session, scope).replace(/^# /, "#")),
                h("span", { class: "row-sub" }, `${last.from}: ${last.text ?? t("chat.locked")}`)),
            unreadBadge(`channel:${scope}`) || h("span", { class: "row-meta" }, clock(last.sentAt))) }));
    const dmRows = (app.dm?.list() || []).map(conversation => {
        const last = conversation.messages.at(-1);
        const name = conversation.nickname || session.clients().find(c => c.unique_id === conversation.peer)?.nickname || t("dm.someone");
        return { at: last.sentAt, row: h("a", { class: "row", href: dmHref(conversation.peer) },
            avatar(name, "md"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, name),
                h("span", { class: "row-sub" }, `${last.self ? t("dm.you") + ": " : ""}${last.body ?? t("chat.locked")}`)),
            unreadBadge(`dm:${conversation.peer}`) || h("span", { class: "row-meta" }, clock(last.sentAt))) };
    });
    const recent = [...channelRows, ...dmRows].sort((a, b) => b.at - a.at);
    root.replaceChildren(h("section", { class: "screen" },
        header(t("home.title")),
        h("div", { class: "screen-body" },
            live.map(channel => h("a", { class: "card row", href: "#/voice", style: "border-color:var(--border-strong)" },
                h("div", { class: "row-main" },
                    h("span", { class: "row-title" }, t("home.live", { channel: channel.name })),
                    h("span", { class: "row-sub" }, channel.clients.map(c => c.nickname).join(", "))),
                h("span", { class: "badge" }, String(channel.clients.length)))),
            h("p", { class: "section-label" }, t("home.recent")),
            recent.length ? h("ul", { class: "list" }, recent.map(entry => h("li", {}, entry.row)))
                : h("p", { class: "hint" }, t("home.empty")))),
        nav("home"));
}
