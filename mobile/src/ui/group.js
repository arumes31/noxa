// group.js — a private group: encrypted history, sending, attachments.
import { app, startGroupCall } from "../app.js";
import { uploadAttachment } from "../core/files.js";
import { t } from "../i18n.js";
import { messageBody } from "./attachments.js";
import { avatar, clock, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header } from "./common.js";

function nameOf(uid) {
    if (uid === app.session?.uniqueID) return app.session.nickname;
    return app.session?.clients().find(c => c.unique_id === uid)?.nickname || app.dm?.conversations.get(uid)?.nickname || t("dm.someone");
}

export function renderGroup(root, id) {
    const groups = app.groups;
    const conversation = () => groups.list.get(id);
    const title = conversation()?.name || t("group.title");
    const folder = { channelID: 0, folder: `private-groups/${id}` };
    const log = h("div", { class: "chat-log", role: "log", "aria-live": "polite", "aria-label": title });
    const notice = h("div");
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

    function line(message) {
        const own = message.from_unique_id === app.session.uniqueID;
        const body = message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
            : message.text === null || message.text === undefined ? h("span", { class: "msg-locked" }, t("group.locked"))
                : messageBody(message.text, { conn: app.session.conn, channelID: folder });
        const from = nameOf(message.from_unique_id);
        return h("div", { class: `msg${own ? " own" : ""}` },
            h("span", { class: "msg-time" }, clock(message.created_at)),
            avatar(from, "sm", message.from_unique_id),
            h("div", { class: "msg-body" }, h("span", { class: "msg-from" }, from), body));
    }

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = groups.messages.get(id) || [];
        log.replaceChildren(hasOlder ? older : h("span"), ...messages.map(line));
        if (atBottom || messages.at(-1)?.from_unique_id === app.session.uniqueID) log.scrollTop = log.scrollHeight;
        const members = (conversation()?.members || []).filter(m => !m.pending).map(m => nameOf(m.unique_id));
        notice.replaceChildren(h("p", { class: "hint group-members" }, members.join(", ")));
    }

    async function send(text) {
        try {
            await groups.send(id, text);
            groups.markRead(id);
        } catch (error) {
            toast(t("chat.sendFailed", { error: error.code ? t(`dm.error.${error.code}`) : error.message }), "warn");
        }
    }

    picker.onchange = async () => {
        const file = picker.files?.[0];
        picker.value = "";
        if (!file) return;
        attachButton.disabled = true;
        try {
            const token = await uploadAttachment(app.session.conn, folder, file);
            const caption = input.value.trim();
            input.value = "";
            await send(caption ? `${caption} ${token}` : token);
        } catch (error) {
            toast(t("file.uploadFailed", { error: error.message }), "warn");
        } finally {
            attachButton.disabled = false;
        }
    };
    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        await send(text);
    } }, attachButton, picker, input, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            form.requestSubmit();
        }
    });

    const call = h("button", { class: "icon-btn", type: "button", "aria-label": t("group.call"), onclick: () => startGroupCall(id).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"));
    root.replaceChildren(h("section", { class: "screen chat" },
        header(title, { back: "#/home", action: call }),
        h("div", { class: "e2e-banner", role: "note" }, icon("lock"), " ", t("dm.encrypted")),
        notice, log, form));
    const onMessages = event => { if (event.detail.id === id) { render(); groups.markRead(id); } };
    const onList = () => render();
    groups.addEventListener("messages", onMessages);
    groups.addEventListener("list", onList);
    groups.get(id).then(render).catch(() => {});
    if (!(groups.messages.get(id) || []).length) older.click();
    else groups.loadNewer(id).then(() => groups.markRead(id)).catch(() => {});
    render();
    return () => {
        groups.removeEventListener("messages", onMessages);
        groups.removeEventListener("list", onList);
    };
}
