// chat-actions.js — per-message actions and composer helpers shared by the
// channel chat, direct messages and groups: the action sheet (reply, quick
// reactions, copy, share, save, pin, edit, delete, thread, report), reply
// quotes, reaction chips, the reply/edit bar, mention completion, the pins
// sheet, unread markers and the undo-send countdown.
import { app, flushNow } from "../app.js";
import { parseFileTokens } from "../core/files.js";
import { Msg } from "../core/frame.js";
import { insertMention, mentionAt, mentionChoices, tabComplete } from "../core/mentions.js";
import { enqueue, remove as removeQueued } from "../core/outbox.js";
import { SavedMessages } from "../core/saved.js";
import { DelayedSends } from "../core/undo-send.js";
import { t } from "../i18n.js";
import { avatar, clock, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import "./chat-actions-strings.js";
import "./chat-actions.css";

// --- Text helpers ------------------------------------------------------------------

/** A message's text for copying and quoting: attachment tokens become their file names. */
export function plainMessageText(text) {
    if (typeof text !== "string") return "";
    let out = text;
    for (const file of parseFileTokens(text)) out = out.replace(file.token, `📎 ${file.name}`);
    return out.trim();
}

/** The first non-empty line, shortened. */
export function excerpt(text, max = 90) {
    const first = plainMessageText(text).split("\n").find(line => line.trim()) || "";
    return first.length > max ? `${first.slice(0, max - 1)}…` : first;
}

// --- Sheets -------------------------------------------------------------------------

function overlay(label, children, onClose) {
    const opener = document.activeElement;
    const onKey = event => { if (event.key === "Escape") close(null); };
    let done = false;
    const close = value => {
        if (done) return;
        done = true;
        node.remove();
        document.removeEventListener("keydown", onKey);
        opener?.focus?.({ preventScroll: true });
        onClose(value);
    };
    const node = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === node) close(null); } },
        h("div", { class: "sheet act-sheet", role: "dialog", "aria-modal": "true", "aria-label": label }, children(close)));
    document.addEventListener("keydown", onKey);
    document.body.append(node);
    node.querySelector("input, textarea, .btn, button")?.focus({ preventScroll: true });
    return { node, close };
}

/**
 * The message action sheet. options: {title, subtitle, quick: [emoji] or
 * null, mine: [emoji], actions: [{label, value, danger}]}. Resolves with an
 * action value, {react: emoji}, {react: "more"} or null.
 */
export function actionSheet({ title, subtitle = "", quick = null, mine = [], actions = [] }) {
    return new Promise(resolve => {
        overlay(title, close => [
            h("h2", { class: "sheet-title" }, title),
            subtitle ? h("p", { class: "sheet-body act-excerpt" }, subtitle) : null,
            quick ? h("div", { class: "act-quick", role: "group", "aria-label": t("act.react") },
                quick.map(emoji => h("button", { class: `act-emoji${mine.includes(emoji) ? " mine" : ""}`, type: "button", "aria-pressed": String(mine.includes(emoji)), "aria-label": t("act.reactWith", { emoji }), onclick: () => close({ react: emoji }) }, emoji)),
                h("button", { class: "act-emoji act-more", type: "button", "aria-label": t("act.moreEmoji"), onclick: () => close({ react: "more" }) }, "+")) : null,
            h("div", { class: "act-list" },
                actions.map(action => h("button", { class: `btn act-item${action.danger ? " btn-danger" : ""}`, type: "button", onclick: () => close(action.value) }, action.label)),
                h("button", { class: "btn act-item", type: "button", onclick: () => close(null) }, t("link.cancel"))),
        ], resolve);
    });
}

