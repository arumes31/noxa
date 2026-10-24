// channel-menu.js — the channel chat header's menu: members, the channel's
// notification level and, for those allowed, the channel's admin settings.
import { app } from "../app.js";
import { channelLevel } from "../core/notify-policy.js";
import * as settings from "../core/settings.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { channelNotifySheet } from "./notify-settings.js";
import { channelSettingsRow } from "./admin-entry.js";
import { openPanel } from "./panel.js";
import "./social-strings.js";

export function channelMenuButton(scope) {
    return h("button", { class: "icon-btn channel-menu", type: "button", "aria-label": t("channelMenu.title"), "aria-haspopup": "dialog", onclick: () => {
        const channel = scope === 0 ? { id: 0, name: t("channels.global") } : app.session?.channels().find(c => c.id === scope) || { id: scope, name: String(scope) };
        openPanel({ title: t("channelMenu.title") }, close => [
            h("h2", { class: "sheet-title" }, scope === 0 ? channel.name : `#${channel.name}`),
            h("a", { class: "row", href: `#/members/${scope}`, onclick: () => close() }, h("span", { class: "hash", "aria-hidden": "true" }, icon("users")), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("members.title")))),
            h("button", { class: "row", type: "button", onclick: () => { close(); channelNotifySheet(channel); } },
                h("span", { class: "hash", "aria-hidden": "true" }, icon("bell")),
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("channelMenu.notifications")), h("span", { class: "row-sub" }, t(`notify.level.${channelLevel(settings.all(), scope)}`)))),
            scope > 0 ? channelSettingsRow(app.session, scope, () => close()) : null,
            h("button", { class: "btn btn-block", type: "button", onclick: () => close() }, t("common.close")),
        ]);
    } }, icon("more"));
}

/** The voice room header's link to the member list. */
export function membersButton(channelID) {
    return h("a", { class: "icon-btn", href: `#/members/${channelID}`, "aria-label": t("members.title") }, icon("users"));
}
