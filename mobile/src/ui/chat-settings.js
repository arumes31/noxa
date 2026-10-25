// chat-settings.js — the Chat section of You: keyword highlights, undo send
// and join/leave lines. Values live in core/settings.js.
import { parseKeywords } from "../core/mentions.js";
import * as settings from "../core/settings.js";
import { UNDO_SEND_CHOICES } from "../core/undo-send.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import "./chat-actions-strings.js";
import "./chat-actions.css";

let seq = 0;

export function chatSettings() {
    const id = `chat-settings-${++seq}`;
    const keywords = h("input", { id: `${id}-kw`, class: "act-field", type: "text", autocomplete: "off", enterkeyhint: "done", "aria-describedby": `${id}-kw-hint` });
    keywords.value = (settings.get("keywords") || []).join(", ");
    keywords.addEventListener("change", () => {
        const list = parseKeywords(keywords.value);
        settings.set({ keywords: list });
        keywords.value = list.join(", ");
    });
    const undo = h("select", { id: `${id}-undo`, class: "act-select", "aria-describedby": `${id}-undo-hint` },
        UNDO_SEND_CHOICES.map(seconds => h("option", { value: String(seconds) }, seconds ? t("act.settings.undoSeconds", { seconds }) : t("act.settings.undoOff"))));
    undo.value = String(settings.get("undoSendSeconds") || 0);
    undo.addEventListener("change", () => settings.set({ undoSendSeconds: Number(undo.value) || 0 }));
    const joinLeave = h("input", { type: "checkbox", role: "switch", checked: !!settings.get("showJoinLeave") });
    joinLeave.addEventListener("change", () => settings.set({ showJoinLeave: joinLeave.checked }));
    return h("div", { class: "act-settings" },
        h("p", { class: "section-label" }, t("act.settings.title")),
        h("div", { class: "card" },
            h("label", { class: "row-title", for: keywords.id }, t("act.settings.keywords")),
            h("p", { class: "row-sub", id: `${id}-kw-hint`, style: "white-space:normal" }, t("act.settings.keywordsHint")),
            keywords,
            h("div", { class: "toggle" },
                h("span", { class: "row-main" }, h("label", { class: "row-title", for: undo.id }, t("act.settings.undo")), h("span", { class: "row-sub", id: `${id}-undo-hint`, style: "white-space:normal" }, t("act.settings.undoHint"))),
                undo),
            h("label", { class: "toggle" },
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("act.settings.joinLeave")), h("span", { class: "row-sub", style: "white-space:normal" }, t("act.settings.joinLeaveHint"))),
                joinLeave)));
}