/** Asks for one line (or several) of text. Resolves with the text or null. */
export function promptSheet(title, { label, value = "", multiline = false, confirm = t("act.confirm") } = {}) {
    return new Promise(resolve => {
        overlay(title, close => {
            const field = h(multiline ? "textarea" : "input", { class: "act-field", "aria-label": label || title, rows: multiline ? "4" : null, maxlength: "500" });
            field.value = value;
            return [
                h("h2", { class: "sheet-title" }, title),
                label ? h("p", { class: "sheet-body" }, label) : null,
                h("form", { class: "act-prompt", onsubmit: event => { event.preventDefault(); close(field.value); } },
                    field,
                    h("div", { class: "act-row" },
                        h("button", { class: "btn", type: "button", onclick: () => close(null) }, t("link.cancel")),
                        h("button", { class: "btn btn-primary", type: "submit" }, confirm))),
            ];
        }, resolve);
    });
}

// --- Copy, share, save, report, thread ---------------------------------------------

export async function copyText(text) {
    try {
        await navigator.clipboard.writeText(plainMessageText(text));
        toast(t("rich.copied"));
    } catch {
        toast(t("act.copyFailed"), "warn");
    }
}

export const canShare = () => typeof navigator.share === "function";

export async function shareText(text) {
    try {
        await navigator.share({ text: plainMessageText(text) });
    } catch (error) {
        if (error?.name !== "AbortError") toast(t("act.failed", { error: error.message }), "warn");
    }
}

/** The saved-messages store; another screen may have created it already. */
export function savedStore() {
    app.saved ||= new SavedMessages();
    return app.saved;
}

/** Asks for a collection and saves the reference there. */
export async function saveMessage(reference) {
    const store = savedStore();
    await store.ready;
    const names = store.collections();
    const choice = await actionSheet({
        title: t("act.saveTo"),
        actions: [...names.map(name => ({ label: name || t("act.savedDefault"), value: { name } })), { label: t("act.newCollection"), value: { create: true } }],
    });
    if (!choice) return;
    let name = choice.name ?? "";
    if (choice.create) {
        const typed = await promptSheet(t("act.newCollection"), { label: t("act.collectionName") });
        if (!typed?.trim()) return;
        name = typed.trim().slice(0, 60);
    }
    await store.save(reference, name);
    toast(t("act.savedTo", { name: name || t("act.savedDefault") }));
}

/** Files a complaint against a message's author (Msg.Complaint; no reply on success). */
export async function reportMessage(session, { uid, from, text, where }) {
    const reason = await promptSheet(t("act.reportTitle", { name: from }), {
        label: t("act.reportReason"), value: t("act.reportDefault", { where, text: excerpt(text, 200) }), multiline: true, confirm: t("act.report"),
    });
    if (!reason?.trim()) return;
    try {
        session.conn.send(Msg.Complaint, { target_unique_id: uid, reason: reason.trim().slice(0, 500) });
        toast(t("act.reported"));
    } catch (error) {
        toast(t("act.failed", { error: error.message }), "warn");
    }
}

/** Asks the thread view (another module) to open a message's thread. */
export function openThread(scope, messageID) {
    window.dispatchEvent(new CustomEvent("noxa:open-thread", { detail: { scope, messageID } }));
}

// --- Message pieces ------------------------------------------------------------------

function emojiFace(emoji, session) {
    const custom = /^:([A-Za-z0-9_+-]+):$/.exec(emoji);
    if (!custom || !session) return h("span", { class: "reaction-emoji" }, emoji);
    const img = h("img", { class: "custom-emoji", alt: emoji, width: "18", height: "18" });
    session.emojiImage(custom[1]).then(url => { if (url.startsWith("data:image/")) img.src = url; }).catch(() => {});
    return img;
}

/** Reaction chips under a message; null when there are none. */
export function reactionChips(message, { session, onToggle, onAdd }) {
    const entries = Object.entries(message.reactions || {}).filter(([, count]) => count > 0);
    if (!entries.length) return null;
    const mine = new Set(message.myReactions || []);
    return h("div", { class: "reactions", role: "group", "aria-label": t("act.reactions") },
        entries.map(([emoji, count]) => h("button", {
            class: `reaction${mine.has(emoji) ? " mine" : ""}`, type: "button", "aria-pressed": String(mine.has(emoji)),
            "aria-label": t("act.reactionChip", { emoji, count }), onclick: () => onToggle(emoji),
        }, emojiFace(emoji, session), h("span", { class: "reaction-count" }, String(count)))),
        onAdd ? h("button", { class: "reaction reaction-add", type: "button", "aria-label": t("act.addReaction"), onclick: onAdd }, "+") : null);
}

