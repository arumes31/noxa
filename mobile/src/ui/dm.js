// dm.js — Direct message (05): end-to-end encrypted conversation.
// Bodies render as rich text; attachments upload to the server-wide scope
// (as the desktop does for direct messages).
import { app, queueMessage, startCall } from "../app.js";
import { Trust } from "../core/direct.js";
import { uploadAttachment } from "../core/files.js";
import { t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { header } from "./common.js";
import { announcementBanner } from "./announcement.js";
import { attachControls, emojiButton } from "./composer-extras.js";
import { dmEntry } from "../core/search.js";
import { chatSearch } from "./chat-search.js";
import { aboutEncryption, encryptionBadge, headerActions, lineCache, menuButton, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";

export function renderDM(root, peer) {
    const session = app.session;
    const dm = app.dm;
    const nickname = () => dm.conversations.get(peer)?.nickname || session.clients().find(c => c.unique_id === peer)?.nickname || t("dm.someone");
    const name = nickname();
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": name });
    const banner = h("div", { class: "e2e-banner", role: "note" }, icon("lock"), " ", t("dm.encrypted"));
    const notice = h("div");
    const input = h("textarea", { rows: "1", "aria-label": t("dm.placeholder", { name }), placeholder: t("dm.placeholder", { name }), enterkeyhint: "send" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    const { button: attachButton, picker } = attachControls(async file => {
        try {
            const token = await uploadAttachment(session.conn, 0, file);
            const caption = input.value.trim();
            input.value = "";
            await dm.send(peer, caption ? `${caption} ${token}` : token, name);
        } catch (error) {
            toast(t("file.uploadFailed", { error: error.code ? t(`dm.error.${error.code}`) : error.message }), "warn");
            if (error.code) checkTrust();
        }
    });

    const line = lineCache(message => {
        const classes = ["msg", message.self ? "own" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : ""].join(" ");
        const body = message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
            : message.body === null || message.body === undefined
                ? h("span", { class: "msg-locked" }, t("chat.locked"))
                : richBody(message.body, { session, conn: session.conn, channelID: 0 });
        const from = message.self ? session.nickname : (message.fromNickname || name);
        const changeable = message.self && !message.deleted && !message.pending && !message.queued && !message.failed;
        return h("div", { class: classes, dataset: { id: String(message.id ?? "") }, tabindex: changeable ? "0" : null, role: changeable ? "button" : null,
            "aria-label": changeable ? t("dm.messageActions") : null, onclick: changeable ? event => { if (!event.target.closest("a, button, img, video")) messageActions(message); } : null },
            timeNode(message.sentAt),
            avatar(from, "sm"),
            h("div", { class: "msg-body" },
                h("span", { class: "msg-from" }, from),
                message.deleted ? null : encryptionBadge("e2e"),
                body,
                message.edited ? h("span", { class: "msg-edited" }, t("chat.edited")) : null,
                message.queued ? h("div", { class: "msg-state" }, t("outbox.waiting")) : null,
                message.failed ? h("div", { class: "error" }, t("outbox.notSent")) : null));
    });

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = dm.conversations.get(peer)?.messages || [];
        log.replaceChildren(...(messages.length ? withDays(messages, m => m.sentAt, line) : [h("p", { class: "hint", style: "text-align:center;margin:24px 0" }, t("dm.empty"))]));
        if (atBottom || messages.at(-1)?.self) log.scrollTop = log.scrollHeight;
    }

    function setNotice(text, action) {
        notice.replaceChildren(text ? h("div", { class: "card notice", role: "status" }, h("p", { class: "hint" }, text), action || null) : "");
    }

    async function checkTrust() {
        try {
            const [contact, own] = await Promise.all([dm.devicesOf(peer, { refresh: true }), dm.devicesOf(session.uniqueID, { refresh: true })]);
            banner.replaceChildren(icon("lock"), " ", contact.fingerprint ? t("dm.encryptedWith", { fingerprint: contact.fingerprint }) : t("dm.encrypted"));
            let blocked = true;
            if (contact.trust === Trust.changed) {
                setNotice(t("dm.identityChanged", { name }), h("button", { class: "btn", onclick: async () => {
                    const ok = await sheet(t("dm.trustTitle"), t("dm.trustBody", { name }), [{ label: t("dm.trust"), value: true, primary: true }, { label: t("link.cancel"), value: false }]);
                    if (ok) { await dm.trustNewIdentity(peer); checkTrust(); }
                } }, t("dm.trust")));
            } else if (!contact.devices.length) {
                setNotice(t("dm.unavailable", { name }));
            } else if (!own.devices.some(d => d.deviceID === session.deviceID)) {
                setNotice(t("dm.unconfirmed"));
            } else {
                setNotice("");
                blocked = false;
            }
            input.disabled = sendButton.disabled = attachButton.disabled = blocked;
        } catch (error) {
            setNotice(t("common.error", { error: error.message }));
        }
    }

    // Editing reuses the composer; Cancel returns it to sending.
    let editing = null;
    const editBar = h("div", { class: "edit-bar", hidden: true }, h("span", {}, t("dm.editing")),
        h("button", { class: "btn", type: "button", onclick: () => stopEditing() }, t("link.cancel")));
    function stopEditing() {
        editing = null;
        editBar.hidden = true;
        input.value = "";
    }
    async function messageActions(message) {
        const choice = await sheet(t("dm.messageActions"), null, [
            { label: t("dm.edit"), value: "edit", primary: true },
            { label: t("dm.delete"), value: "delete", danger: true },
            { label: t("link.cancel"), value: null },
        ]);
        if (choice === "edit") {
            editing = message.id;
            editBar.hidden = false;
            input.value = message.body || "";
            input.focus();
        } else if (choice === "delete") {
            const sure = await sheet(t("dm.deleteTitle"), t("dm.deleteBody"), [{ label: t("dm.delete"), value: true, danger: true }, { label: t("link.cancel"), value: false }]);
            if (sure) await dm.deleteMessage(peer, message.id).catch(error => toast(t("chat.sendFailed", { error: error.message }), "warn"));
        }
    }

    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        if (editing) {
            const ref = editing;
            stopEditing();
            await dm.editMessage(peer, ref, text).catch(error => toast(t("chat.sendFailed", { error: error.message }), "warn"));
            return;
        }
        const id = crypto.randomUUID();
        const sentAt = Date.now() / 1000;
        try {
            await dm.send(peer, text, name, { id, sentAt });
        } catch (error) {
            if (error.retryable) {
                await queueMessage({ kind: "dm", peer, nickname: name }, text, { id, sentAt });
                return;
            }
            toast(t("chat.sendFailed", { error: error.code ? t(`dm.error.${error.code}`) : error.message }), "warn");
            checkTrust();
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
    });

    const exportLoad = async () => ({
        messages: (dm.conversations.get(peer)?.messages || []).filter(m => !m.pending && !m.queued && !m.failed).map(m => ({
            id: m.id, from: m.self ? session.nickname : (m.fromNickname || name), fromUniqueID: m.from, sentAt: m.sentAt,
            text: m.deleted ? "" : m.body, deleted: !!m.deleted, edited: !!m.edited, encryption: "e2e",
        })),
        complete: true,
    });
    const menu = menuButton([
        { label: t("rich.aboutEncryption"), run: aboutEncryption },
        { label: t("rich.exportHistory"), run: () => import("./export.js").then(({ openExport }) => openExport({ title: name, kind: "dm", load: exportLoad })) },
    ]);

    // Direct messages all live on the phone: nothing older to load.
    const search = chatSearch({
        tag: `dm:${peer}`, log,
        entries: () => (dm.conversations.get(peer)?.messages || []).filter(m => !m.pending && !m.queued && !m.failed)
            .map(m => dmEntry(peer, m, { selfName: session.nickname, selfUID: session.uniqueID, peerName: nickname() })),
        loadOlder: async () => false,
    });
    const progress = h("div", { class: "history-progress", role: "status" });
    const onMessages = event => { if (event.detail.peer === peer) render(); };
    const onHistory = event => {
        const { done, total } = event.detail;
        progress.textContent = done < total ? t("dm.historyCopying", { done, total }) : "";
        render();
    };
    dm.addEventListener("messages", onMessages);
    dm.addEventListener("history", onHistory);
    const rerender = () => { line.clear(); render(); };
    const stops = [watchCustomEmoji(session, rerender), watchDisplaySettings(rerender), relativeTicker(log), search.stop];
    const call = h("button", { class: "icon-btn", type: "button", "aria-label": t("call.start", { name }), onclick: () => startCall(peer).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"));
    root.replaceChildren(h("section", { class: "screen chat" },
        header(name, { back: "#/home", action: headerActions(search.button, call, menu) }),
        banner, announcementBanner(session), notice, progress, search.bar, log, editBar, form));
    dm.ready.then(render);
    checkTrust();
    return () => {
        dm.removeEventListener("messages", onMessages);
        dm.removeEventListener("history", onHistory);
        stops.forEach(stop => stop());
    };
}
