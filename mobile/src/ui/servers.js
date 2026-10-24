// servers.js — Server & channels (03): text and voice tree with members.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { avatar, h } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav, unreadBadge } from "./common.js";

export function renderServers(root) {
    const session = app.session;
    const channels = session.channels();
    root.replaceChildren(h("section", { class: "screen" },
        header(app.info?.server_name || "noXa", { sub: `${session.clients().length} online` }),
        h("div", { class: "screen-body" },
            h("p", { class: "section-label" }, t("channels.text")),
            h("ul", { class: "list" },
                session.current.has(0) ? h("li", {}, h("a", { class: "row", href: "#/chat/0" }, h("span", { class: "hash" }, "◎"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("channels.global"))), unreadBadge(0))) : null,
                channels.filter(c => c.chat).map(c => h("li", {}, h("a", { class: "row", href: `#/chat/${c.id}`, style: `padding-left:${10 + c.depth * 14}px` },
                    h("span", { class: "hash" }, c.restricted ? "🔒" : "#"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name), c.topic ? h("span", { class: "row-sub" }, c.topic) : null),
                    unreadBadge(c.id))))),
            h("p", { class: "section-label" }, t("channels.voice")),
            h("ul", { class: "list" }, channels.filter(c => !c.textOnly).map(c => h("li", {},
                h("div", { class: "row", style: `padding-left:${10 + c.depth * 14}px` },
                    icon("voice"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name)),
                    c.clients.length ? h("span", { class: "row-meta" }, String(c.clients.length)) : null),
                c.clients.length ? h("ul", { class: "list", style: "padding-left:44px" }, c.clients.map(client => h("li", { class: "row", style: "min-height:36px" },
                    avatar(client.nickname, "sm"), h("span", { class: "row-sub" }, client.nickname)))) : null))))),
        nav("servers"));
}
