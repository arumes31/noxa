// home.js — Home (02): live voice rooms and recent conversations.
import { app, dismissReminder, reminders, startCall } from "../app.js";
import { t } from "../i18n.js";
import { clock, h, sheet } from "./dom.js";
import { dmHref, header, lastMessage, nav, scopeName, unreadBadge } from "./common.js";
import { avatar } from "./dom.js";
import { homeCards } from "./devices.js";
import { icon } from "./icons.js";
import { presenceOf } from "../core/presence.js";
import { isBlocked } from "../core/social.js";
import { notifier } from "../notifier.js";
import { dndButton } from "./notify-settings.js";
import { statusDot } from "./presence-ui.js";

import { isMuted } from "../core/conversation-prefs.js";
import "./conversation-strings.js";

/** The activity center's entry, with its unread count. */
function activityLink() {
    const unread = notifier.activity?.unread || 0;
    return h("a", { class: "icon-btn", href: "#/activity", "aria-label": unread ? `${t("activity.open")} · ${t("home.unread", { count: unread })}` : t("activity.open") },
        icon("inbox"), unread ? h("span", { class: "badge badge-mention", "aria-hidden": "true" }, unread > 99 ? "99+" : String(unread)) : null);
}

/** A muted conversation shows a muted mark instead of its unread count. */
function mutedMark() {
    return h("span", { class: "row-muted", role: "img", "aria-label": t("conv.mutedLabel") }, icon("bellOff"));
}

export function renderHome(root) {
    const session = app.session;
    const live = session.channels().filter(c => !c.textOnly && !c.home && c.clients.length);
    const channelRows = session.chatScopes()
        .map(scope => ({ scope, last: lastMessage(session, scope) }))
        .filter(entry => entry.last)
        .map(({ scope, last }) => ({ at: last.sentAt, row: h("a", { class: "row", href: `#/chat/${scope}` },
            scope === 0 ? h("span", { class: "hash" }, "◎") : h("span", { class: "hash" }, "#"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, scopeName(session, scope).replace(/^# /, "#")),
                h("span", { class: "row-sub" }, `${last.from}: ${last.text ?? t("chat.locked")}`)),
            unreadBadge(`channel:${scope}`) || h("span", { class: "row-meta" }, clock(last.sentAt))) }));
    // Blocked users' conversations are hidden, not deleted.
    const dmRows = (app.dm?.list() || []).filter(conversation => !isBlocked(conversation.peer)).map(conversation => {
        const last = conversation.messages.at(-1);
        const name = conversation.nickname || session.clients().find(c => c.unique_id === conversation.peer)?.nickname || t("dm.someone");
        const presence = presenceOf(session, conversation.peer);
        return { at: last.sentAt, row: h("a", { class: "row", href: dmHref(conversation.peer) },
            h("span", { class: "avatar-wrap" }, avatar(name, "md", conversation.peer), statusDot(presence.online ? presence.status : "offline")),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, name),
                h("span", { class: "row-sub" }, `${last.self ? t("dm.you") + ": " : ""}${last.body ?? t("chat.locked")}`)),
            isMuted(`dm:${conversation.peer}`) ? mutedMark() : unreadBadge(`dm:${conversation.peer}`) || h("span", { class: "row-meta" }, clock(last.sentAt))) };
    });
    const groupRows = [...(app.groups?.list.values() || [])].filter(g => !g.members?.find(m => m.unique_id === session.uniqueID)?.pending).map(group => {
        const tag = `conversation:${group.id}`;
        const unread = app.unread.get(tag)?.count || group.unread_count || 0;
        const last = app.groups.messages.get(group.id)?.at(-1);
        return { at: last?.created_at || 0, row: h("a", { class: "row", href: `#/group/${encodeURIComponent(group.id)}` },
            h("span", { class: "hash" }, "◇"),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, group.name),
                h("span", { class: "row-sub" }, last ? (last.text ?? t("group.locked")) : t("group.members", { count: (group.members || []).filter(m => !m.pending).length }))),
            isMuted(tag) ? mutedMark() : unread ? h("span", { class: "badge", "aria-label": t("home.unread", { count: unread }) }, String(unread)) : null) };
    });
    const recent = [...channelRows, ...dmRows, ...groupRows].sort((a, b) => b.at - a.at);
    const invitations = [...(app.groups?.list.values() || [])].filter(g => g.members?.find(m => m.unique_id === session.uniqueID)?.pending);
    root.replaceChildren(h("section", { class: "screen" },
        header(t("home.title"), { action: h("div", { class: "header-actions" }, activityLink(), dndButton(), h("button", { class: "icon-btn", type: "button", "aria-label": t("home.new"), onclick: async () => {
            const destination = await sheet(t("home.new"), "", [
                { label: t("home.newMessage"), value: "new-chat", primary: true },
                { label: t("group.create"), value: "new-group" },
                { label: t("home.cancel"), value: null },
            ]);
            if (destination && app.session === session) location.hash = `#/${destination}`;
        } }, "+")) }),
        h("div", { class: "screen-body" },
            homeCards(session),
            h("a", { class: "btn btn-primary", href: "#/new-chat" }, t("chatNew.title")),
            live.map(channel => h("a", { class: "card row", href: "#/voice", style: "border-color:var(--border-strong)" },
                h("div", { class: "row-main" },
                    h("span", { class: "row-title" }, t("home.live", { channel: channel.name })),
                    h("span", { class: "row-sub" }, channel.clients.map(c => c.nickname).join(", "))),
                h("span", { class: "badge" }, String(channel.clients.length)))),
            invitations.map(group => h("div", { class: "card", role: "group", "aria-label": t("group.invited", { name: group.name }) },
                h("p", { class: "row-title" }, t("group.invited", { name: group.name })),
                h("div", { class: "voice-controls", style: "grid-template-columns:1fr 1fr" },
                    h("button", { class: "btn btn-primary", type: "button", onclick: () => app.groups.answer(group.id, "accept").catch(() => {}) }, t("group.accept")),
                    h("button", { class: "btn", type: "button", onclick: () => app.groups.answer(group.id, "decline").catch(() => {}) }, t("call.decline"))))),
            reminders().filter(r => r.shown).map(reminder => h("div", { class: "card", role: "group", "aria-label": t("call.callBack", { name: reminder.name }) },
                h("p", { class: "row-title" }, t("call.callBack", { name: reminder.name })),
                h("div", { class: "voice-controls", style: "grid-template-columns:1fr 1fr" },
                    h("button", { class: "btn btn-primary", type: "button", onclick: () => { dismissReminder(reminder.uid); startCall(reminder.uid).catch(() => {}); } }, t("call.callNow")),
                    h("button", { class: "btn", type: "button", onclick: () => dismissReminder(reminder.uid) }, t("call.dismiss"))))),
            h("p", { class: "section-label" }, t("home.recent")),
            recent.length ? h("ul", { class: "list" }, recent.map(entry => h("li", {}, entry.row)))
                : h("p", { class: "hint" }, t("home.empty")))),
        nav("home"));
}
