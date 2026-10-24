// servers.js — Server & channels (03): text and voice tree with members.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav, unreadBadge } from "./common.js";
import { memberMenuButton } from "./moderation.js";
import { addChannelButton } from "./admin-entry.js";
import { joinWithPassword, lockBadge, serverMenuButton } from "./server-tools.js";
import { statusOf } from "../core/presence.js";
import * as settings from "../core/settings.js";
import { channelWatchSheet } from "./notify-settings.js";
import { statusDot, statusLine } from "./presence-ui.js";

const openProfile = options => import("./profile.js").then(module => module.openProfile(options));

/** Bell of a voice channel: notify when it reaches a member count. */
function watchButton(channel) {
    const count = Number(settings.get("channelWatch")?.[String(channel.id)]) || 0;
    return h("button", { class: "icon-btn watch-btn", type: "button", "aria-pressed": String(count > 0),
        "aria-label": count ? `${t("watch.action", { channel: channel.name })} · ${t("watch.active", { count })}` : t("watch.action", { channel: channel.name }),
        onclick: () => channelWatchSheet(channel) }, icon("bell"));
}

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
            if ((!app.voice?.active || app.voice.channelID !== channel.id) && !await joinWithPassword(channel)) return;
            if (app.session === session && location.hash === before) location.hash = "#/voice";
        } catch (error) {
            if (app.session === session) toast(t("voice.joinFailed", { error: error.message }), "warn");
        } finally { joining = false; button.disabled = false; root.querySelectorAll(".server-join").forEach(control => { control.disabled = false; }); }
    } }, t("voice.joinAction"));
    const channels = session.channels().map(c => ({ ...c, clients: c.home ? c.clients.filter(member => !member.on_mobile) : c.clients }));
    const online = new Set(session.clients().map(c => c.user_id > 0 ? `user:${c.user_id}` : c.unique_id || c.client_id)).size;
    root.replaceChildren(h("section", { class: "screen" },
        header(app.info?.server_name || "noXa", { sub: `${online} online`, action: h("span", { class: "header-actions" },
            h("a", { class: "icon-btn", href: "#/members/0", "aria-label": t("members.title") }, icon("users")), serverMenuButton(session)) }),
        h("div", { class: "screen-body" },
            h("p", { class: "section-label" }, t("channels.text")),
            h("ul", { class: "list" },
                session.current.has(0) ? h("li", {}, h("a", { class: "row", href: "#/chat/0" }, h("span", { class: "hash" }, "◎"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("channels.global"))), unreadBadge("channel:0"))) : null,
                channels.filter(c => c.chat).map(c => h("li", {}, h("a", { class: "row", href: `#/chat/${c.id}`, style: `padding-left:${10 + c.depth * 14}px` },
                    h("span", { class: "hash" }, c.restricted ? "🔒" : "#"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name), c.topic ? h("span", { class: "row-sub" }, c.topic) : null),
                    lockBadge(c),
                    unreadBadge(`channel:${c.id}`))))),
            h("p", { class: "section-label" }, t("channels.voice")),
            h("ul", { class: "list" }, channels.filter(c => !c.textOnly).map(c => h("li", {},
                h("div", { class: "row", style: `padding-left:${10 + c.depth * 14}px` },
                    icon(c.home ? "home" : "voice"),
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name)),
                    lockBadge(c),
                    c.clients.length ? h("span", { class: "row-meta" }, String(c.clients.length)) : null,
                    c.home ? null : watchButton(c),
                    c.home ? null : joinButton(c)),
                c.clients.length ? h("ul", { class: "list", style: "padding-left:44px" }, c.clients.map(client => memberRow(client,
                    h("button", {
                        class: "row member-row", type: "button", style: "min-height:44px",
                        "aria-label": t("members.open", { name: client.nickname }),
                        onclick: () => openProfile({ uid: client.unique_id, clientID: client.client_id, name: client.nickname }),
                    }, h("span", { class: "avatar-wrap" }, avatar(client.nickname, "sm", client.unique_id), statusDot(statusOf(client))),
                    h("span", { class: "row-main" }, h("span", { class: "row-sub" }, client.nickname), statusLine(client) ? h("span", { class: "row-sub member-status" }, statusLine(client)) : null),
                    client.on_mobile ? h("span", { class: "row-meta", title: t("presence.onMobile") }, t("presence.mobileBadge")) : null)))) : null))),
            addChannelButton(session))),
        nav("servers"));
    // Other members get a ⋯ menu (also on long-press) with moderation and reporting.
    function memberRow(client, row) {
        return h("li", { class: "member-row" }, row, client.client_id !== session.clientID ? memberMenuButton(client, row) : null);
    }
}
