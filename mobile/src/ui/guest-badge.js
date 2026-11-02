// Account status comes from the authenticated server snapshot, independently of roles.
import { t } from "../i18n.js";
import { h } from "./dom.js";

export function guestBadge(client) {
    return client?.is_guest === true ? h("span", { class: "badge guest-badge", title: t("presence.guestAccount"), "aria-label": t("presence.guestAccount") }, t("presence.guestBadge")) : null;
}
