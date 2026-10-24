// admin-entry.js — the ways into the Admin area from other screens: the You
// row, the channel options panel's settings row and the Servers "Add channel"
// button. Each shows only when the server would accept the user there:
// owners and administrators at once, others after a probe succeeds.
import { adminFor, canManageRoles } from "../core/admin.js";
import { addStrings, t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import "./admin.css";

addStrings({
    en: { "entry.admin": "Administration", "entry.adminSub": "Roles, bans, channels and server settings", "entry.channel": "Settings of {name}", "entry.addChannel": "Add channel" },
    de: { "entry.admin": "Verwaltung", "entry.adminSub": "Rollen, Sperren, Kanäle und Servereinstellungen", "entry.channel": "Einstellungen von {name}", "entry.addChannel": "Kanal hinzufügen" },
});

const probes = new WeakMap();

/** Caches one probe per session and key. */
function probe(session, key, run) {
    if (!probes.has(session)) probes.set(session, new Map());
    const cache = probes.get(session);
    if (!cache.has(key)) cache.set(key, run().then(() => true, () => false));
    return cache.get(key);
}

/** Reveals node once allowed resolves true. */
function reveal(node, allowed) {
    node.hidden = true;
    allowed.then(ok => { node.hidden = !ok; });
    return node;
}

/** The You screen's "Administration" row. */
export function adminEntry(session) {
    const row = h("a", { class: "card row admin-link", href: "#/admin" }, icon("shield"),
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("entry.admin")), h("span", { class: "row-sub" }, t("entry.adminSub"))),
        h("span", { class: "admin-chevron", "aria-hidden": "true" }, "›"));
    const managesChannels = session.channels().some(c => c.canManageAccess);
    return reveal(row, managesChannels || session.snapshot?.can_view_audit_log ? Promise.resolve(true) : canManageRoles(session));
}

/** The "Channel settings" row of the channel options panel (channel-menu.js). */
export function channelSettingsRow(session, channelID, onclick) {
    const channel = session?.channels().find(c => c.id === channelID);
    if (!channel || channel.home) return null;
    const row = h("a", { class: "row", href: `#/admin/channel/${channelID}`, onclick },
        h("span", { class: "hash", "aria-hidden": "true" }, icon("shield")),
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("entry.channel", { name: channel.name }))));
    return reveal(row, canEditChannel(session, channelID));
}

/** Whether the server would let this session edit the channel. */
function canEditChannel(session, channelID) {
    const channel = session.channels().find(c => c.id === channelID);
    return session.isAdmin || channel?.canManageAccess ? Promise.resolve(true)
        : probe(session, `edit:${channelID}`, () => adminFor(session).channelState("channel_edit", channelID));
}

/** The Servers screen's "Add channel" button. */
export function addChannelButton(session) {
    const button = h("a", { class: "btn btn-block admin-add-channel", href: "#/admin/channel-new/0" }, icon("plus"), t("entry.addChannel"));
    return reveal(button, session.isAdmin ? Promise.resolve(true) : probe(session, "create:0", () => adminFor(session).channelState("channel_create", 0)));
}
