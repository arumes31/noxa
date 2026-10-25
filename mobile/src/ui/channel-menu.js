// channel-menu.js — the channel chat menu's channel items: members, the
// channel's notification level and, for those allowed, the channel's admin
// settings. ui/chat.js puts them into its ⋮ menu (message-view.js menuButton).
import { app } from "../app.js";
import { channelLevel } from "../core/notify-policy.js";
import * as settings from "../core/settings.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { channelNotifySheet } from "./notify-settings.js";
import { canEditChannel } from "./admin-entry.js";
import "./social-strings.js";

/**
 * Returns items() for menuButton. The admin item appears once the server
 * confirmed the permission (owners and administrators at once).
 */
export function channelMenuItems(scope) {
    const session = app.session;
    const channel = scope === 0 ? { id: 0, name: t("channels.global") } : session?.channels().find(c => c.id === scope) || { id: scope, name: String(scope) };
    let canEdit = false;
    if (scope > 0 && session && !channel.home) canEditChannel(session, scope).then(ok => { canEdit = ok; }).catch(() => {});
    return () => [
        { label: t("members.title"), run: () => { location.hash = `#/members/${scope}`; } },
        { label: `${t("channelMenu.notifications")}: ${t(`notify.level.${channelLevel(settings.all(), scope)}`)}`, run: () => channelNotifySheet(channel) },
        canEdit ? { label: t("entry.channel", { name: channel.name }), run: () => { location.hash = `#/admin/channel/${scope}`; } } : null,
    ].filter(Boolean);
}

/** The voice room header's link to the member list. */
export function membersButton(channelID) {
    return h("a", { class: "icon-btn", href: `#/members/${channelID}`, "aria-label": t("members.title") }, icon("users"));
}
