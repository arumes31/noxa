// chat-settings.js — keyword highlights and undo send, as rows of the
// settings registry's Chat section (join/leave lines already have a row in
// settings-sections.js). Values live in core/settings.js.
import { parseKeywords } from "../core/mentions.js";
import * as settings from "../core/settings.js";
import { UNDO_SEND_CHOICES } from "../core/undo-send.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { registerSettings } from "./settings-registry.js";
import "./chat-actions-strings.js";
import "./chat-actions.css";

let seq = 0;

function keywordsRow() {
    const id = `chat-kw-${++seq}`;
    const keywords = h("input", { id, class: "act-field", type: "text", autocomplete: "off", enterkeyhint: "done", "aria-describedby": `${id}-hint` });
    keywords.value = (settings.get("keywords") || []).join(", ");
    keywords.addEventListener("change", () => {
        const list = parseKeywords(keywords.value);
        settings.set({ keywords: list });
        keywords.value = list.join(", ");
    });
    return h("div", { class: "act-settings" },
        h("label", { class: "row-title", for: id }, t("act.settings.keywords")),
        h("p", { class: "row-sub", id: `${id}-hint`, style: "white-space:normal" }, t("act.settings.keywordsHint")),
        keywords);
}

function undoRow() {
    const id = `chat-undo-${++seq}`;
    const undo = h("select", { id, class: "act-select", "aria-describedby": `${id}-hint` },
        UNDO_SEND_CHOICES.map(seconds => h("option", { value: String(seconds) }, seconds ? t("act.settings.undoSeconds", { seconds }) : t("act.settings.undoOff"))));
    undo.value = String(settings.get("undoSendSeconds") || 0);
    undo.addEventListener("change", () => settings.set({ undoSendSeconds: Number(undo.value) || 0 }));
    return h("div", { class: "toggle" },
        h("span", { class: "row-main" }, h("label", { class: "row-title", for: id }, t("act.settings.undo")), h("span", { class: "row-sub", id: `${id}-hint`, style: "white-space:normal" }, t("act.settings.undoHint"))),
        undo);
}

registerSettings("chat", [
    { key: "keywords", label: t("act.settings.keywords"), keywords: "highlight keyword mention stichwort hervorheben", render: keywordsRow },
    { key: "undoSend", label: t("act.settings.undo"), keywords: "undo send delay rückgängig senden", render: undoRow },
]);
