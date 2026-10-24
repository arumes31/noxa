// profile.js — a member's card: picture, name, status, roles, unique ID,
// connection info when the server shares it, and what you can do with
// them (message, call, poke, contact, block, notify when online), plus a
// private note kept on this phone.
import { app, startCall } from "../app.js";
import { presenceOf } from "../core/presence.js";
import {
    addContact, clientInfo, contacts, isBlocked, isBuddy, isContact, note, removeContact, rolesOf, setBlocked, setBuddy, setNote,
} from "../core/social.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { dmHref } from "./common.js";
import { openPanel, promptText } from "./panel.js";
import { statusDot } from "./presence-ui.js";
import "./social-strings.js";
import "./social.css";

function duration(seconds) {
    const s = Math.max(0, Math.round(seconds));
    if (s < 60) return t("profile.seconds", { n: s });
    if (s < 3600) return t("profile.minutes", { n: Math.floor(s / 60) });
    return t("profile.hours", { h: Math.floor(s / 3600), m: Math.floor((s % 3600) / 60) });
}

/** Sends a poke with an optional message; returns whether it was sent. */
export async function pokeMember(clientID, name) {
    const message = await promptText(t("poke.title", { name }), { placeholder: t("poke.placeholder"), maxLength: 100, confirm: t("poke.send"), cancel: t("home.cancel") });
    if (message === null) return false;
    try {
        await app.session.poke(clientID, message.trim());
        toast(t("poke.sent", { name }));
        return true;
    } catch (error) {
        toast(t("common.error", { error: error.message }), "warn");
        return false;
    }
}

/**
 * Opens the card of uid (or of the session clientID). name is shown while
 * the member is offline.
 */
export function openProfile({ uid = "", clientID = "", name = "" } = {}) {
    const session = app.session;
    if (!session) return null;
    const byClient = clientID ? session.clients().find(c => c.client_id === clientID) : null;
    const id = uid || byClient?.unique_id || "";
    const presence = presenceOf(session, id);
    const client = byClient || presence.client || null;
    const display = client?.nickname || name || contacts().find(c => c.uid === id)?.name || app.dm?.conversations?.get(id)?.nickname || t("dm.someone");
    const self = id === session.uniqueID;
    return openPanel({ title: t("profile.title", { name: display }), className: "profile-card" }, close => {
        const actions = h("div", { class: "profile-actions" });
        const info = h("dl", { class: "profile-info" });
        const status = presence.online ? presence.status : "offline";
        const roles = rolesOf(client);

        const renderActions = () => {
            const blocked = isBlocked(id), contact = isContact(id), buddy = isBuddy(id);
            const button = (label, onclick, extra = {}) => h("button", { class: `btn ${extra.class || ""}`, type: "button", onclick, ...extra.attrs }, label);
            actions.replaceChildren(...(self ? [] : [
                button(t("profile.message"), () => { close(); location.hash = dmHref(id); }, { attrs: { disabled: !id || blocked } }),
                button(t("profile.call"), () => { close(); startCall(id).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")); }, { attrs: { disabled: !id || blocked || !presence.online } }),
                button(t("profile.poke"), () => pokeMember(client.client_id, display), { attrs: { disabled: !client?.client_id || blocked } }),
                button(contact ? t("profile.removeContact") : t("profile.addContact"), () => {
                    contact ? removeContact(id) : addContact({ uid: id, name: display });
                    renderActions();
                }),
                h("label", { class: "toggle profile-toggle" }, h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("profile.buddy")), h("span", { class: "row-sub" }, t("profile.buddyHint"))),
                    h("input", { type: "checkbox", role: "switch", checked: buddy, onchange: event => setBuddy(id, event.target.checked) })),
                button(blocked ? t("profile.unblock") : t("profile.block"), () => {
                    setBlocked(id, !blocked);
                    toast(blocked ? t("profile.unblocked", { name: display }) : t("profile.blocked", { name: display }));
                    renderActions();
                }, { class: blocked ? "" : "btn-danger" }),
            ]));
        };
        renderActions();

        const row = (label, value) => [h("dt", {}, label), h("dd", {}, value)];
        if (client?.connected_at) info.append(...row(t("profile.connected"), new Date(client.connected_at).toLocaleString()));
        if (client?.client_id && session.conn.open) {
            clientInfo(session, client.client_id).then(data => {
                info.replaceChildren(
                    ...row(t("profile.connected"), new Date(data.connected_at * 1000).toLocaleString()),
                    ...row(t("profile.idle"), duration(data.idle_seconds)),
                    ...(data.ping_ms >= 0 ? row(t("profile.ping"), `${data.ping_ms} ms`) : []),
                    ...(data.client_version ? row(t("profile.version"), data.client_version) : []));
            }).catch(() => { /* not allowed to see it */ });
        }

        const noteField = h("textarea", { class: "input profile-note", rows: 2, maxlength: 1000, placeholder: t("profile.notePlaceholder"), "aria-label": t("profile.note") });
        noteField.value = note(id);
        noteField.addEventListener("change", () => setNote(id, noteField.value));
        noteField.addEventListener("blur", () => setNote(id, noteField.value));

        return [
            h("div", { class: "profile-head" },
                avatar(display, "lg", id),
                h("div", { class: "row-main" },
                    h("h2", { class: "sheet-title" }, display),
                    h("p", { class: "profile-status" }, statusDot(status), " ", t(`status.${status}`), presence.message ? ` · ${presence.message}` : "",
                        presence.onMobile ? h("span", { class: "badge profile-mobile", title: t("presence.onMobile") }, t("presence.mobileBadge")) : null),
                    roles.length ? h("ul", { class: "role-chips", "aria-label": t("profile.roles") }, roles.map(role => h("li", { class: "role-chip", style: role.color ? `--role:${role.color}` : null }, role.name))) : null)),
            id ? h("div", { class: "profile-uid" },
                h("span", { class: "mono", title: id }, id),
                h("button", { class: "btn", type: "button", onclick: async () => {
                    try { await navigator.clipboard.writeText(id); toast(t("profile.copied")); } catch { toast(id); }
                } }, t("profile.copy"))) : null,
            info,
            actions,
            self ? null : h("label", { class: "field" }, h("span", {}, t("profile.note")), noteField),
            h("button", { class: "btn btn-block", type: "button", onclick: () => close() }, t("common.close")),
        ];
    });
}
