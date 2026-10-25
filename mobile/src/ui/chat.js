// chat.js — Channel chat (04): history, live messages, typing, sending.
// Bodies render as rich text (ui/rich-text.js). line(message) returns the
// message element (data-id = message ID); per-message actions, replies,
// reactions, pins, mentions and undo send come from ui/chat-actions.js.
import { app, queueMessage } from "../app.js";
import { joinLeaveLog, mergeSystemLines } from "../core/join-leave.js";
import { QUICK_EMOJI } from "../core/markdown.js";
import { isMention } from "../core/mentions.js";
import * as settings from "../core/settings.js";
import { t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { header, scopeName } from "./common.js";
import { uploadAttachment } from "../core/files.js";
import { announcementBanner } from "./announcement.js";
import { attachControls, emojiButton } from "./composer-extras.js";
import { aboutEncryption, blockedLine, encryptionBadge, headerActions, isBlockedUser, lineCache, menuButton, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";
import {
    actionSheet, bindMessageTaps, canShare, composerBar, copyText, excerpt, flash, holdChannelMessage, mentionCompleter, newMessagesPill,
    openThread, pinBadge, pinsSheet, reactionChips, replyQuote, reportMessage, saveMessage, sessionMentionChoices, shareText, undoCountdown,
    undoHeld, unreadDivider,
} from "./chat-actions.js";

const EXPORT_PAGES = 20;
const JUMP_PAGES = 20;
const pinRefused = new WeakSet(); // sessions whose server refused a pin (no manage_messages)
const reducedMotion = () => matchMedia("(prefers-reduced-motion: reduce)").matches;

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
    const systemLines = joinLeaveLog(session);
    const keyOf = message => message.id ?? message.clientMsgID;

    const older = h("button", { class: "btn", style: "align-self:center;margin:8px 0" }, t("chat.loadOlder"));
    older.onclick = async () => {
        older.disabled = true;
        try { hasOlder = await session.loadHistory(scope); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        older.disabled = false;
        render(false);
    };

    // --- Reply / edit state ---------------------------------------------------------
    const bar = composerBar();
    let replyTo = null; // message being answered
    let editing = null; // message being edited
    function startReply(message) {
        stopEdit(false);
        replyTo = message;
        bar.show(t("act.replyingTo", { name: message.from }), excerpt(message.text), t("act.cancelReply"), () => stopReply());
        input.focus();
    }
    function stopReply() {
        replyTo = null;
        bar.hide();
    }
    function startEdit(message) {
        stopReply();
        editing = message;
        bar.show(t("act.editing"), excerpt(message.text), t("act.cancelEdit"), () => stopEdit());
        input.value = message.text || "";
        input.dispatchEvent(new Event("input"));
        input.focus();
    }
    function stopEdit(clear = true) {
        if (!editing) return;
        editing = null;
        bar.hide();
        if (clear) input.value = "";
    }

    // --- Message lines --------------------------------------------------------------
    const missingOriginals = new Set(); // replies whose original is not loaded yet
    const pinned = message => !!message.id && (message.pinned || (session.pins.get(scope) || []).includes(message.id));

    const line = lineCache(message => {
        if (message.system) return systemLine(message);
        const key = keyOf(message);
        if (!message.own && isBlockedUser(message.fromUniqueID) && !shown.has(key)) {
            return blockedLine(message, message.sentAt, () => { shown.add(key); line.forget(message); render(false); });
        }
        const held = message.holdUntil > Date.now();
        const mention = isMention(message, settings.get("keywords"));
        const classes = ["msg", message.own ? "own" : "", mention ? "mention" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : "", held ? "held" : ""].join(" ");
        let body;
        if (message.deleted) body = h("span", { class: "msg-deleted" }, t("chat.deleted"));
        else if (message.text === null || message.text === undefined) body = h("span", { class: "msg-locked" }, t("chat.locked"));
        else body = richBody(message.text, { session, conn: session.conn, channelID: scope });
        let quote = null;
        if (message.replyTo && !message.deleted) {
            const original = session.messageByID(scope, message.replyTo);
            if (!original) missingOriginals.add(message);
            quote = replyQuote(original, () => jumpTo(message.replyTo));
        }
        const el = h("div", { class: classes, tabindex: message.deleted ? null : "0", "aria-haspopup": message.deleted ? null : "dialog", dataset: { id: String(message.id ?? ""), client: message.clientMsgID || "" } },
            timeNode(message.sentAt),
            avatar(message.from, "sm"),
            h("div", { class: "msg-body" },
                quote,
                pinned(message) ? pinBadge() : null,
                h("span", { class: "msg-from" }, message.from),
                message.deleted ? null : encryptionBadge("channel"),
                body,
                message.edited && !message.deleted ? h("span", { class: "msg-edited" }, t("chat.edited")) : null,
                message.id && !message.deleted ? reactionChips(message, { session, onToggle: emoji => react(message, emoji), onAdd: () => pickReaction(message) }) : null,
                held ? undoCountdown(message, () => undoHeld(session, scope, message.clientMsgID).then(ok => { if (ok && !input.value) input.value = message.text; })) : null,
                message.queued ? h("div", { class: "msg-state" }, t("outbox.waiting")) : null,
                message.failed ? h("div", { class: "error" }, t("outbox.notSent")) : null));
        el.noxaMessage = message.deleted ? null : message;
        return el;
    });

    function systemLine(item) {
        const text = entry => t(scope === 0 ? (entry.kind === "joined" ? "act.joinedServer" : "act.leftServer") : (entry.kind === "joined" ? "act.joined" : "act.left"), { name: entry.name });
        if (!item.group) return h("div", { class: "sys-line", role: "note" }, text(item));
        return h("div", { class: "sys-line", role: "note" }, h("details", {},
            h("summary", {}, t("act.joinLeaveRun", { count: item.group.length })),
            h("ul", {}, item.group.map(entry => h("li", {}, text(entry))))));
    }

    // --- Unread divider and the new-messages pill --------------------------------------
    const divider = unreadDivider();
    let unreadFrom = null;
    const tag = `channel:${scope}`;
    const opened = app.lastUnread?.tag === tag ? app.lastUnread.count : 0;
    if (app.lastUnread?.tag === tag) app.lastUnread = null;
    if (opened > 0) {
        const others = (session.messages.get(scope) || []).filter(m => !m.own && m.id);
        const first = others[Math.max(0, others.length - opened)];
        if (first) unreadFrom = keyOf(first);
    }
    let fresh = 0;
    const setFresh = count => { fresh = count; pill.set(count); };
    const pill = newMessagesPill(() => {
        log.scrollTo({ top: log.scrollHeight, behavior: reducedMotion() ? "auto" : "smooth" });
        setFresh(0);
    });
    const nearBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    log.addEventListener("scroll", () => { if (fresh && nearBottom()) setFresh(0); }, { passive: true });
    const fragment = (...nodes) => { const f = document.createDocumentFragment(); f.append(...nodes); return f; };

    function render(stick = true) {
        const atBottom = nearBottom();
        const messages = session.messages.get(scope) || [];
        const lines = settings.get("showJoinLeave") ? systemLines.get(scope) || [] : [];
        const items = lines.length ? mergeSystemLines(messages, lines) : messages;
        const out = withDays(items, m => m.sentAt ?? m.at, item => {
            const el = line(item);
            return unreadFrom !== null && !item.system && keyOf(item) === unreadFrom ? fragment(divider, el) : el;
        });
        log.replaceChildren(hasOlder ? older : h("span"), ...out);
        if (stick && (atBottom || messages.at(-1)?.own)) log.scrollTop = log.scrollHeight;
    }

    function renderTyping() {
        const entry = session.typing.get(scope);
        typing.textContent = entry && entry.until > Date.now() ? t("chat.typing", { name: entry.nickname }) : "";
    }

    // --- Jumping to a message (reply quotes, pins) ------------------------------------------
    async function jumpTo(id) {
        let found = session.messageByID(scope, id);
        if (!found && hasOlder) {
            try {
                found = await session.loadUntil(scope, id, JUMP_PAGES);
            } catch (error) {
                toast(t("common.error", { error: error.message }), "warn");
            }
        }
        render(false);
        const el = log.querySelector(`.msg[data-id="${CSS.escape(String(id))}"]`);
        if (!found || !el) return toast(t("act.notFound"), "warn");
        el.scrollIntoView({ block: "center", behavior: reducedMotion() ? "auto" : "smooth" });
        flash(el);
        el.focus({ preventScroll: true });
    }

    // --- Actions --------------------------------------------------------------------------
    const failed = error => toast(t("act.failed", { error: error.message }), "warn");

    async function react(message, emoji) {
        try { await session.react(scope, message.id, emoji); } catch (error) { failed(error); }
    }
    async function pickReaction(message) {
        const { openEmojiPicker } = await import("./emoji-picker.js");
        const emoji = await openEmojiPicker({ session, title: t("act.react") });
        if (emoji) react(message, emoji);
    }

    async function messageActions(message) {
        const sent = !!message.id;
        const hasText = typeof message.text === "string" && !!message.text;
        const isPinned = pinned(message);
        const actions = [
            sent ? { label: t("act.reply"), value: "reply" } : null,
            hasText ? { label: t("act.copy"), value: "copy" } : null,
            hasText && canShare() ? { label: t("act.share"), value: "share" } : null,
            sent ? { label: t("act.save"), value: "save" } : null,
            sent && !pinRefused.has(session) ? { label: isPinned ? t("act.unpin") : t("act.pin"), value: "pin" } : null,
            sent ? { label: t("act.thread"), value: "thread" } : null,
            sent && message.own && hasText ? { label: t("act.edit"), value: "edit" } : null,
            sent && !message.own && message.fromUniqueID ? { label: t("act.report"), value: "report" } : null,
            // Others' messages need manage_messages; the server decides, the toast tells.
            sent && (message.own || session.isAdmin) ? { label: t("act.delete"), value: "delete", danger: true } : null,
        ].filter(Boolean);
        if (!actions.length) return;
        const choice = await actionSheet({
            title: t("act.messageFrom", { name: message.from }), subtitle: excerpt(message.text, 160),
            quick: sent ? QUICK_EMOJI : null, mine: message.myReactions || [], actions,
        });
        if (!choice) return;
        if (choice.react === "more") return pickReaction(message);
        if (choice.react) return react(message, choice.react);
        switch (choice) {
            case "reply": return startReply(message);
            case "copy": return copyText(message.text);
            case "share": return shareText(message.text);
            case "save": return saveMessage({ kind: "channel", channelID: scope, messageID: message.id }).catch(failed);
            case "thread": return openThread(scope, message.id);
            case "edit": return startEdit(message);
            case "report": return reportMessage(session, { uid: message.fromUniqueID, from: message.from, text: message.text, where: name });
            case "pin":
                try {
                    await session.pin(scope, message.id, !isPinned);
                    toast(isPinned ? t("act.unpinned") : t("act.pinned"));
                } catch (error) {
                    if (error.code === 4) pinRefused.add(session);
                    failed(error);
                }
                return;
            case "delete": {
                const sure = await sheet(t("act.deleteTitle"), t("act.deleteBody"), [{ label: t("act.delete"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
                if (sure) await session.remove(scope, message.id).catch(failed);
                return;
            }
        }
    }
    bindMessageTaps(log, message => messageActions(message));

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
    const showPins = () => pinsSheet(session, scope, { onJump: jumpTo, canUnpin: () => !pinRefused.has(session) });
    const menu = menuButton(() => [
        description ? { label: t("rich.channelInfo"), run: channelInfo } : null,
        { label: t("act.pins"), run: showPins },
        { label: t("rich.aboutEncryption"), run: aboutEncryption },
        { label: t("rich.exportHistory"), run: () => import("./export.js").then(({ openExport }) => openExport({ title: name.replace(/^# /, ""), kind: "channel", load: exportLoad })) },
    ].filter(Boolean));
    const topic = channel?.topic || channel?.description?.split("\n")[0] || "";
    const topicButton = topic ? h("button", { class: "channel-topic", type: "button", "aria-label": `${t("rich.channelInfo")}: ${topic}`, onclick: channelInfo }, topic) : null;
    const slowMode = channel?.slowMode > 0 ? h("p", { class: "slow-mode", role: "note" }, t("rich.slowMode", { seconds: channel.slowMode })) : null;

    // --- Sending ----------------------------------------------------------------------------
    async function sendText(text, reply) {
        const seconds = Number(settings.get("undoSendSeconds")) || 0;
        if (seconds > 0) {
            try { await holdChannelMessage(session, scope, text, { seconds, replyTo: reply }); } catch (error) { toast(t("chat.sendFailed", { error: error.message }), "warn"); }
            return;
        }
        const id = crypto.randomUUID();
        const sentAt = Date.now() / 1000;
        try {
            await session.send(scope, text, { clientMsgID: id, sentAt, replyTo: reply });
        } catch (error) {
            if (error.retryable) await queueMessage({ kind: "channel", scope }, text, { id, sentAt, replyTo: reply });
            else toast(t("chat.sendFailed", { error: error.message }), "warn");
        }
    }

    const mentions = mentionCompleter(input, sessionMentionChoices(session));
    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        mentions.close();
        if (editing) {
            const message = editing;
            stopEdit(false);
            if (text !== message.text) await session.edit(scope, message.id, text).catch(failed);
            return;
        }
        const reply = replyTo?.id || 0;
        stopReply();
        await sendText(text, reply);
    } }, attachButton, emojiButton(input, session), picker, input, sendButton);
    input.addEventListener("keydown", event => {
        if (mentions.handleKey(event)) return;
        if (event.key === "Escape" && (editing || replyTo)) {
            event.preventDefault();
            editing ? stopEdit() : stopReply();
            return;
        }
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

    // --- Events -------------------------------------------------------------------------------
    const rerender = () => { line.clear(); render(false); };
    const onMessages = event => {
        if (event.detail.scope !== scope) return;
        // Replies whose original just arrived get their quote.
        for (const message of missingOriginals) {
            if (session.messageByID(scope, message.replyTo)) { missingOriginals.delete(message); line.forget(message); }
        }
        render();
    };
    const onIncoming = event => {
        if (event.detail.scope !== scope || event.detail.message.own) return;
        if (!nearBottom()) setFresh(fresh + 1);
    };
    const onJoinLeave = event => { if (settings.get("showJoinLeave") && event.detail.scopes.includes(scope)) render(); };
    const onTyping = event => { if (event.detail.scope === scope) { renderTyping(); setTimeout(renderTyping, 5100); } };
    session.addEventListener("messages", onMessages);
    session.addEventListener("incoming", onIncoming);
    session.addEventListener("join-leave", onJoinLeave);
    session.addEventListener("typing", onTyping);
    const stopSettings = settings.onChange(({ changed }) => { if (changed.some(n => n === "keywords" || n === "showJoinLeave")) rerender(); });
    const stops = [watchCustomEmoji(session, rerender), watchDisplaySettings(rerender), relativeTicker(log), stopSettings];
    root.replaceChildren(h("section", { class: "screen chat" },
        header(name, { back: "#/servers", action: headerActions(menu) }),
        topicButton, announcementBanner(session),
        h("div", { class: "chat-log-wrap" }, log, pill.el),
        typing, slowMode, mentions.el, bar.el, form));
    render();
    if (unreadFrom !== null && divider.isConnected) divider.scrollIntoView({ block: "start" });
    if (!(session.messages.get(scope) || []).length) older.click();
    // Pinned IDs for the pin badges; the sheet reloads them when opened.
    session.loadPins(scope).catch(() => {});
    return () => {
        session.removeEventListener("messages", onMessages);
        session.removeEventListener("incoming", onIncoming);
        session.removeEventListener("join-leave", onJoinLeave);
        session.removeEventListener("typing", onTyping);
        stops.forEach(stop => stop());
    };
}
