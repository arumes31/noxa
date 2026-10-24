// voice.js — Voice room (09).
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { header, nav } from "./common.js";

export function renderVoice(root) {
    root.replaceChildren(h("section", { class: "screen" },
        header(t("voice.title")),
        h("div", { class: "screen-body" }, h("div", { class: "card" }, h("p", { class: "hint" }, t("voice.later"))))),
        nav("voice"));
}
