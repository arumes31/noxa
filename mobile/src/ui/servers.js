// servers.js — Server & channels (03): text and voice tree with members.
import { app, joinVoice } from "../app.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { dmHref, header, nav, unreadBadge } from "./common.js";
import { filesButton } from "./files-link.js";
import { pingBadge } from "./ping-badge.js";

let joining = false;

export function renderServers(root) {
    const session = app.session;
    const joinButton = channel => h("button", { class: "btn server-join", type: "button", disabled: joining, "aria-label": t("voice.joinNamed", { name: channel.name }), onclick: async event => {
        if (joining || app.session !== session) return;
        joining = true;
        const before = location.hash;
        const button = event.currentTarget;
        button.disabled = true;
        try {
            if (!app.voice?.active || app.voice.channelID !== channel.id) await joinVoice(channel.id);
            if (app.session === session && location.hash === before) location.hash = "#/voice";
        } catch (error) {
            if (app.session === session) toast(t("voice.joinFailed", { error: error.message }), "warn");
        } finally { joining = false; button.disabled = false; root.querySelectorAll(".server-join").forEach(control => { control.disabled = false; }); }
    } }, t("voice.joinAction"));
    const channels = session.channels().map(c => ({ ...c, clients: c.home ? c.clients.filter(member => !member.on_mobile) : c.clients }));
    const online = new Set(session.clients().map(c => c.user_id > 0 ? `user:${c.user_id}` : c.unique_id || c.client_id)).size;
    root.replaceChildren(h("section", { class: "screen" },
        header(app.info?.server_name || "noXa", { sub: `${online} online`, action: pingBadge() }),
        h("div", { class: "screen-body" },
            h("p", { class: "section-label" }, t("channels.text")),
            h("ul", { class: "list" },
                session.current.has(0) ? h("li", { class: "server-text-row" }, h("a", { class: "row", href: "#/chat/0" }, h("span", { class: "hash" }, "◎"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("channels.global"))), unreadBadge("channel:0")), filesButton(0, t("channels.global"))) : null,
                channels.filter(c => c.chat).map(c => h("li", { class: "server-text-row" }, h("a", { class: "row", href: `#/chat/${c.id}`, style: `padding-left:${10 + c.depth * 14}px` },
                    h("span", { class: "hash" }, c.restricted ? "🔒" : "#"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name), c.topic ? h("span", { class: "row-sub" }, c.topic) : null),
                    unreadBadge(`channel:${c.id}`)), filesButton(c.id, c.name)))),
            h("p", { class: "section-label" }, t("channels.voice")),
            h("ul", { class: "list" }, channels.filter(c => !c.textOnly).map(c => h("li", {},
                h("div", { class: "row", style: `padding-left:${10 + c.depth * 14}px` },
                    icon(c.home ? "home" : "voice"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name)),
                    c.clients.length ? h("span", { class: "row-meta" }, String(c.clients.length)) : null,
                    c.home ? null : joinButton(c)),
                c.clients.length ? h("ul", { class: "list", style: "padding-left:44px" }, c.clients.map(client => h("li", {},
                    h(client.unique_id && client.unique_id !== session.uniqueID ? "a" : "div", {
                        class: "row", style: "min-height:36px",
                        href: client.unique_id && client.unique_id !== session.uniqueID ? dmHref(client.unique_id) : null,
                        "aria-label": client.unique_id !== session.uniqueID ? t("dm.messageTo", { name: client.nickname }) : null,
                    }, avatar(client.nickname, "sm"), h("span", { class: "row-sub" }, client.nickname), client.on_mobile ? h("span", { class: "row-meta", title: t("presence.onMobile") }, t("presence.mobileBadge")) : null)))) : null))))),
        nav("servers"));
}
