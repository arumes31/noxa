// chat.js — Channel chat (04): history, live messages, typing, sending.
// Bodies render as rich text (ui/rich-text.js). line(message) returns the
// message element (data-id = message ID) so per-message actions can attach.
import { app, queueMessage } from "../app.js";
import { t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { header, scopeName } from "./common.js";
import { uploadAttachment } from "../core/files.js";
import { announcementBanner } from "./announcement.js";
import { attachControls, emojiButton } from "./composer-extras.js";
import { aboutEncryption, blockedLine, encryptionBadge, headerActions, isBlockedUser, lineCache, menuButton, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";

const EXPORT_PAGES = 20;

export function renderChat(root, scope) {
    const session = app.session;
    const name = scopeName(session, scope);
    const channel = scope > 0 ? session.channels().find(c => c.id === scope) : null;
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": name });
    const typing = h("div", { class: "typing" });
    const input = h("textarea", { rows: "1", "aria-label": t("chat.placeholder", { channel: name }), placeholder: t("chat.placeholder", { channel: name }), enterkeyhint: "send" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    const { button: attachButton, picker } = attachControls(async file => {
        try {
            const token = await uploadAttachment(session.conn, scope, file);
            const caption = input.value.trim();
            input.value = "";
            await session.send(scope, caption ? `${caption} ${token}` : token);
        } catch (error) {
            toast(t("file.uploadFailed", { error: error.message }), "warn");
        }
    });
    let hasOlder = true;
    let lastTyping = 0;
    const shown = new Set(); // blocked messages the user chose to show

    const older = h("button", { class: "btn", style: "align-self:center;margin:8px 0" }, t("chat.loadOlder"));
    older.onclick = async () => {
        older.disabled = true;
        try { hasOlder = await session.loadHistory(scope); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        older.disabled = false;
        render(false);
    };

    const line = lineCache(message => {
        const key = message.id ?? message.clientMsgID;
        if (!message.own && isBlockedUser(message.fromUniqueID) && !shown.has(key)) {
            return blockedLine(message, message.sentAt, () => { shown.add(key); line.forget(message); render(false); });
        }
        const classes = ["msg", message.own ? "own" : "", message.mentioned ? "mention" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : ""].join(" ");
        let body;
        if (message.deleted) body = h("span", { class: "msg-deleted" }, t("chat.deleted"));
        else if (message.text === null || message.text === undefined) body = h("span", { class: "msg-locked" }, t("chat.locked"));
        else body = richBody(message.text, { session, conn: session.conn, channelID: scope });
        const reactions = Object.entries(message.reactions || {}).filter(([, n]) => n > 0);
        return h("div", { class: classes, dataset: { id: String(message.id ?? ""), client: message.clientMsgID || "" } },
            timeNode(message.sentAt),
            avatar(message.from, "sm"),
            h("div", { class: "msg-body" },
                h("span", { class: "msg-from" }, message.from),
                message.deleted ? null : encryptionBadge("channel"),
                body,
                message.edited ? h("span", { class: "msg-edited" }, t("chat.edited")) : null,
                reactions.length ? h("span", { class: "reactions" }, reactions.map(([emoji, n]) => h("span", { class: "reaction" }, `${emoji} ${n}`))) : null,
                message.queued ? h("div", { class: "msg-state" }, t("outbox.waiting")) : null,
                message.failed ? h("div", { class: "error" }, t("outbox.notSent")) : null));
    });

    function render(stick = true) {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = session.messages.get(scope) || [];
        log.replaceChildren(hasOlder ? older : h("span"), ...withDays(messages, m => m.sentAt, line));
        if (stick && (atBottom || messages.at(-1)?.own)) log.scrollTop = log.scrollHeight;
    }

    function renderTyping() {
        const entry = session.typing.get(scope);
        typing.textContent = entry && entry.until > Date.now() ? t("chat.typing", { name: entry.nickname }) : "";
    }

    /** Loads up to EXPORT_PAGES older pages and returns the normalized log. */
    async function exportLoad() {
        for (let page = 0; page < EXPORT_PAGES && hasOlder; page++) hasOlder = await session.loadHistory(scope);
        render(false);
        const messages = (session.messages.get(scope) || []).filter(m => m.id).map(m => ({
            id: m.id, from: m.from, fromUniqueID: m.fromUniqueID, sentAt: m.sentAt, text: m.text, deleted: !!m.deleted, edited: !!m.edited, encryption: "server",
        }));
        return { messages, complete: !hasOlder };
    }

    const description = [channel?.topic, channel?.description].filter(Boolean).join("\n\n");
    const channelInfo = () => sheet(name, description, [{ label: t("common.close"), value: null, primary: true }]);
    const menu = menuButton(() => [
        description ? { label: t("rich.channelInfo"), run: channelInfo } : null,
        { label: t("rich.aboutEncryption"), run: aboutEncryption },
        { label: t("rich.exportHistory"), run: () => import("./export.js").then(({ openExport }) => openExport({ title: name.replace(/^# /, ""), kind: "channel", load: exportLoad })) },
    ].filter(Boolean));
    const topic = channel?.topic || channel?.description?.split("\n")[0] || "";
    const topicButton = topic ? h("button", { class: "channel-topic", type: "button", "aria-label": `${t("rich.channelInfo")}: ${topic}`, onclick: channelInfo }, topic) : null;
    const slowMode = channel?.slowMode > 0 ? h("p", { class: "slow-mode", role: "note" }, t("rich.slowMode", { seconds: channel.slowMode })) : null;

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
    } }, attachButton, emojiButton(input, session), picker, input, sendButton);
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

    const rerender = () => { line.clear(); render(false); };
    const onMessages = event => { if (event.detail.scope === scope) render(); };
    const onTyping = event => { if (event.detail.scope === scope) { renderTyping(); setTimeout(renderTyping, 5100); } };
    session.addEventListener("messages", onMessages);
    session.addEventListener("typing", onTyping);
    const stops = [watchCustomEmoji(session, rerender), watchDisplaySettings(rerender), relativeTicker(log)];
    root.replaceChildren(h("section", { class: "screen chat" },
        header(name, { back: "#/servers", action: headerActions(menu) }),
        topicButton, announcementBanner(session),
        log, typing, slowMode, form));
    render();
    if (!(session.messages.get(scope) || []).length) older.click();
    return () => {
        session.removeEventListener("messages", onMessages);
        session.removeEventListener("typing", onTyping);
        stops.forEach(stop => stop());
    };
}
