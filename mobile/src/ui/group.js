// group.js — a private group: encrypted history, sending (with the outbox),
// attachments, voice messages, typing, "Seen by" and the group sheet.
import { app, queueMessage, secureMessagingStatus, startGroupCall } from "../app.js";
import { isMuted } from "../core/conversation-prefs.js";
import { uploadAttachment } from "../core/files.js";
import { legacyID, seenMarkers } from "../core/groups.js";
import { remove as removeQueued } from "../core/outbox.js";
import { t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header } from "./common.js";
import { menu, retryProps, sendState, typingText } from "./conversation-tools.js";
import { memberName, showGroupManage } from "./group-manage.js";
import { voiceNoteButton } from "./voice-note.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { announcementBanner } from "./announcement.js";
import { attachControls, emojiButton } from "./composer-extras.js";
import { actionSheet, bindMessageTaps, canShare, copyText, excerpt, saveMessage, shareText } from "./chat-actions.js";
import { aboutEncryption, blockedLine, encryptionBadge, secureBanner, isBlockedUser, lineCache, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";
import { groupEntry } from "../core/search.js";
import { chatSearch } from "./chat-search.js";

const EXPORT_PAGES = 20;


export function renderGroup(root, id) {
    const groups = app.groups;
    const tag = `conversation:${id}`;
    const conversation = () => groups.list.get(id);
    const title = conversation()?.name || t("group.title");
    const folder = { channelID: 0, folder: `private-groups/${id}` };
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": title });
    const notice = h("div");
    const typing = h("div", { class: "typing", role: "status" });
    const input = h("textarea", { rows: "1", "aria-label": t("dm.placeholder", { name: title }), placeholder: t("dm.placeholder", { name: title }), enterkeyhint: "send" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    const { button: attachButton, picker } = attachControls(async file => {
        try {
            const token = await uploadAttachment(app.session.conn, folder, file);
            const caption = input.value.trim();
            input.value = "";
            await send(caption ? `${caption} ${token}` : token);
        } catch (error) {
            toast(t("file.uploadFailed", { error: error.message }), "warn");
        }
    });
    const shown = new Set(); // blocked messages the user chose to show
    let hasOlder = true;
    const older = h("button", { class: "btn", style: "align-self:center;margin:8px 0" }, t("chat.loadOlder"));
    older.onclick = async () => {
        older.disabled = true;
        try { hasOlder = await groups.loadOlder(id); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        older.disabled = false;
        render();
    };

    function retryActions(message) {
        return {
            retry: async () => {
                groups.showQueued(id, { reference: message.reference, text: message.text || "", sentAt: message.created_at });
                await queueMessage({ kind: "conversation", conversation: id }, message.text || "", { id: message.reference, sentAt: message.created_at });
            },
            discard: async () => {
                await removeQueued(message.reference).catch(() => {});
                groups.discardLocal(id, message.reference);
            },
        };
    }

    async function messageActions(message) {
        const hasText = typeof message.text === "string" && !!message.text;
        // Only the owner may delete messages in a group (netproto.ChangeConversation).
        // Ratcheted records are deleted only by their authenticated sender.
        const deletable = !message.local && !message.deleted && (message.protocol ? message.from_unique_id === app.session.uniqueID : conversation()?.owner === app.session.uniqueID);
        const choice = await actionSheet({
            title: t("act.messageFrom", { name: memberName(message.from_unique_id) }), subtitle: excerpt(message.text, 160),
            actions: [
                hasText ? { label: t("act.copy"), value: "copy" } : null,
                hasText && canShare() ? { label: t("act.share"), value: "share" } : null,
                // Saved items resolve legacy numeric history only.
                legacyID(message) ? { label: t("act.save"), value: "save" } : null,
                deletable ? { label: t("dm.delete"), value: "delete", danger: true } : null,
            ].filter(Boolean),
        });
        if (choice === "copy") copyText(message.text);
        else if (choice === "share") shareText(message.text);
        else if (choice === "save") saveMessage({ kind: "group", groupID: id, messageID: message.id }).catch(error => toast(t("act.failed", { error: error.message }), "warn"));
        else if (choice === "delete") {
            const sure = await sheet(t("dm.deleteTitle"), t("groupManage.deleteMessageBody"), [{ label: t("dm.delete"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
            if (sure) await groups.deleteMessage(id, message.id).catch(error => toast(t("common.error", { error: error.message }), "warn"));
        }
    }

    // seen is part of the cache key: "Seen by" changes without the message changing.
    const line = lineCache((message, seen) => {
        const own = message.from_unique_id === app.session.uniqueID;
        if (!own && isBlockedUser(message.from_unique_id) && !shown.has(message.id)) {
            return blockedLine(message, message.created_at, () => { shown.add(message.id); line.forget(message); render(); });
        }
        const body = message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
            : message.text === null || message.text === undefined ? h("span", { class: "msg-locked" }, t("group.locked"))
                : richBody(message.text, { conversation: "group", session: app.session, conn: app.session.conn, channelID: folder });
        // A locked placeholder's sender is unauthenticated: never attributed.
        const from = message.unattributed ? t("chat.lockedSender") : memberName(message.from_unique_id);
        const classes = ["msg", own ? "own" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : ""].join(" ");
        // Not sent: tapping offers Retry or Delete; otherwise the action sheet (bindMessageTaps).
        const props = message.failed ? retryProps(message, retryActions(message)) : { tabindex: message.deleted ? null : "0", "aria-haspopup": message.deleted ? null : "dialog" };
        const el = h("div", { class: classes, dataset: { id: String(message.id ?? "") }, ...props },
            timeNode(message.created_at),
            avatar(from, "sm", message.from_unique_id),
            h("div", { class: "msg-body" }, h("span", { class: "msg-from" }, from), message.deleted ? null : encryptionBadge("e2e", message), body,
                own ? sendState(message) : null,
                seen?.length ? h("div", { class: "msg-seen" }, t("conv.seenBy", { names: seen.map(memberName).join(", ") })) : null));
        el.noxaMessage = message.deleted || message.failed ? null : message;
        return el;
    });
    bindMessageTaps(log, message => messageActions(message));

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = groups.messages.get(id) || [];
        const markers = seenMarkers(messages, conversation()?.members, app.session.uniqueID);
        log.replaceChildren(hasOlder ? older : h("span"), ...withDays(messages, m => m.created_at, m => line(m, markers.get(m.id))));
        if (atBottom || messages.at(-1)?.from_unique_id === app.session.uniqueID) log.scrollTop = log.scrollHeight;
        const members = (conversation()?.members || []).filter(m => !m.pending).map(m => memberName(m.unique_id));
        notice.replaceChildren(h("p", { class: "hint group-members" }, members.join(", ")));
        paintTitle();
        readNow();
    }

    function readNow() {
        if (document.visibilityState === "visible" && app.viewing === tag) groups.markRead(id);
    }

    let typingTimer = 0;
    function renderTyping() {
        typing.textContent = typingText(groups.typingNames(id));
        clearTimeout(typingTimer);
        if (typing.textContent) typingTimer = setTimeout(renderTyping, 1000);
    }

    async function send(text) {
        const reference = crypto.randomUUID();
        const sentAt = Date.now() / 1000;
        try {
            await groups.send(id, text, { reference, sentAt });
        } catch (error) {
            if (error.retryable) {
                await queueMessage({ kind: "conversation", conversation: id }, text, { id: reference, sentAt });
                return;
            }
            toast(t("chat.sendFailed", { error: error.code ? t(`dm.error.${error.code}`) : error.message }), "warn");
        }
    }

    /** Loads up to EXPORT_PAGES older pages and returns the normalized log. */
    async function exportLoad() {
        for (let page = 0; page < EXPORT_PAGES && hasOlder; page++) hasOlder = await groups.loadOlder(id);
        render();
        const messages = (groups.messages.get(id) || []).map(m => ({
            id: m.id, from: memberName(m.from_unique_id), fromUniqueID: m.from_unique_id, sentAt: m.created_at, text: m.text, deleted: !!m.deleted, edited: !!m.edited, encryption: "e2e",
        }));
        return { messages, complete: !hasOlder };
    }
    const voiceButton = voiceNoteButton({ upload: file => uploadAttachment(app.session.conn, folder, file), send: token => send(token) });
    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        await send(text);
    } }, attachButton, emojiButton(input, app.session), picker, input, voiceButton, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            form.requestSubmit();
        }
    });
    input.addEventListener("input", () => {
        input.style.height = "";
        input.style.height = `${Math.min(input.scrollHeight, 140)}px`;
        if (input.value.trim()) groups.sendTyping(id);
    });

    const search = chatSearch({
        tag: `conversation:${id}`, log,
        entries: () => (groups.messages.get(id) || []).map(m => groupEntry(id, m, memberName)),
        loadOlder: async () => {
            if (hasOlder) hasOlder = await groups.loadOlder(id);
            render();
            return hasOlder;
        },
        ensure: async messageID => {
            const has = () => (groups.messages.get(id) || []).some(m => m.id === messageID);
            for (let page = 0; page < 50 && !has() && hasOlder; page++) hasOlder = await groups.loadOlder(id);
            render();
            return has();
        },
    });
    const call = h("button", { class: "icon-btn", type: "button", "aria-label": t("group.call"), onclick: () => startGroupCall(id).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"));
    // One header menu: the group sheet (members, roles, mute, leave), export and encryption.
    const more = h("button", { class: "icon-btn", type: "button", "aria-label": t("conv.more"), "aria-haspopup": "menu" }, icon("more"));
    more.onclick = async () => {
        const choice = await menu(conversation()?.name || title, [
            { label: t("groupManage.title"), value: "manage" },
            { label: t("rich.exportHistory"), value: "export" },
            { label: t("rich.aboutEncryption"), value: "encryption" },
        ]);
        if (choice === "manage") showGroupManage(id, { onGone: () => { location.hash = "#/home"; } });
        else if (choice === "export") import("./export.js").then(({ openExport }) => openExport({ title: conversation()?.name || title, kind: "group", load: exportLoad }));
        else if (choice === "encryption") aboutEncryption();
    };
    const top = header(title, { back: "#/home", action: h("span", { class: "header-actions" }, search.button, call, more) });
    function paintTitle() {
        const heading = top.querySelector("h1");
        const name = conversation()?.name;
        if (name && heading.firstChild?.nodeType === Node.TEXT_NODE && heading.firstChild.textContent !== name) heading.firstChild.textContent = name;
        heading.querySelector(".muted-mark")?.remove();
        if (isMuted(tag)) heading.append(h("span", { class: "muted-mark", role: "img", "aria-label": t("conv.mutedLabel") }, icon("bellOff")));
    }
    root.replaceChildren(h("section", { class: "screen chat" },
        top,
        secureBanner(secureMessagingStatus()),
        announcementBanner(app.session), notice, search.bar, log, typing, form));
    const rerender = () => { line.clear(); render(); };
    const stops = [watchCustomEmoji(app.session, rerender), watchDisplaySettings(rerender), relativeTicker(log), search.stop];
    const onMessages = event => { if (event.detail.id === id) render(); };
    const onList = () => render();
    const onTyping = event => { if (event.detail.id === id) renderTyping(); };
    const onVisible = () => readNow();
    groups.addEventListener("messages", onMessages);
    groups.addEventListener("list", onList);
    groups.addEventListener("seen", onMessages);
    groups.addEventListener("typing", onTyping);
    document.addEventListener("visibilitychange", onVisible);
    groups.get(id).then(render).catch(() => {});
    if (!(groups.messages.get(id) || []).some(m => !m.local)) older.click();
    else groups.loadNewer(id).catch(() => {});
    render();
    return () => {
        clearTimeout(typingTimer);
        groups.removeEventListener("messages", onMessages);
        groups.removeEventListener("list", onList);
        groups.removeEventListener("seen", onMessages);
        groups.removeEventListener("typing", onTyping);
        document.removeEventListener("visibilitychange", onVisible);
        stops.forEach(stop => stop());
    };
}
