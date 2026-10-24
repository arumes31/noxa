// contacts.js — the You screen's contacts and blocked users: saved
// members with their online state, added from a member card or by unique
// ID. Both lists live on this phone only.
import { app } from "../app.js";
import { presenceOf } from "../core/presence.js";
import * as settings from "../core/settings.js";
import { addContact, contacts, removeContact, setBlocked } from "../core/social.js";
import { t } from "../i18n.js";
import { avatar, h } from "./dom.js";
import { promptText } from "./panel.js";
import { statusDot } from "./presence-ui.js";
import "./social-strings.js";
import "./social.css";

const openProfile = options => import("./profile.js").then(module => module.openProfile(options));

export function contactsSection() {
    const box = h("div", { class: "contacts" });
    const render = () => {
        const session = app.session;
        const list = contacts().map(contact => ({ ...contact, presence: presenceOf(session, contact.uid) }))
            .sort((a, b) => (b.presence.online - a.presence.online) || (a.name || "").localeCompare(b.name || ""));
        const blocked = settings.get("blockedUsers") || [];
        box.replaceChildren(
            list.length ? h("ul", { class: "list" }, list.map(contact => {
                const name = contact.presence.client?.nickname || contact.name || contact.uid;
                const status = contact.presence.online ? contact.presence.status : "offline";
                return h("li", { class: "contact-row" },
                    h("button", { class: "row", type: "button", "aria-label": t("members.open", { name }), onclick: () => openProfile({ uid: contact.uid, name }) },
                        h("span", { class: "avatar-wrap" }, avatar(name, "md", contact.uid), statusDot(status)),
                        h("span", { class: "row-main" }, h("span", { class: "row-title" }, name),
                            h("span", { class: "row-sub" }, [t(`status.${status}`), contact.presence.message, contact.note].filter(Boolean).join(" · ")))),
                    h("button", { class: "icon-btn", type: "button", "aria-label": t("contacts.remove", { name }), onclick: () => { removeContact(contact.uid); render(); } }, "×"));
            })) : h("p", { class: "hint" }, t("contacts.empty")),
            h("button", { class: "btn btn-block", type: "button", onclick: async () => {
                const uid = await promptText(t("contacts.add"), { label: t("contacts.uid"), placeholder: t("contacts.uid"), maxLength: 128, confirm: t("contacts.addAction"), cancel: t("home.cancel"), hint: t("contacts.addHint") });
                if (!uid?.trim()) return;
                const id = uid.trim();
                addContact({ uid: id, name: session?.clients().find(c => c.unique_id === id)?.nickname || "" });
                render();
            } }, t("contacts.add")),
            blocked.length ? h("details", { class: "blocked-list" },
                h("summary", {}, t("contacts.blocked", { count: blocked.length })),
                h("ul", { class: "list" }, blocked.map(uid => {
                    const name = session?.clients().find(c => c.unique_id === uid)?.nickname || contacts().find(c => c.uid === uid)?.name || uid;
                    return h("li", { class: "row" }, h("span", { class: "row-main" }, h("span", { class: "row-title" }, name)),
                        h("button", { class: "btn", type: "button", onclick: () => { setBlocked(uid, false); render(); } }, t("profile.unblock")));
                }))) : "");
    };
    render();
    const stop = settings.onChange(({ changed }) => { if (changed.includes("contacts") || changed.includes("blockedUsers")) { if (box.isConnected) render(); else stop(); } });
    return box;
}