/** The compact quote above a reply; original may be null while not loaded. */
export function replyQuote(original, onJump) {
    const name = original?.from || "";
    const text = original ? (original.deleted ? t("chat.deleted") : excerpt(original.text) || t("act.original")) : t("act.original");
    return h("button", { class: "reply-quote", type: "button", "aria-label": t("act.jumpTo", { name: name || "…" }), onclick: onJump },
        icon("reply"),
        name ? h("span", { class: "reply-from" }, name) : null,
        h("span", { class: "reply-text" }, text));
}

export function pinBadge() {
    return h("span", { class: "pin-badge", role: "img", title: t("act.pinnedBadge"), "aria-label": t("act.pinnedBadge") }, icon("pin"));
}

/** Briefly highlights a message that was jumped to. */
export function flash(el) {
    el.classList.remove("flash");
    void el.offsetWidth;
    el.classList.add("flash");
    setTimeout(() => el.classList.remove("flash"), 2000);
}

/**
 * Opens actions on tap, long press (contextmenu) and Enter for message
 * elements that carry el.noxaMessage. Taps on links, media, buttons and
 * code blocks keep their own behaviour.
 */
export function bindMessageTaps(log, open) {
    const target = event => {
        if (event.target.closest("a, button, img, video, audio, input, textarea, select, .md-pre, .md-table-wrap, .link-preview")) return null;
        const el = event.target.closest(".msg");
        return el?.noxaMessage ? el : null;
    };
    log.addEventListener("click", event => {
        const el = target(event);
        if (!el || String(window.getSelection?.() || "")) return;
        open(el.noxaMessage, el);
    });
    log.addEventListener("contextmenu", event => {
        const el = target(event);
        if (!el) return;
        event.preventDefault();
        open(el.noxaMessage, el);
    });
    log.addEventListener("keydown", event => {
        if ((event.key === "Enter" || event.key === " ") && event.target.classList?.contains("msg") && event.target.noxaMessage) {
            event.preventDefault();
            open(event.target.noxaMessage, event.target);
        }
    });
}

// --- Composer bar (reply / edit) ------------------------------------------------------

/** The bar above the composer for a reply or an edit. */
export function composerBar() {
    const el = h("div", { class: "compose-bar", hidden: true, role: "status" });
    return {
        el,
        show(label, detail, cancelLabel, onCancel) {
            el.replaceChildren(
                h("span", { class: "compose-bar-main" }, h("span", { class: "compose-bar-title" }, label), detail ? h("span", { class: "compose-bar-detail" }, detail) : null),
                h("button", { class: "icon-btn", type: "button", "aria-label": cancelLabel, onclick: onCancel }, icon("close")));
            el.hidden = false;
        },
        hide() {
            el.hidden = true;
            el.replaceChildren();
        },
    };
}

// --- Mention completion -------------------------------------------------------------

let popoverSeq = 0;

/**
 * Mention completion for a composer textarea: a listbox above the composer
 * while "@word" is typed (arrows + Enter/Tab or tap insert), and Tab cycling
 * through the nearest names. choices(query) returns mentionChoices(...).
 * Returns {el, handleKey(event) -> consumed, close}.
 */
