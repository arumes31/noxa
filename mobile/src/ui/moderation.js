// moderation.js — the member moderation actions: move, voice disconnect,
// server mute/deafen, kick, ban, sign out every session, and reporting.
// moderationActions(client) returns the actions as an element so a profile
// card can mount them; openMemberSheet(client) shows them in a sheet (the
// Servers member rows use it). The server decides; refused actions toast
// its message.
import { app } from "../app.js";
import { adminFor, BAN_DURATIONS, canManageRoles } from "../core/admin.js";
import { addStrings, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { busy, chooseSheet, field, formSheet, input, textSheet } from "./admin-kit.js";

addStrings({
    en: {
        "mod.title": "Moderation",
        "mod.menu": "Actions for {name}",
        "mod.move": "Move to channel…",
        "mod.moveTitle": "Move {name} to",
        "mod.moved": "{name} was moved.",
        "mod.disconnect": "Disconnect from voice",
        "mod.disconnected": "{name} was disconnected from voice.",
        "mod.mute": "Server mute",
        "mod.unmute": "Remove server mute",
        "mod.deafen": "Server deafen",
        "mod.undeafen": "Remove server deafen",
        "mod.voiceSaved": "Voice state changed.",
        "mod.kick": "Remove from server…",
        "mod.kickTitle": "Remove {name} from the server",
        "mod.kicked": "{name} was removed from the server.",
        "mod.ban": "Ban…",
        "mod.banTitle": "Ban {name}",
        "mod.banned": "{name} was banned.",
        "mod.banUnconfirmed": "{name} was signed out, but the ban may not have been saved. Check the ban list.",
        "mod.duration": "Duration",
        "mod.duration.5m": "5 minutes",
        "mod.duration.1h": "1 hour",
        "mod.duration.1d": "1 day",
        "mod.duration.permanent": "Permanent",
        "mod.reason": "Reason (optional)",
        "mod.sessions": "Sign out all sessions",
        "mod.sessionsTitle": "Sign out every session of {name}?",
        "mod.sessionsDone": "{count} sessions were signed out.",
        "mod.report": "Report member…",
        "mod.reportTitle": "Report {name}",
        "mod.reportReason": "What happened?",
        "mod.reported": "Thanks — the moderators can see your report.",
        "mod.confirm": "Confirm",
    },
    de: {
        "mod.title": "Moderation",
        "mod.menu": "Aktionen für {name}",
        "mod.move": "In Kanal verschieben…",
        "mod.moveTitle": "{name} verschieben nach",
        "mod.moved": "{name} wurde verschoben.",
        "mod.disconnect": "Aus Sprachkanal entfernen",
        "mod.disconnected": "{name} wurde aus dem Sprachkanal entfernt.",
        "mod.mute": "Serverseitig stummschalten",
        "mod.unmute": "Serverseitige Stummschaltung aufheben",
        "mod.deafen": "Serverseitig taub schalten",
        "mod.undeafen": "Serverseitige Taubschaltung aufheben",
        "mod.voiceSaved": "Sprachstatus geändert.",
        "mod.kick": "Vom Server entfernen…",
        "mod.kickTitle": "{name} vom Server entfernen",
        "mod.kicked": "{name} wurde vom Server entfernt.",
        "mod.ban": "Sperren…",
        "mod.banTitle": "{name} sperren",
        "mod.banned": "{name} wurde gesperrt.",
        "mod.banUnconfirmed": "{name} wurde abgemeldet, aber die Sperre wurde womöglich nicht gespeichert. Prüfe die Sperrliste.",
        "mod.duration": "Dauer",
        "mod.duration.5m": "5 Minuten",
        "mod.duration.1h": "1 Stunde",
        "mod.duration.1d": "1 Tag",
        "mod.duration.permanent": "Dauerhaft",
        "mod.reason": "Grund (optional)",
        "mod.sessions": "Alle Sitzungen abmelden",
        "mod.sessionsTitle": "Alle Sitzungen von {name} abmelden?",
        "mod.sessionsDone": "{count} Sitzungen wurden abgemeldet.",
        "mod.report": "Mitglied melden…",
        "mod.reportTitle": "{name} melden",
        "mod.reportReason": "Was ist passiert?",
        "mod.reported": "Danke – die Moderation sieht deine Meldung.",
        "mod.confirm": "Bestätigen",
    },
});

/** Signs of moderation rights the snapshot carries without another request. */
function looksLikeModerator(session) {
    return session.isAdmin || !!session.snapshot?.can_view_audit_log ||
        session.channels().some(c => c.canManageAccess || c.canManageVoiceQueue);
}

/** The live entry of a client (its channel and voice flags may have changed). */
function live(session, client) {
    return session.clients().find(c => c.client_id === client.client_id) || client;
}

async function banSheet(name) {
    const duration = h("div", { class: "admin-segments", role: "radiogroup", "aria-label": t("mod.duration") });
    let seconds = BAN_DURATIONS[0].seconds;
    const buttons = BAN_DURATIONS.map(d => h("button", { type: "button", class: "btn admin-segment", role: "radio", "aria-checked": String(d.seconds === seconds), onclick: () => {
        seconds = d.seconds;
        buttons.forEach((b, i) => b.setAttribute("aria-checked", String(BAN_DURATIONS[i].seconds === seconds)));
    } }, t(`mod.duration.${d.key}`)));
    duration.append(...buttons);
    const reason = input({ maxlength: "500" });
    const choice = await formSheet(t("mod.banTitle", { name }), h("div", { class: "admin-stack" },
        h("p", { class: "section-label" }, t("mod.duration")), duration, field(t("mod.reason"), reason)), [
        { label: t("mod.ban").replace("…", ""), value: "ban", danger: true },
        { label: t("admin.cancel"), value: null },
    ]);
    return choice === "ban" ? { seconds, reason: reason.value.trim() } : null;
}

/**
 * The actions for one member (a snapshot client). Returns an element; it
 * dispatches "moderation-done" when the member left this session's reach.
 */
export function moderationActions(client) {
    const session = app.session;
    const admin = adminFor(session);
    const name = client.nickname || t("dm.someone");
    const box = h("div", { class: "mod-actions", role: "group", "aria-label": t("mod.menu", { name }) });
    if (!session || client.client_id === session.clientID) return box;
    const done = () => box.dispatchEvent(new CustomEvent("moderation-done", { bubbles: true }));
    const action = (label, run, { danger = false, iconName } = {}) => {
        const button = h("button", { class: `row mod-action${danger ? " danger" : ""}`, type: "button" }, iconName ? icon(iconName) : null, h("span", { class: "row-main" }, h("span", { class: "row-title" }, label)));
        button.onclick = () => busy(button, run);
        return h("li", {}, button);
    };
    const moderation = h("ul", { class: "list", hidden: !looksLikeModerator(session) });
    const render = () => {
        const current = live(session, client);
        const channel = session.channels().find(c => c.id === current.channel_id);
        const inVoice = !!channel && !channel.textOnly && current.channel_id > 0;
        const sessions = session.clients().filter(c => c.unique_id === client.unique_id && c.client_id !== session.clientID).length;
        moderation.replaceChildren(
            action(t("mod.move"), async () => {
                const targets = session.channels().filter(c => !c.textOnly && c.id !== current.channel_id);
                const target = await chooseSheet(t("mod.moveTitle", { name }), targets.map(c => ({ value: c.id, label: c.name, sub: c.clients.length ? String(c.clients.length) : "" })));
                if (target === null) return;
                await admin.move(client.client_id, target);
                toast(t("mod.moved", { name }));
            }, { iconName: "voice" }),
            inVoice ? action(t("mod.disconnect"), async () => {
                await admin.disconnect(current);
                toast(t("mod.disconnected", { name }));
            }, { iconName: "hangup" }) : null,
            inVoice ? action(t(current.server_muted ? "mod.unmute" : "mod.mute"), async () => {
                const state = await admin.voice(current, { muted: !current.server_muted });
                Object.assign(current, { server_muted: state.muted, server_deafened: state.deafened });
                toast(t("mod.voiceSaved"));
                render();
            }, { iconName: "micOff" }) : null,
            inVoice ? action(t(current.server_deafened ? "mod.undeafen" : "mod.deafen"), async () => {
                const state = await admin.voice(current, { deafened: !current.server_deafened });
                Object.assign(current, { server_muted: state.muted, server_deafened: state.deafened });
                toast(t("mod.voiceSaved"));
                render();
            }, { iconName: "voice" }) : null,
            action(t("mod.kick"), async () => {
                const reason = await textSheet(t("mod.kickTitle", { name }), t("mod.reason"), { confirm: t("mod.kick").replace("…", ""), danger: true });
                if (reason === null) return;
                await admin.kick(client.client_id, reason);
                toast(t("mod.kicked", { name }));
                done();
            }, { danger: true }),
            client.unique_id ? action(t("mod.ban"), async () => {
                const choice = await banSheet(name);
                if (!choice) return;
                const result = await admin.ban(client.client_id, choice.reason, choice.seconds);
                toast(t(result.persistence === "unconfirmed" ? "mod.banUnconfirmed" : "mod.banned", { name }), result.persistence === "unconfirmed" ? "warn" : "info");
                done();
            }, { danger: true }) : null,
            sessions > 1 ? action(t("mod.sessions"), async () => {
                const reason = await textSheet(t("mod.sessionsTitle", { name }), t("mod.reason"), { confirm: t("mod.confirm"), danger: true });
                if (reason === null) return;
                const count = await admin.kickSessions(client.unique_id, reason);
                toast(t("mod.sessionsDone", { count }));
                done();
            }, { danger: true }) : null);
    };
    render();
    if (moderation.hidden) canManageRoles(session).then(ok => { if (ok) moderation.hidden = false; });
    const report = client.unique_id && !client.is_guest ? h("ul", { class: "list" }, action(t("mod.report"), async () => {
        const reason = await textSheet(t("mod.reportTitle", { name }), t("mod.reportReason"), { confirm: t("mod.report").replace("…", ""), required: true, multiline: true });
        if (!reason) return;
        await admin.complain(client.unique_id, reason);
        toast(t("mod.reported"));
    }, { iconName: "shield" })) : null;
    box.append(h("p", { class: "section-label", hidden: moderation.hidden }, t("mod.title")), moderation, report);
    // The label follows the list's visibility once the role probe answers.
    new MutationObserver(() => { box.firstChild.hidden = moderation.hidden; }).observe(moderation, { attributes: true, attributeFilter: ["hidden"] });
    return box;
}

/** Opens the member's actions in a bottom sheet. */
export function openMemberSheet(client) {
    const actions = moderationActions(client);
    let closeSheet;
    actions.addEventListener("moderation-done", () => closeSheet?.(null));
    return formSheet(client.nickname || t("dm.someone"), actions, [{ label: t("common.close"), value: null }], { mount: close => { closeSheet = close; } });
}

/** The ⋯ button of a member row; also opens on long-press (contextmenu). */
export function memberMenuButton(client, row) {
    const button = h("button", { class: "icon-btn member-menu", type: "button", "aria-label": t("mod.menu", { name: client.nickname }), "aria-haspopup": "dialog", onclick: event => {
        event.preventDefault();
        event.stopPropagation();
        openMemberSheet(client);
    } }, icon("more"));
    row?.addEventListener("contextmenu", event => { event.preventDefault(); openMemberSheet(client); });
    return button;
}
