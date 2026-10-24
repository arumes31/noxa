// members.js — everyone online, grouped by channel, with status, roles and
// the mobile badge (#/members/<channelID>). Tapping a member opens their
// card. The given channel's members come first.
import { app } from "../app.js";
import { statusOf } from "../core/presence.js";
import { rolesOf } from "../core/social.js";
import { t } from "../i18n.js";
import { avatar, h } from "./dom.js";
import { header, nav } from "./common.js";
import { statusDot, statusLine } from "./presence-ui.js";
import "./social-strings.js";
import "./social.css";

function memberRow(client, session) {
    const roles = rolesOf(client);
    const self = client.client_id === session.clientID;
    const sub = statusLine(client);
    return h("li", {}, h("button", {
        class: "row member-row", type: "button", dataset: { uid: client.unique_id || "" },
        "aria-label": t("members.open", { name: client.nickname }),
        onclick: () => import("./profile.js").then(({ openProfile }) => openProfile({ uid: client.unique_id, clientID: client.client_id, name: client.nickname })),
    },
    h("span", { class: "avatar-wrap" }, avatar(client.nickname, "md", client.unique_id), statusDot(statusOf(client))),
    h("span", { class: "row-main" },
        h("span", { class: "row-title", style: roles[0]?.color ? `color:${roles[0].color}` : null }, client.nickname, self ? ` (${t("dm.you")})` : ""),
        sub ? h("span", { class: "row-sub" }, sub) : null,
        roles.length ? h("span", { class: "role-chips inline" }, roles.slice(0, 3).map(role => h("span", { class: "role-chip", style: role.color ? `--role:${role.color}` : null }, role.name))) : null),
    client.on_mobile ? h("span", { class: "row-meta", title: t("presence.onMobile") }, t("presence.mobileBadge")) : null));
}

export function renderMembers(root, channelID = 0) {
    const session = app.session;
    let query = "";
    const list = h("div", { class: "member-groups" });
    const search = h("input", { class: "input members-search", type: "search", placeholder: t("members.search"), "aria-label": t("members.search"), oninput: () => { query = search.value.trim().toLowerCase(); render(); } });
    const channelName = channelID ? session.channels().find(c => c.id === channelID)?.name : "";
    const render = () => {
        const match = client => !query || (client.nickname || "").toLowerCase().includes(query);
        const groups = session.channels().map(c => ({ id: c.id, name: c.name, clients: c.clients.filter(match) })).filter(g => g.clients.length);
        groups.sort((a, b) => (b.id === channelID) - (a.id === channelID));
        const unassigned = (session.snapshot?.unassigned_clients || []).filter(match);
        if (unassigned.length) groups.push({ id: -1, name: t("members.noChannel"), clients: unassigned });
        const total = new Set(session.clients().map(c => c.unique_id || c.client_id)).size;
        list.replaceChildren(
            h("p", { class: "hint", role: "status" }, t("members.online", { count: total })),
            ...(groups.length ? groups.map(group => h("section", { class: "member-group", "aria-label": group.name },
                h("p", { class: "section-label" }, group.id === channelID && channelID ? t("members.here", { channel: group.name }) : group.name, ` · ${group.clients.length}`),
                h("ul", { class: "list" }, group.clients.map(client => memberRow(client, session)))))
                : [h("p", { class: "hint" }, t("members.none"))]));
    };
    root.replaceChildren(h("section", { class: "screen" },
        header(t("members.title"), { back: channelID ? `#/chat/${channelID}` : "#/servers", sub: channelName ? `#${channelName}` : "" }),
        h("div", { class: "screen-body" }, search, list),
        nav("servers")));
    render();
    // Keep the list live without re-routing (which would reset the search).
    const update = () => { if (app.session === session) render(); };
    session.addEventListener("snapshot", update);
    return () => session.removeEventListener("snapshot", update);
}
