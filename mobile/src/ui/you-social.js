// you-social.js — the You screen's profile card: picture, display name and
// status, kept current while the screen is open.
import { app } from "../app.js";
import { statusOf } from "../core/presence.js";
import { t } from "../i18n.js";
import { avatar, h, sheet } from "./dom.js";
import { avatarEditor, nameSheet, statusDot, statusLine, statusSheet } from "./presence-ui.js";
import "./social-strings.js";
import "./social.css";

async function avatarMenu() {
    const choice = await sheet(t("profile.changeAvatar"), "", [
        { label: t("profile.choosePhoto"), value: "gallery", primary: true },
        { label: t("profile.takePhoto"), value: "camera" },
        { label: t("home.cancel"), value: null },
    ]);
    if (choice) avatarEditor({ camera: choice === "camera" });
}

/** Returns {card, stop}: the card and a function that stops its updates. */
export function profileCard(session) {
    const card = h("section", { class: "card me-card", "aria-label": t("profile.yours") });
    const render = () => {
        const self = session.self?.() || null;
        const status = statusOf(self);
        card.replaceChildren(
            h("div", { class: "row" },
                h("button", { class: "me-avatar", type: "button", "aria-label": t("profile.changeAvatar"), onclick: avatarMenu },
                    h("span", { class: "avatar-wrap" }, avatar(session.nickname, "lg", session.uniqueID), statusDot(status))),
                h("span", { class: "row-main" },
                    h("span", { class: "row-title me-name" }, session.nickname),
                    h("span", { class: "row-sub me-status" }, statusLine(self) || t(`status.${status}`)),
                    h("span", { class: "row-sub" }, app.info?.server_name || ""))),
            h("div", { class: "me-actions" },
                h("button", { class: "btn", type: "button", onclick: statusSheet }, t("profile.setStatus")),
                h("button", { class: "btn", type: "button", onclick: nameSheet }, t("profile.changeName")),
                h("button", { class: "btn", type: "button", onclick: avatarMenu }, t("profile.changeAvatar"))));
    };
    render();
    const update = () => { if (card.isConnected) render(); };
    session.addEventListener("snapshot", update);
    return { card, stop: () => session.removeEventListener("snapshot", update) };
}
