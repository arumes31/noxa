// admin.js — the Admin area (#/admin/<view>/<id>): a hub of moderation and
// server administration, one task per screen. Reached from You (owners,
// administrators and role managers) and from channel and member menus.
import { app } from "../app.js";
import { adminFor } from "../core/admin.js";
import { addStrings, t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { adminScreen, linkRow } from "./admin-kit.js";
import { renderMembers, renderRoleEditor, renderRoles } from "./admin-roles.js";
import { renderAccess, renderChannelAdmin, renderChannelForm, renderInvites, renderWebhooks } from "./admin-channels.js";
import { renderAudit, renderBans, renderComplaints, renderEmoji, renderFilters, renderServerSettings } from "./admin-server.js";

addStrings({
    en: {
        "admin.title": "Administration",
        "admin.people": "Members",
        "admin.roles": "Roles",
        "admin.rolesSub": "Names, colours and permissions",
        "admin.members": "Members and roles",
        "admin.membersSub": "Search members, assign roles",
        "admin.bans": "Ban list",
        "admin.bansSub": "Lift bans",
        "admin.complaints": "Complaints",
        "admin.complaintsSub": "Reports filed by members",
        "admin.channels": "Channels",
        "admin.addChannel": "Add channel",
        "admin.server": "Server",
        "admin.audit": "Audit log",
        "admin.auditSub": "Who changed what",
        "admin.filters": "Chat filters",
        "admin.filtersSub": "Blocked words and links",
        "admin.emoji": "Custom emoji",
        "admin.emojiSub": "Upload, rename, delete",
        "admin.settings": "Server settings",
        "admin.settingsSub": "Icon, banner, limits and voice defaults",
        "admin.textChannel": "Text channel",
        "admin.voiceChannel": "Voice channel",
    },
    de: {
        "admin.title": "Verwaltung",
        "admin.people": "Mitglieder",
        "admin.roles": "Rollen",
        "admin.rolesSub": "Namen, Farben und Berechtigungen",
        "admin.members": "Mitglieder und Rollen",
        "admin.membersSub": "Mitglieder suchen, Rollen zuweisen",
        "admin.bans": "Sperrliste",
        "admin.bansSub": "Sperren aufheben",
        "admin.complaints": "Beschwerden",
        "admin.complaintsSub": "Meldungen von Mitgliedern",
        "admin.channels": "Kanäle",
        "admin.addChannel": "Kanal hinzufügen",
        "admin.server": "Server",
        "admin.audit": "Prüfprotokoll",
        "admin.auditSub": "Wer hat was geändert",
        "admin.filters": "Chatfilter",
        "admin.filtersSub": "Gesperrte Wörter und Links",
        "admin.emoji": "Eigene Emoji",
        "admin.emojiSub": "Hochladen, umbenennen, löschen",
        "admin.settings": "Servereinstellungen",
        "admin.settingsSub": "Symbol, Banner, Limits und Sprachvorgaben",
        "admin.textChannel": "Textkanal",
        "admin.voiceChannel": "Sprachkanal",
    },
});

function renderHub(root, session) {
    const channels = session.channels();
    adminScreen(root, t("admin.title"), { back: "#/you" },
        h("p", { class: "section-label" }, t("admin.people")),
        h("ul", { class: "list" },
            linkRow("#/admin/roles", t("admin.roles"), t("admin.rolesSub"), { iconNode: icon("shield") }),
            linkRow("#/admin/members", t("admin.members"), t("admin.membersSub"), { iconNode: icon("you") }),
            linkRow("#/admin/bans", t("admin.bans"), t("admin.bansSub"), { iconNode: icon("lock") }),
            linkRow("#/admin/complaints", t("admin.complaints"), t("admin.complaintsSub"), { iconNode: icon("shield") })),
        h("p", { class: "section-label" }, t("admin.channels")),
        h("ul", { class: "list" },
            channels.filter(c => !c.home).map(c => h("li", {}, h("a", { class: "row admin-link", href: `#/admin/channel/${c.id}`, style: `padding-left:${10 + c.depth * 14}px` },
                h("span", { class: "hash" }, c.textOnly ? "#" : icon("voice")),
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name), h("span", { class: "row-sub" }, t(c.textOnly ? "admin.textChannel" : "admin.voiceChannel"))),
                h("span", { class: "admin-chevron", "aria-hidden": "true" }, "›")))),
            h("li", {}, h("a", { class: "row admin-link admin-add", href: "#/admin/channel-new/0" }, icon("plus"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("admin.addChannel")))))),
        h("p", { class: "section-label" }, t("admin.server")),
        h("ul", { class: "list" },
            session.snapshot?.can_view_audit_log || session.isAdmin ? linkRow("#/admin/audit", t("admin.audit"), t("admin.auditSub")) : null,
            linkRow("#/admin/filters", t("admin.filters"), t("admin.filtersSub")),
            linkRow("#/admin/emoji", t("admin.emoji"), t("admin.emojiSub")),
            linkRow("#/admin/server", t("admin.settings"), t("admin.settingsSub"))));
}

/** Renders #/admin/<view>/<id>/<extra>; returns a cleanup function when the screen has one. */
export function renderAdmin(root, arg = "") {
    const session = app.session;
    const admin = adminFor(session);
    const [view = "", id = "", extra = ""] = String(arg).split("/");
    const number = Number(id) || 0;
    const context = { root, session, admin };
    switch (view) {
        case "roles": return renderRoles(context);
        case "role": return renderRoleEditor(context, id, extra);
        case "members": return renderMembers(context);
        case "bans": return renderBans(context);
        case "complaints": return renderComplaints(context);
        case "audit": return renderAudit(context);
        case "filters": return renderFilters(context);
        case "emoji": return renderEmoji(context);
        case "server": return renderServerSettings(context);
        case "channel": return renderChannelAdmin(context, number);
        case "channel-edit": return renderChannelForm(context, "channel_edit", number);
        case "channel-new": return renderChannelForm(context, "channel_create", number);
        case "access": return renderAccess(context, number);
        case "invites": return renderInvites(context, number);
        case "webhooks": return renderWebhooks(context, number);
        default: return renderHub(root, session);
    }
}
