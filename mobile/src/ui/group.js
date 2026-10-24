// group.js — a private group: encrypted history, sending, attachments.
import { app, startGroupCall } from "../app.js";
import { uploadAttachment } from "../core/files.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header } from "./common.js";
import { richBody, watchCustomEmoji } from "./rich-text.js";
import { announcementBanner } from "./announcement.js";
import { attachControls, emojiButton } from "./composer-extras.js";
import { aboutEncryption, blockedLine, encryptionBadge, headerActions, isBlockedUser, lineCache, menuButton, relativeTicker, timeNode, watchDisplaySettings, withDays } from "./message-view.js";

const EXPORT_PAGES = 20;

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

    const line = lineCache(message => {
        const own = message.from_unique_id === app.session.uniqueID;
        if (!own && isBlockedUser(message.from_unique_id) && !shown.has(message.id)) {
            return blockedLine(message, message.created_at, () => { shown.add(message.id); line.forget(message); render(); });
        }
        const body = message.deleted ? h("span", { class: "msg-deleted" }, t("chat.deleted"))
            : message.text === null || message.text === undefined ? h("span", { class: "msg-locked" }, t("group.locked"))
                : richBody(message.text, { session: app.session, conn: app.session.conn, channelID: folder });
        const from = nameOf(message.from_unique_id);
        return h("div", { class: `msg${own ? " own" : ""}`, dataset: { id: String(message.id ?? "") } },
            timeNode(message.created_at),
            avatar(from, "sm"),
            h("div", { class: "msg-body" }, h("span", { class: "msg-from" }, from), message.deleted ? null : encryptionBadge("e2e"), body));
    });

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = groups.messages.get(id) || [];
        log.replaceChildren(hasOlder ? older : h("span"), ...withDays(messages, m => m.created_at, line));
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

    /** Loads up to EXPORT_PAGES older pages and returns the normalized log. */
    async function exportLoad() {
        for (let page = 0; page < EXPORT_PAGES && hasOlder; page++) hasOlder = await groups.loadOlder(id);
        render();
        const messages = (groups.messages.get(id) || []).map(m => ({
            id: m.id, from: nameOf(m.from_unique_id), fromUniqueID: m.from_unique_id, sentAt: m.created_at, text: m.text, deleted: !!m.deleted, edited: !!m.edited, encryption: "e2e",
        }));
        return { messages, complete: !hasOlder };
    }
    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        await send(text);
    } }, attachButton, emojiButton(input, app.session), picker, input, sendButton);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            form.requestSubmit();
        }
    });

    const call = h("button", { class: "icon-btn", type: "button", "aria-label": t("group.call"), onclick: () => startGroupCall(id).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"));
    const menu = menuButton([
        { label: t("rich.aboutEncryption"), run: aboutEncryption },
        { label: t("rich.exportHistory"), run: () => import("./export.js").then(({ openExport }) => openExport({ title, kind: "group", load: exportLoad })) },
    ]);
    root.replaceChildren(h("section", { class: "screen chat" },
        header(title, { back: "#/home", action: headerActions(call, menu) }),
        h("div", { class: "e2e-banner", role: "note" }, icon("lock"), " ", t("dm.encrypted")),
        announcementBanner(app.session), notice, log, form));
    const rerender = () => { line.clear(); render(); };
    const stops = [watchCustomEmoji(app.session, rerender), watchDisplaySettings(rerender), relativeTicker(log)];
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
        stops.forEach(stop => stop());
    };
}
