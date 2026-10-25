// dm.js — Direct message (05): end-to-end encrypted conversation with
// attachments, voice messages, typing, receipts and the header tools.
// Bodies render as rich text; attachments upload to the server-wide scope
// (as the desktop does for direct messages).
import { app, queueMessage, startCall } from "../app.js";
import { callLog, callsWith } from "../core/call-log.js";
import { isMuted, setMuted } from "../core/conversation-prefs.js";
import { Trust } from "../core/direct.js";
import { uploadAttachment } from "../core/files.js";
import { remove as removeQueued } from "../core/outbox.js";
import { t } from "../i18n.js";
import { avatar, clock, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { header } from "./common.js";
import { presenceSub } from "./presence-ui.js";
import { menu, receiptTicks, retryProps, sendState, typingText } from "./conversation-tools.js";
import { deleteLocalHistory, showCalls, showSafetyNumber } from "./dm-tools.js";
import { voiceNoteButton } from "./voice-note.js";
import { announcementBanner } from "./announcement.js";
import { attachControls, emojiButton } from "./composer-extras.js";
import { aboutEncryption, encryptionBadge, lineCache, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";

export function renderDM(root, peer) {
    const session = app.session;
    const dm = app.dm;
    const tag = `dm:${peer}`;
    const nickname = () => dm.conversations.get(peer)?.nickname || session.clients().find(c => c.unique_id === peer)?.nickname || t("dm.someone");
    const name = nickname();
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": name });
    const banner = h("div", { class: "e2e-banner", role: "note" }, icon("lock"), " ", t("dm.encrypted"));
    const notice = h("div");
    const deviceNotice = h("div");
    const typing = h("div", { class: "typing", role: "status" });
    const input = h("textarea", { rows: "1", "aria-label": t("dm.placeholder", { name }), placeholder: t("dm.placeholder", { name }), enterkeyhint: "send" });
    const sendButton = h("button", { class: "send", type: "submit", "aria-label": t("chat.send") }, icon("send"));
    // The attach sheet (file, photo, camera) of composer-extras.js; DM files live
    // in the server-wide scope (channel 0), as on the desktop.
    const { button: attachButton, picker } = attachControls(async file => {
        try {
            const token = await uploadAttachment(session.conn, 0, file);
            const caption = input.value.trim();
            input.value = "";
            await sendText(caption ? `${caption} ${token}` : token);
        } catch (error) {
            toast(t("file.uploadFailed", { error: error.code ? t(`dm.error.${error.code}`) : error.message }), "warn");
            if (error.code) checkTrust();
        }
    });
    let calls = [];
    let query = "";

    // --- Search within this conversation ---------------------------------------
    const searchInput = h("input", { class: "input", type: "search", "aria-label": t("dmTools.searchLabel", { name }), placeholder: t("dmTools.searchPlaceholder") });
    const searchBar = h("div", { class: "conv-search", hidden: true }, searchInput,
        h("button", { class: "btn", type: "button", onclick: () => toggleSearch(false) }, t("conv.close")));
    searchInput.addEventListener("input", () => { query = searchInput.value.trim().toLocaleLowerCase(); render(); });
    function toggleSearch(on) {
        searchBar.hidden = !on;
        if (on) searchInput.focus();
        else { searchInput.value = ""; query = ""; render(); }
    }

    // --- Messages ------------------------------------------------------------------
    function retryActions(message) {
        return {
            retry: async () => {
                dm.showQueued(peer, { id: message.id, text: message.body || "", sentAt: message.sentAt });
                await queueMessage({ kind: "dm", peer, nickname: name }, message.body || "", { id: message.id, sentAt: message.sentAt });
            },
            discard: async () => {
                await removeQueued(message.id).catch(() => {});
                dm.discardLocal(peer, message.id);
            },
        };
    }

    const line = lineCache(message => {
        const classes = ["msg", message.self ? "own" : "", message.pending ? "pending" : "", message.queued ? "queued" : "", message.failed ? "failed" : ""].join(" ");
        const body = message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
            : message.body === null || message.body === undefined
                ? h("span", { class: "msg-locked" }, t("chat.locked"))
                : richBody(message.body, { session, conn: session.conn, channelID: 0 });
        const from = message.self ? session.nickname : (message.fromNickname || name);
        const changeable = message.self && !message.deleted && !message.pending && !message.queued && !message.failed;
        const props = message.failed ? retryProps(message, retryActions(message))
            : changeable ? { tabindex: "0", role: "button", "aria-label": t("dm.messageActions"), onclick: event => {
                // Taps on the voice player, an attachment, a link or media are not message actions.
                if (!event.target.closest(".voice-note, .file-chip, a, button, img, video")) messageActions(message);
            } } : {};
        return h("div", { class: classes, dataset: { id: String(message.id ?? "") }, ...props },
            timeNode(message.sentAt),
            avatar(from, "sm", message.self ? session.uniqueID : peer),
            h("div", { class: "msg-body" },
                h("span", { class: "msg-from" }, from),
                message.deleted ? null : encryptionBadge("e2e"),
                body,
                message.edited ? h("span", { class: "msg-edited" }, t("chat.edited")) : null,
                message.self && !message.deleted ? receiptTicks(message.receipt) : null,
                sendState(message)));
    });

    function callLine(record) {
        return h("div", { class: "msg-system" }, icon("phone"),
            h("span", {}, t("calls.missedLine", { name, time: clock(record.startedAt / 1000) })),
            h("button", { class: "btn btn-small", type: "button", onclick: () => startCall(peer).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, t("calls.callBackShort")));
    }

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = dm.conversations.get(peer)?.messages || [];
        let entries;
        if (query) {
            entries = messages.filter(m => !m.deleted && typeof m.body === "string" && m.body.toLocaleLowerCase().includes(query)).map(m => line(m));
            if (!entries.length) entries = [h("p", { class: "hint conv-empty" }, t("dmTools.noMatches"))];
        } else {
            // Missed calls appear in the conversation at their time.
            const missed = calls.filter(r => r.direction === "in" && r.outcome === "missed").map(r => ({ at: r.startedAt / 1000, node: () => callLine(r) }));
            const items = [...messages.map(m => ({ at: m.sentAt, node: () => line(m) })), ...missed].sort((a, b) => a.at - b.at);
            entries = items.length ? withDays(items, item => item.at, item => item.node()) : [h("p", { class: "hint conv-empty" }, t("dm.empty"))];
        }
        log.replaceChildren(...entries);
        if (!query && (atBottom || messages.at(-1)?.self)) log.scrollTop = log.scrollHeight;
        readNow();
    }

    // Read receipts go out while the conversation is on screen.
    function readNow() {
        if (document.visibilityState === "visible" && app.viewing === tag) dm.markRead(peer);
    }

    function renderTyping() {
        const typer = dm.typingName(peer);
        typing.textContent = typer === null ? "" : typingText([typer || name]);
    }

    function setNotice(text, action) {
        notice.replaceChildren(text ? h("div", { class: "card notice", role: "status" }, h("p", { class: "hint" }, text), action || null) : "");
    }

    function paintBanner(contact) {
        const verified = contact.trust === Trust.ok && contact.verified;
        banner.replaceChildren(icon(verified ? "shield" : "lock"), " ",
            contact.fingerprint ? t("dm.encryptedWith", { fingerprint: contact.fingerprint }) : t("dm.encrypted"),
            ...(verified ? [h("span", { class: "verified-badge" }, t("safety.verified"))] : []));
        banner.classList.toggle("verified", verified);
    }

    function paintDevices(contact) {
        if (contact.trust !== Trust.ok || !contact.newDevices?.length) {
            deviceNotice.replaceChildren();
            return;
        }
        deviceNotice.replaceChildren(h("div", { class: "device-notice", role: "status" },
            h("span", {}, t("dm.newDevice", { name })),
            h("button", { class: "btn btn-small", type: "button", onclick: async () => {
                await dm.acknowledgeDevices(peer).catch(() => {});
                deviceNotice.replaceChildren();
            } }, t("call.dismiss"))));
    }

    async function checkTrust() {
        try {
            const [contact, own] = await Promise.all([dm.devicesOf(peer, { refresh: true }), dm.devicesOf(session.uniqueID, { refresh: true })]);
            paintBanner(contact);
            paintDevices(contact);
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

    /** Sends text; a lost connection keeps it in the outbox with the same ID. */
    async function sendText(text) {
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
    }

    const voiceButton = voiceNoteButton({
        upload: file => uploadAttachment(session.conn, 0, file),
        send: token => sendText(token),
        disabled: () => input.disabled,
    });

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
        await sendText(text);
    } }, attachButton, emojiButton(input, session), picker, input, voiceButton, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            form.requestSubmit();
        }
    });
    input.addEventListener("input", () => {
        input.style.height = "";
        input.style.height = `${Math.min(input.scrollHeight, 140)}px`;
        if (input.value.trim() && !editing) dm.sendTyping(peer);
    });

    const exportLoad = async () => ({
        messages: (dm.conversations.get(peer)?.messages || []).filter(m => !m.pending && !m.queued && !m.failed).map(m => ({
            id: m.id, from: m.self ? session.nickname : (m.fromNickname || name), fromUniqueID: m.from, sentAt: m.sentAt,
            text: m.deleted ? "" : m.body, deleted: !!m.deleted, edited: !!m.edited, encryption: "e2e",
        })),
        complete: true,
    });
    // --- Header menu -------------------------------------------------------------------
    const more = h("button", { class: "icon-btn", type: "button", "aria-label": t("conv.more"), "aria-haspopup": "menu" }, icon("more"));
    more.onclick = async () => {
        const muted = isMuted(tag);
        const choice = await menu(name, [
            { label: t("safety.title"), value: "safety" },
            { label: t("dmTools.search"), value: "search" },
            { label: t("calls.title"), value: "calls" },
            { label: t(muted ? "conv.unmute" : "conv.mute"), value: "mute" },
            { label: t("rich.exportHistory"), value: "export" },
            { label: t("rich.aboutEncryption"), value: "encryption" },
            { label: t("dmTools.delete"), value: "delete", danger: true },
        ]);
        if (choice === "safety") showSafetyNumber(peer, name, () => dm.devicesOf(peer).then(paintBanner)).catch(error => toast(t("common.error", { error: error.message }), "warn"));
        else if (choice === "search") toggleSearch(true);
        else if (choice === "calls") showCalls(peer, name);
        else if (choice === "mute") { setMuted(tag, !muted); paintTitle(); toast(t(muted ? "conv.unmuted" : "conv.muted", { name })); }
        else if (choice === "export") import("./export.js").then(({ openExport }) => openExport({ title: name, kind: "dm", load: exportLoad }));
        else if (choice === "encryption") aboutEncryption();
        else if (choice === "delete" && await deleteLocalHistory(peer, name)) { calls = []; render(); }
    };
    const call = h("button", { class: "icon-btn", type: "button", "aria-label": t("call.start", { name }), onclick: () => startCall(peer).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"));
    const top = header(name, { back: "#/home", sub: presenceSub(peer), action: h("span", { class: "header-actions" }, call, more) });
    function paintTitle() {
        top.querySelector("h1 .muted-mark")?.remove();
        if (isMuted(tag)) top.querySelector("h1").append(h("span", { class: "muted-mark", role: "img", "aria-label": t("conv.mutedLabel") }, icon("bellOff")));
    }
    paintTitle();

    const progress = h("div", { class: "history-progress", role: "status" });
    const onMessages = event => { if (event.detail.peer === peer) render(); };
    const onHistory = event => {
        const { done, total } = event.detail;
        progress.textContent = done < total ? t("dm.historyCopying", { done, total }) : "";
        render();
    };
    let typingTimer = 0;
    const onTyping = event => {
        if (event.detail.peer !== peer) return;
        renderTyping();
        clearTimeout(typingTimer);
        typingTimer = setTimeout(renderTyping, 5100);
    };
    const onCalls = async event => {
        if (event.detail.peer !== peer) return;
        calls = await callsWith(peer);
        render();
    };
    const onVisible = () => readNow();
    dm.addEventListener("messages", onMessages);
    dm.addEventListener("receipt", onMessages);
    dm.addEventListener("history", onHistory);
    dm.addEventListener("typing", onTyping);
    callLog.addEventListener("change", onCalls);
    document.addEventListener("visibilitychange", onVisible);
    const rerender = () => { line.clear(); render(); };
    const stops = [watchCustomEmoji(session, rerender), watchDisplaySettings(rerender), relativeTicker(log)];
    root.replaceChildren(h("section", { class: "screen chat" },
        top, banner, announcementBanner(session), notice, deviceNotice, searchBar, progress, log, typing, editBar, form));
    dm.ready.then(render);
    callsWith(peer).then(list => { calls = list; render(); }).catch(() => {});
    checkTrust();
    return () => {
        clearTimeout(typingTimer);
        dm.removeEventListener("messages", onMessages);
        dm.removeEventListener("receipt", onMessages);
        dm.removeEventListener("history", onHistory);
        dm.removeEventListener("typing", onTyping);
        callLog.removeEventListener("change", onCalls);
        document.removeEventListener("visibilitychange", onVisible);
        stops.forEach(stop => stop());
    };
}
