// group.js — a private group: encrypted history, sending (with the outbox),
// attachments, voice messages, typing, "Seen by" and the group sheet.
import { app, queueMessage, startGroupCall } from "../app.js";
import { isMuted } from "../core/conversation-prefs.js";
import { uploadAttachment } from "../core/files.js";
import { seenMarkers } from "../core/groups.js";
import { remove as removeQueued } from "../core/outbox.js";
import { t } from "../i18n.js";
import { messageBody } from "./attachments.js";
import { avatar, clock, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header } from "./common.js";
import { retryProps, sendState, typingText } from "./conversation-tools.js";
import { memberName, showGroupManage } from "./group-manage.js";
import { voiceNoteButton } from "./voice-note.js";

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
    const picker = h("input", { type: "file", hidden: true, "aria-hidden": "true" });
    const attachButton = h("button", { class: "attach", type: "button", "aria-label": t("file.attach"), onclick: () => picker.click() }, icon("attach"));
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
        const choice = await sheet(t("dm.messageActions"), null, [
            { label: t("link.cancel"), value: null },
            { label: t("dm.delete"), value: "delete", danger: true },
        ]);
        if (choice !== "delete") return;
        const sure = await sheet(t("dm.deleteTitle"), t("groupManage.deleteMessageBody"), [{ label: t("dm.delete"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
        if (sure) await groups.deleteMessage(id, message.id).catch(error => toast(t("common.error", { error: error.message }), "warn"));
    }

    function line(message, seen) {
        const own = message.from_unique_id === app.session.uniqueID;
        const body = message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
            : message.text === null || message.text === undefined ? h("span", { class: "msg-locked" }, t("group.locked"))
                : messageBody(message.text, { conn: app.session.conn, channelID: folder });
        const from = memberName(message.from_unique_id);
        // Only the owner may delete messages in a group (netproto.ChangeConversation).
        const deletable = !message.local && !message.deleted && conversation()?.owner === app.session.uniqueID;
        const props = message.failed ? retryProps(message, retryActions(message))
            : deletable ? { tabindex: "0", role: "button", "aria-label": t("dm.messageActions"), onclick: event => {
                if (!event.target.closest(".voice-note, .file-chip, a")) messageActions(message);
            } } : {};
        const classes = ["msg", own ? "own" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : ""].join(" ");
        return h("div", { class: classes, ...props },
            h("span", { class: "msg-time" }, clock(message.created_at)),
            avatar(from, "sm", message.from_unique_id),
            h("div", { class: "msg-body" }, h("span", { class: "msg-from" }, from), body,
                own ? sendState(message) : null,
                seen?.length ? h("div", { class: "msg-seen" }, t("conv.seenBy", { names: seen.map(memberName).join(", ") })) : null));
    }

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = groups.messages.get(id) || [];
        const markers = seenMarkers(messages, conversation()?.members, app.session.uniqueID);
        log.replaceChildren(hasOlder ? older : h("span"), ...messages.map(m => line(m, markers.get(m.id))));
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

    picker.onchange = async () => {
        const file = picker.files?.[0];
        picker.value = "";
        if (!file) return;
        attachButton.disabled = true;
        attachButton.setAttribute("aria-busy", "true");
        try {
            const token = await uploadAttachment(app.session.conn, folder, file);
            const caption = input.value.trim();
            input.value = "";
            await send(caption ? `${caption} ${token}` : token);
        } catch (error) {
            toast(t("file.uploadFailed", { error: error.message }), "warn");
        } finally {
            attachButton.disabled = false;
            attachButton.removeAttribute("aria-busy");
        }
    };
    const voiceButton = voiceNoteButton({ upload: file => uploadAttachment(app.session.conn, folder, file), send: token => send(token) });
    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        await send(text);
    } }, attachButton, picker, input, voiceButton, sendButton);
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

    const call = h("button", { class: "icon-btn", type: "button", "aria-label": t("group.call"), onclick: () => startGroupCall(id).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"));
    const manage = h("button", { class: "icon-btn", type: "button", "aria-label": t("groupManage.title"), "aria-haspopup": "dialog",
        onclick: () => showGroupManage(id, { onGone: () => { location.hash = "#/home"; } }) }, icon("more"));
    const top = header(title, { back: "#/home", action: h("span", { class: "header-actions" }, call, manage) });
    function paintTitle() {
        const heading = top.querySelector("h1");
        const name = conversation()?.name;
        if (name && heading.firstChild?.nodeType === Node.TEXT_NODE && heading.firstChild.textContent !== name) heading.firstChild.textContent = name;
        heading.querySelector(".muted-mark")?.remove();
        if (isMuted(tag)) heading.append(h("span", { class: "muted-mark", role: "img", "aria-label": t("conv.mutedLabel") }, icon("bellOff")));
    }
    root.replaceChildren(h("section", { class: "screen chat" },
        top,
        h("div", { class: "e2e-banner", role: "note" }, icon("lock"), " ", t("dm.encrypted")),
        notice, log, typing, form));
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
    };
}