export function mentionCompleter(input, choices) {
    const id = `mentions-${++popoverSeq}`;
    const list = h("div", { class: "mention-pop", role: "listbox", id, hidden: true, "aria-label": t("act.mentions") });
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-controls", id);
    input.setAttribute("aria-expanded", "false");
    let items = [];
    let active = 0;
    let at = null;
    let cycle = null;

    const caret = () => input.selectionStart ?? input.value.length;
    const sub = choice => choice.kind === "user" ? t(`act.status.${choice.status}`) : choice.kind === "role" ? t("act.role") : t("act.mass");
    const apply = next => {
        input.value = next.value;
        input.setSelectionRange(next.caret, next.caret);
        input.dispatchEvent(new Event("input", { bubbles: true }));
    };
    const pick = index => {
        const choice = items[index];
        if (!choice || !at) return;
        apply(insertMention(input.value, at.start, caret(), choice.insert));
        close();
        input.focus();
    };
    function close() {
        list.hidden = true;
        items = [];
        input.setAttribute("aria-expanded", "false");
        input.removeAttribute("aria-activedescendant");
    }
    function render() {
        list.replaceChildren(...items.map((choice, index) => h("div", {
            class: `mention-opt${index === active ? " active" : ""}`, role: "option", id: `${id}-${index}`, "aria-selected": String(index === active),
            onmousedown: event => event.preventDefault(), onclick: () => pick(index),
        },
        choice.kind === "user" ? h("span", { class: `mention-avatar status-${choice.status}` }, avatar(choice.label, "sm")) : h("span", { class: "mention-avatar mention-symbol", "aria-hidden": "true" }, "@"),
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, choice.label), h("span", { class: "row-sub" }, sub(choice))))));
        input.setAttribute("aria-activedescendant", `${id}-${active}`);
    }
    function update() {
        at = mentionAt(input.value, caret());
        items = at ? choices(at.query) : [];
        if (!items.length) return close();
        active = Math.min(active, items.length - 1);
        list.hidden = false;
        input.setAttribute("aria-expanded", "true");
        render();
    }
    input.addEventListener("input", () => { if (!cycle || caret() !== cycle.end) cycle = null; update(); });
    input.addEventListener("click", update);
    input.addEventListener("blur", () => setTimeout(close, 150));

    function handleKey(event) {
        if (event.key === "Tab" && !event.shiftKey) {
            const next = tabComplete(input.value, caret(), cycle, choices);
            if (!next) return false;
            event.preventDefault();
            input.value = next.value;
            input.setSelectionRange(next.caret, next.caret);
            cycle = next.cycle;
            close();
            return true;
        }
        if (list.hidden) return false;
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            active = (active + (event.key === "ArrowDown" ? 1 : items.length - 1)) % items.length;
            render();
            return true;
        }
        if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            pick(active);
            return true;
        }
        if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            close();
            return true;
        }
        return false;
    }
    return { el: list, handleKey, close, update };
}

/** mentionChoices bound to a session: online members, roles and mass forms. */
export function sessionMentionChoices(session, { roles = true, mass = true } = {}) {
    return query => mentionChoices(query, { clients: session.clients(), selfUID: session.uniqueID, roles, mass });
}

// --- Pins -----------------------------------------------------------------------------

/** The pinned messages of a scope, with jump and (optionally) unpin. */
export function pinsSheet(session, scope, { onJump, canUnpin }) {
    return new Promise(resolve => {
        const list = h("ul", { class: "list act-pins", "aria-busy": "true" }, h("li", { class: "hint" }, t("act.pinsLoading")));
        const { close } = overlay(t("act.pins"), done => [
            h("div", { class: "emoji-head" }, h("h2", { class: "sheet-title" }, t("act.pins")),
                h("button", { class: "btn", type: "button", onclick: () => done(null) }, t("common.close"))),
            list,
        ], resolve);
        session.loadPins(scope).then(pins => {
            list.removeAttribute("aria-busy");
            if (!pins.length) return list.replaceChildren(h("li", { class: "hint" }, t("act.pinsEmpty")));
            list.replaceChildren(...pins.map(pin => {
                const message = pin.message;
                const item = h("li", { class: "row act-pin" },
                    h("span", { class: "row-main" },
                        h("span", { class: "row-title" }, message?.from || "…"),
                        h("span", { class: "row-sub" }, message ? (message.deleted ? t("chat.deleted") : excerpt(message.text) || t("chat.locked")) : t("act.original")),
                        message?.sentAt ? h("span", { class: "row-meta" }, clock(message.sentAt)) : null),
                    h("button", { class: "btn", type: "button", onclick: () => { close(null); onJump(pin.messageID); } }, t("act.jump")),
                    canUnpin() ? h("button", { class: "btn", type: "button", "aria-label": `${t("act.unpin")}: ${message?.from || ""}`, onclick: async event => {
                        event.currentTarget.disabled = true;
                        try {
                            await session.pin(scope, pin.messageID, false);
                            item.remove();
                            if (!list.children.length) list.replaceChildren(h("li", { class: "hint" }, t("act.pinsEmpty")));
                        } catch (error) {
                            toast(t("act.failed", { error: error.message }), "warn");
                            event.target.disabled = false;
                        }
                    } }, t("act.unpin")) : null);
                return item;
            }));
        }).catch(error => {
            list.removeAttribute("aria-busy");
            list.replaceChildren(h("li", { class: "error" }, t("common.error", { error: error.message })));
        });
    });
}

