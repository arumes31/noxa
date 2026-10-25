// voice-chat.js — the channel's text chat as a collapsible panel inside the
// voice room: the latest messages and a composer, without leaving the call.
import { t } from "../i18n.js";
import { clock, h, toast } from "./dom.js";
import { messageBody } from "./attachments.js";
import "./voice-strings.js";

const SHOWN = 40;

/** Returns {node, stop}. */
export function voiceChat(session, channelID) {
    const log = h("div", { class: "vc-chat-log", role: "log", "aria-live": "polite", "aria-label": t("vc.chat") });
    const input = h("input", { class: "input", type: "text", enterkeyhint: "send", "aria-label": t("vc.chatPlaceholder"), placeholder: t("vc.chatPlaceholder") });
    const count = h("span", { class: "vc-chat-count" });
    let seen = (session.messages.get(channelID) || []).length;
    let loaded = false;
    const details = h("details", { class: "card vc-chat" },
        h("summary", {}, h("span", {}, t("vc.chat")), count),
        log,
        h("form", { class: "vc-chat-form", onsubmit: async event => {
            event.preventDefault();
            const text = input.value.trim();
            if (!text) return;
            input.value = "";
            try { await session.send(channelID, text); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        } }, input, h("button", { class: "btn btn-primary", type: "submit" }, t("vc.chatSend"))));

    function render() {
        const list = session.messages.get(channelID) || [];
        if (details.open) seen = list.length;
        const unread = list.length - seen;
        count.textContent = unread > 0 ? String(unread) : "";
        count.hidden = unread <= 0;
        if (!details.open) return;
        const stick = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        log.replaceChildren(...(list.length ? list.slice(-SHOWN).map(message => h("div", { class: `vc-chat-line${message.own ? " own" : ""}` },
            h("span", { class: "vc-chat-meta" }, h("b", {}, message.from || "?"), " ", clock(message.sentAt)),
            message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
                : message.text === null || message.text === undefined ? h("span", { class: "msg-locked" }, t("chat.locked"))
                    : h("span", { class: "vc-chat-text" }, messageBody(message.text, { conn: session.conn, channelID }))))
            : [h("p", { class: "hint" }, t("vc.chatEmpty"))]));
        if (stick) log.scrollTop = log.scrollHeight;
    }

    details.addEventListener("toggle", () => {
        if (details.open && !loaded && !(session.messages.get(channelID) || []).length) {
            loaded = true;
            session.loadHistory(channelID).catch(() => {}).then(render);
        }
        render();
        if (details.open) log.scrollTop = log.scrollHeight;
    });
    const onMessages = event => { if (event.detail?.scope === channelID) render(); };
    session.addEventListener("messages", onMessages);
    render();
    return { node: details, stop: () => session.removeEventListener("messages", onMessages) };
}
