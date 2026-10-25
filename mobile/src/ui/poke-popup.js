// poke-popup.js — a received poke: who, what and when, with "Poke back".
import { app } from "../app.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { dmHref } from "./common.js";
import { openPanel } from "./panel.js";
import "./social-strings.js";
import "./social.css";

let current = null;

export function showPoke({ clientID, uid, name, message, at }) {
    // A second poke replaces the first instead of stacking sheets.
    current?.close();
    current = openPanel({ title: t("poke.received", { name }), className: "poke-popup", onClose: () => { current = null; } }, close => [
        h("div", { class: "poke-head", role: "alert" },
            avatar(name, "lg", uid),
            h("div", { class: "row-main" },
                h("h2", { class: "sheet-title" }, t("poke.received", { name })),
                h("p", { class: "poke-time" }, new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })))),
        message ? h("blockquote", { class: "poke-message" }, message) : null,
        h("div", { class: "panel-buttons" },
            uid ? h("button", { class: "btn", type: "button", onclick: () => { close(); location.hash = dmHref(uid); } }, t("profile.message")) : null,
            h("button", { class: "btn btn-primary", type: "button", autofocus: true, onclick: async event => {
                const button = event.currentTarget;
                button.disabled = true;
                try {
                    await app.session.poke(clientID, "");
                    toast(t("poke.sent", { name }));
                    close();
                } catch (error) {
                    button.disabled = false;
                    toast(t("common.error", { error: error.message }), "warn");
                }
            } }, t("poke.back"))),
        h("button", { class: "btn btn-block", type: "button", onclick: () => close() }, t("common.close")),
    ]);
}