// --- Unread markers ------------------------------------------------------------------

export function unreadDivider() {
    return h("div", { class: "unread-sep", role: "separator", "aria-label": t("act.newMessages") }, h("span", {}, t("act.newMessages")));
}

/** The "↓ N new" pill; set(0) hides it. */
export function newMessagesPill(onClick) {
    const el = h("button", { class: "new-pill", type: "button", hidden: true, onclick: onClick });
    return {
        el,
        set(count) {
            el.hidden = !count;
            el.textContent = t("act.newPill", { count });
            el.setAttribute("aria-label", t("act.newPillLabel", { count }));
        },
    };
}

// --- Undo send ----------------------------------------------------------------------

export const delayedSends = new DelayedSends();

/**
 * Keeps a channel message in the outbox for seconds with an Undo countdown,
 * then flushes the outbox. Returns its client message ID.
 */
export async function holdChannelMessage(session, scope, text, { seconds, replyTo = 0 }) {
    const id = crypto.randomUUID();
    const sentAt = Date.now() / 1000;
    const holdUntil = Date.now() + seconds * 1000;
    await enqueue({ kind: "channel", scope }, text, { id, sentAt, holdUntil, replyTo });
    session.showQueued(scope, { clientMsgID: id, text, sentAt, holdUntil, replyTo });
    delayedSends.hold(id, seconds, async () => {
        // The outbox skips entries until holdUntil has strictly passed.
        await new Promise(resolve => setTimeout(resolve, Math.max(0, holdUntil - Date.now() + 10)));
        if (app.session?.conn.open) await flushNow().catch(() => {});
        const still = (session.messages.get(scope) || []).find(m => m.clientMsgID === id && !m.id);
        // Offline (or a flush already under way): show it as waiting in the outbox.
        if (still?.holdUntil) session.showQueued(scope, { clientMsgID: id, text, sentAt, replyTo });
        if (still && app.session?.conn.open) setTimeout(() => flushNow().catch(() => {}), 500);
    });
    return id;
}

/** Takes a held message back. Resolves with whether it was still waiting. */
export async function undoHeld(session, scope, id) {
    if (!delayedSends.cancel(id)) {
        toast(t("act.alreadySent"));
        return false;
    }
    await removeQueued(id).catch(() => {});
    session.discardLocal(scope, id);
    toast(t("act.undone"));
    return true;
}

/** "Sending in N s · Undo" under a held message; counts itself down. */
export function undoCountdown(message, onUndo) {
    const label = h("span");
    const undo = h("button", { class: "link-btn", type: "button", onclick: onUndo }, t("act.undo"));
    const el = h("div", { class: "msg-state undo-state", role: "status" }, label, " · ", undo);
    let seen = false;
    const tick = () => {
        if (el.isConnected) seen = true;
        else if (seen) return clearInterval(timer);
        const left = Math.max(0, Math.ceil((message.holdUntil - Date.now()) / 1000));
        if (left > 0) {
            label.textContent = t("act.sendingIn", { seconds: left });
            return;
        }
        label.textContent = t("act.sending");
        el.replaceChildren(label);
        clearInterval(timer);
    };
    const timer = setInterval(tick, 250);
    tick();
    return el;
}
