// conversation-tools.js — pieces the direct message, group and channel
// screens share: a bottom panel with arbitrary content, the retry offer for
// messages that were not sent, receipt ticks, the typing line and the mute
// toggle.
import { isMuted, setMuted } from "../core/conversation-prefs.js";
import { t } from "../i18n.js";
import { h, sheet } from "./dom.js";
import "./conversation-strings.js";
import "./conversation.css";

/**
 * A bottom sheet holding content nodes. Returns {node, close, closed}; closed
 * resolves when the panel goes away (backdrop, Escape, Close or close()).
 */
export function panel(title, content, { label = title } = {}) {
    let resolve;
    const closed = new Promise(r => { resolve = r; });
    const previous = document.activeElement;
    const close = () => {
        if (!overlay.isConnected) return;
        overlay.remove();
        document.removeEventListener("keydown", onKey);
        if (previous?.isConnected) previous.focus?.();
        resolve();
    };
    const onKey = event => { if (event.key === "Escape") close(); };
    const closeButton = h("button", { class: "btn", type: "button", onclick: close }, t("conv.close"));
    const node = h("div", { class: "sheet conv-panel", role: "dialog", "aria-modal": "true", "aria-label": label },
        h("h2", { class: "sheet-title" }, title), content, closeButton);
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } }, node);
    document.body.append(overlay);
    document.addEventListener("keydown", onKey);
    (node.querySelector("input, button:not(:last-child)") || closeButton).focus();
    return { node, close, closed };
}

/** A menu of actions in a panel; resolves with the chosen value or null. */
export function menu(title, items) {
    return new Promise(resolve => {
        let chosen = null;
        const list = h("div", { class: "conv-menu", role: "menu" }, items.filter(Boolean).map(item =>
            h("button", { class: `row conv-menu-item${item.danger ? " danger" : ""}`, type: "button", role: "menuitem",
                onclick: () => { chosen = item.value; view.close(); } }, item.label)));
        const view = panel(title, list);
        view.closed.then(() => resolve(chosen));
    });
}

/**
 * The "Not sent — tap to retry" offer: Retry sends again with the same
 * client message ID (through the outbox), Delete drops the local copy.
 */
export async function offerRetry({ retry, discard }) {
    const choice = await sheet(t("conv.notSentTitle"), t("conv.notSentBody"), [
        { label: t("link.cancel"), value: null },
        { label: t("conv.discard"), value: "discard", danger: true },
        { label: t("conv.retry"), value: "retry", primary: true },
    ]);
    if (choice === "retry") await retry();
    else if (choice === "discard") await discard();
}

/** Props that make a failed message a button offering retry. */
export function retryProps(message, actions) {
    if (!message.failed) return {};
    return { role: "button", tabindex: "0", "aria-label": t("conv.notSentAction"), onclick: () => offerRetry(actions),
        onkeydown: event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); offerRetry(actions); } } };
}

/** The state line of an own message: waiting, or not sent with the tap hint. */
export function sendState(message) {
    if (message.failed) return h("div", { class: "error" }, t("conv.notSentTap"));
    if (message.queued) return h("div", { class: "msg-state" }, t("outbox.waiting"));
    return null;
}

/** ✓ sent, ✓✓ delivered, ✓✓ (accent) read. */
export function receiptTicks(receipt) {
    if (!receipt) return null;
    return h("span", { class: `msg-receipt ${receipt}`, role: "img", "aria-label": t(`conv.receipt.${receipt}`), title: t(`conv.receipt.${receipt}`) },
        receipt === "sent" ? "✓" : "✓✓");
}

/** "X is typing…", "X and Y are typing…", "3 people are typing…". */
export function typingText(names) {
    const list = names.map(name => name || t("dm.someone"));
    if (!list.length) return "";
    if (list.length === 1) return t("chat.typing", { name: list[0] });
    if (list.length === 2) return t("conv.typingTwo", { first: list[0], second: list[1] });
    return t("conv.typingMany", { count: list.length });
}

/** A mute switch for a conversation tag ("dm:<uid>", "conversation:<id>"). */
export function muteToggle(tag) {
    const input = h("input", { type: "checkbox", role: "switch", checked: isMuted(tag) });
    input.onchange = () => setMuted(tag, input.checked);
    return h("label", { class: "toggle" },
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("conv.mute")), h("span", { class: "row-sub" }, t("conv.muteHint"))),
        input);
}

/** Saves text as a file download. */
export function downloadText(name, text) {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
    const link = h("a", { href: url, download: name, hidden: true });
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
}

/** A file-name-safe version of a conversation name. */
export function safeFileName(name) {
    return String(name || "conversation").replace(/[^\p{L}\p{N}._-]+/gu, "_").slice(0, 60) || "conversation";
}
