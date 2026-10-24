// chat.js — Channel chat (04): history, live messages, typing, sending.
import { app, queueMessage } from "../app.js";
import { t } from "../i18n.js";
import { avatar, clock, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header, scopeName } from "./common.js";

export function renderChat(root, scope) {
    const session = app.session;
    const name = scopeName(session, scope);
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": name });
    const typing = h("div", { class: "typing" });
    const input = h("textarea", { rows: "1", "aria-label": t("chat.placeholder", { channel: name }), placeholder: t("chat.placeholder", { channel: name }), enterkeyhint: "send" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    let hasOlder = true;
    let lastTyping = 0;

    const older = h("button", { class: "btn", style: "align-self:center;margin:8px 0" }, t("chat.loadOlder"));
    older.onclick = async () => {
        older.disabled = true;
        try { hasOlder = await session.loadHistory(scope); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        older.disabled = false;
        render(false);
    };

    function line(message) {
        const classes = ["msg", message.own ? "own" : "", message.mentioned ? "mention" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : ""].join(" ");
        let body;
        if (message.deleted) body = h("span", { class: "msg-deleted" }, t("chat.deleted"));
        else if (message.text === null || message.text === undefined) body = h("span", { class: "msg-locked" }, t("chat.locked"));
        else body = message.text;
        const reactions = Object.entries(message.reactions || {}).filter(([, n]) => n > 0);
        return h("div", { class: classes },
            h("span", { class: "msg-time" }, clock(message.sentAt)),
            avatar(message.from, "sm"),
            h("div", { class: "msg-body" },
                h("span", { class: "msg-from" }, message.from),
                body,
                message.edited ? h("span", { class: "msg-edited" }, t("chat.edited")) : null,
                reactions.length ? h("span", { class: "reactions" }, reactions.map(([emoji, n]) => h("span", { class: "reaction" }, `${emoji} ${n}`))) : null,
                message.queued ? h("div", { class: "msg-state" }, t("outbox.waiting")) : null,
                message.failed ? h("div", { class: "error" }, t("outbox.notSent")) : null));
    }

    function render(stick = true) {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = session.messages.get(scope) || [];
        log.replaceChildren(hasOlder ? older : h("span"), ...messages.map(line));
        if (stick && (atBottom || messages.at(-1)?.own)) log.scrollTop = log.scrollHeight;
    }

    function renderTyping() {
        const entry = session.typing.get(scope);
        typing.textContent = entry && entry.until > Date.now() ? t("chat.typing", { name: entry.nickname }) : "";
    }

    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        const id = crypto.randomUUID();
        const sentAt = Date.now() / 1000;
        try {
            await session.send(scope, text, { clientMsgID: id, sentAt });
        } catch (error) {
            if (error.retryable) await queueMessage({ kind: "channel", scope }, text, { id, sentAt });
            else toast(t("chat.sendFailed", { error: error.message }), "warn");
        }
    } }, input, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            form.requestSubmit();
        }
    });
    input.addEventListener("input", () => {
        input.style.height = "";
        input.style.height = `${Math.min(input.scrollHeight, 140)}px`;
        if (Date.now() - lastTyping > 3000) {
            lastTyping = Date.now();
            session.sendTyping(scope);
        }
    });

    const onMessages = event => { if (event.detail.scope === scope) render(); };
    const onTyping = event => { if (event.detail.scope === scope) { renderTyping(); setTimeout(renderTyping, 5100); } };
    session.addEventListener("messages", onMessages);
    session.addEventListener("typing", onTyping);
    root.replaceChildren(h("section", { class: "screen chat" },
        header(name, { back: "#/servers" }),
        log, typing, form));
    render();
    if (!(session.messages.get(scope) || []).length) older.click();
    return () => {
        session.removeEventListener("messages", onMessages);
        session.removeEventListener("typing", onTyping);
    };
}
