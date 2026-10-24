// dm.js — Direct message (05): end-to-end encrypted conversation.
import { app } from "../app.js";
import { Trust } from "../core/direct.js";
import { t } from "../i18n.js";
import { avatar, clock, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header } from "./common.js";

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

    function line(message) {
        const classes = ["msg", message.self ? "own" : "", message.pending ? "pending" : "", message.failed ? "failed" : ""].join(" ");
        const body = message.body === null || message.body === undefined
            ? h("span", { class: "msg-locked" }, t("chat.locked"))
            : message.body;
        const from = message.self ? session.nickname : (message.fromNickname || name);
        return h("div", { class: classes },
            h("span", { class: "msg-time" }, clock(message.sentAt)),
            avatar(from, "sm"),
            h("div", { class: "msg-body" },
                h("span", { class: "msg-from" }, from),
                body,
                message.failed ? h("div", { class: "error" }, t("chat.failed")) : null));
    }

    function render() {
        const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
        const messages = dm.conversations.get(peer)?.messages || [];
        log.replaceChildren(...(messages.length ? messages.map(line) : [h("p", { class: "hint", style: "text-align:center;margin:24px 0" }, t("dm.empty"))]));
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
            input.disabled = sendButton.disabled = blocked;
        } catch (error) {
            setNotice(t("common.error", { error: error.message }));
        }
    }

    const form = h("form", { class: "composer", onsubmit: async event => {
        event.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        input.style.height = "";
        try {
            await dm.send(peer, text, name);
        } catch (error) {
            toast(t("chat.sendFailed", { error: error.code ? t(`dm.error.${error.code}`) : error.message }), "warn");
            checkTrust();
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
    root.replaceChildren(h("section", { class: "screen chat" },
        header(name, { back: "#/home" }),
        banner, notice, progress, log, form));
    dm.ready.then(render);
    checkTrust();
    return () => {
        dm.removeEventListener("messages", onMessages);
        dm.removeEventListener("history", onHistory);
    };
}
