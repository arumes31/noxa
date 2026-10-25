// dm-settings.js — the Chat privacy switch of the You screen: one setting
// for read receipts and typing indicators in direct messages and private
// groups, like the desktop's (settings.hideChatActivity).
import { get, set } from "../core/settings.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import "./conversation-strings.js";

export function dmPrivacySettings() {
    const input = h("input", { type: "checkbox", role: "switch", checked: !get("hideChatActivity") });
    input.onchange = () => set({ hideChatActivity: !input.checked });
    return h("section", { class: "dm-privacy", "aria-label": t("privacy.section") },
        h("p", { class: "section-label" }, t("privacy.section")),
        h("div", { class: "card" },
            h("label", { class: "toggle" },
                h("span", { class: "row-main" },
                    h("span", { class: "row-title" }, t("privacy.activity")),
                    h("span", { class: "row-sub wrap" }, t("privacy.activityHint"))),
                input)));
}
