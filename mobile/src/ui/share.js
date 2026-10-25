// share.js — Share into noXa (Android share sheet → service worker →
// here): shows what arrived and sends it to the conversation the user picks.
import { app } from "../app.js";
import { uploadAttachment } from "../core/files.js";
import { formatSize } from "../core/message-format.js";
import { clearShare, getShare } from "../core/share-store.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { dmHref, header, lastMessage, scopeName } from "./common.js";
import "./rich-strings.js";
import "./rich-text.css";

/** Every conversation the user can send to, most recent first. */
function destinations(session) {
    const out = [];
    for (const scope of session.chatScopes()) {
        out.push({ kind: "channel", scope, at: lastMessage(session, scope)?.sentAt || 0, name: scopeName(session, scope), href: `#/chat/${scope}`, mark: scope === 0 ? "◎" : "#" });
    }
    for (const conversation of app.dm?.list() || []) {
        const name = conversation.nickname || session.clients().find(c => c.unique_id === conversation.peer)?.nickname || t("dm.someone");
        out.push({ kind: "dm", peer: conversation.peer, at: conversation.messages.at(-1)?.sentAt || 0, name, href: dmHref(conversation.peer) });
    }
    for (const group of app.groups?.list.values() || []) {
        if (group.members?.find(m => m.unique_id === session.uniqueID)?.pending) continue;
        out.push({ kind: "group", id: group.id, at: app.groups.messages.get(group.id)?.at(-1)?.created_at || 0, name: group.name, href: `#/group/${encodeURIComponent(group.id)}`, mark: "◇" });
    }
    return out.sort((a, b) => b.at - a.at);
}

/** Uploads the files into the destination's storage and sends them, the text with the first. */
async function deliver(session, target, text, files) {
    const location = target.kind === "channel" ? target.scope : target.kind === "group" ? { channelID: 0, folder: `private-groups/${target.id}` } : 0;
    const send = body => target.kind === "channel" ? session.send(target.scope, body)
        : target.kind === "dm" ? app.dm.send(target.peer, body, target.name)
            : app.groups.send(target.id, body);
    if (!files.length) {
        await send(text);
        return;
    }
    let caption = text;
    for (const file of files) {
        const token = await uploadAttachment(session.conn, location, file);
        await send(caption ? `${caption} ${token}` : token);
        caption = "";
    }
}

export function renderShare(root) {
    const session = app.session;
    const body = h("div", { class: "screen-body share-body" }, h("p", { class: "hint", role: "status" }, t("file.loading")));
    root.replaceChildren(h("section", { class: "screen" }, header(t("rich.share.title"), { back: "#/home" }), body));
    let cancelled = false;
    getShare().catch(() => null).then(share => {
        if (cancelled) return;
        if (!share || (!share.text && !share.files.length)) {
            body.replaceChildren(h("p", { class: "hint" }, t("rich.share.empty")), h("a", { class: "btn", href: "#/home" }, t("nav.home")));
            return;
        }
        const text = h("textarea", { class: "share-text", rows: "3", "aria-label": t("rich.share.text") });
        text.value = share.text;
        const status = h("p", { class: "hint", role: "status" });
        const list = h("ul", { class: "list" });
        const pick = async target => {
            for (const button of list.querySelectorAll("button")) button.disabled = true;
            status.textContent = t("rich.share.sending", { name: target.name });
            try {
                await deliver(session, target, text.value.trim(), share.files);
                await clearShare().catch(() => {});
                toast(t("rich.share.sent", { name: target.name }));
                location.hash = target.href;
            } catch (error) {
                status.textContent = t("chat.sendFailed", { error: error.message });
                for (const button of list.querySelectorAll("button")) button.disabled = false;
            }
        };
        list.append(...destinations(session).map(target => h("li", {},
            h("button", { class: "row", type: "button", onclick: () => pick(target) },
                target.kind === "dm" ? avatar(target.name, "md") : h("span", { class: "hash", "aria-hidden": "true" }, target.mark),
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, target.name),
                    h("span", { class: "row-sub" }, t(`rich.share.kind.${target.kind}`)))))));
        body.replaceChildren(
            h("div", { class: "card share-preview" },
                text,
                share.files.length ? h("ul", { class: "share-files" }, share.files.map(file => h("li", {}, h("span", { class: "file-icon", "aria-hidden": "true" }, "📄"), h("span", { class: "file-name" }, file.name), h("span", { class: "file-size" }, formatSize(file.size))))) : null,
                h("button", { class: "btn", type: "button", onclick: async () => { await clearShare().catch(() => {}); location.hash = "#/home"; } }, t("rich.share.discard"))),
            status,
            h("p", { class: "section-label" }, t("rich.share.to")),
            list.childElementCount ? list : h("p", { class: "hint" }, t("home.empty")));
    });
    return () => { cancelled = true; };
}
