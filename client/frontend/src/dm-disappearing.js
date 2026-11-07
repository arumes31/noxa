// dm-disappearing.js — disappearing direct messages (plan item 28).
//
// Expiry is enforced by the Go layer on this device (client/dm_disappearing.go):
// it deletes expired text from the sealed log and redacts the protected
// archive, then emits "dm_expired". This module only shows the feature: the
// timer options and labels, the countdown on a message, the system line of a
// setting change, and the dialog that changes the setting.
import { t } from "./i18n.js";
import { closeDialog, mountServerDialog } from "./modal.js";

/** Timers offered (seconds); 0 = off. Mirrors dmDisappearOptions in Go. */
export const DISAPPEAR_OPTIONS = Object.freeze([0, 30, 300, 3600, 86400, 604800]);
/** Largest TTL accepted from a sender (four weeks), as in Go. */
export const DISAPPEAR_MAX_TTL = 28 * 24 * 3600;

const OPTION_KEYS = { 0: "disappear.off", 30: "disappear.30s", 300: "disappear.5m", 3600: "disappear.1h", 86400: "disappear.1d", 604800: "disappear.1w" };

export { disappearingFromBody } from "./ratchet-projection.js";

/** Readable name of a timer ("1 hour"); unusual values fall back to a duration. */
export function timerLabel(seconds) {
    const key = OPTION_KEYS[seconds];
    return key ? t(key) : durationLabel(seconds);
}

/** Compact duration ("3d", "4h", "12m", "45s") for a countdown. */
export function durationLabel(seconds) {
    const s = Math.max(0, Math.ceil(Number(seconds) || 0));
    if (s >= 86400) return Math.floor(s / 86400) + "d";
    if (s >= 3600) return Math.floor(s / 3600) + "h";
    if (s >= 60) return Math.floor(s / 60) + "m";
    return s + "s";
}

/** Seconds left before expiresAt (unix seconds), never negative. */
export function remainingSeconds(expiresAt, nowMS = Date.now()) {
    return Math.max(0, Number(expiresAt || 0) - nowMS / 1000);
}

/**
 * Text of a setting change: the system line for this app, and the readable
 * fallback older apps show as an ordinary message.
 */
export function timerNotice(seconds, name) {
    return seconds > 0 ? t("disappear.noticeOn", { name, timer: timerLabel(seconds) }) : t("disappear.noticeOff", { name });
}
export function timerFallbackText(seconds) {
    return seconds > 0 ? t("disappear.fallbackOn", { timer: timerLabel(seconds) }) : t("disappear.fallbackOff");
}

const key = (from, id) => JSON.stringify([from || "", id || ""]);

/**
 * Marks the listed messages ({from_unique_id, client_msg_id}) as expired in
 * msgs: text gone, shown as a tombstone. Returns the messages changed.
 */
export function applyExpired(msgs, list = []) {
    const wanted = new Set(list.map((m) => key(m.from_unique_id, m.client_msg_id)));
    const changed = [];
    for (const m of msgs || []) {
        if (m.expired || !wanted.has(key(m.fromUID, m.clientMsgID))) continue;
        Object.assign(m, { text: "", deleted: true, expired: true, edited: false, expiresAt: 0 });
        changed.push(m);
    }
    return changed;
}

/** Records the expiry times Go started ({from_unique_id, client_msg_id, expires_at}). */
export function applyExpiries(msgs, list = []) {
    const times = new Map(list.map((m) => [key(m.from_unique_id, m.client_msg_id), Number(m.expires_at) || 0]));
    const changed = [];
    for (const m of msgs || []) {
        const at = times.get(key(m.fromUID, m.clientMsgID));
        if (at && m.expiresAt !== at) { m.expiresAt = at; changed.push(m); }
    }
    return changed;
}

/** Whether any message in msgs is a received disappearing one not started yet. */
export function needsStart(msgs) {
    return (msgs || []).some((m) => !m.self && m.ttl > 0 && !m.expiresAt && !m.deleted);
}

/** Countdown element for a message with a running timer, or null. */
export function countdownElement(m, nowMS = Date.now()) {
    if (!(m?.ttl > 0) || m.deleted) return null;
    const el = document.createElement("span");
    el.className = "msg-disappear";
    el.dataset.expiresAt = String(m.expiresAt || 0);
    updateCountdown(el, m.ttl, nowMS);
    el.dataset.ttl = String(m.ttl);
    return el;
}

/** Refreshes one countdown element's text and tooltip. */
export function updateCountdown(el, ttl = Number(el.dataset.ttl) || 0, nowMS = Date.now()) {
    const at = Number(el.dataset.expiresAt) || 0;
    const text = at ? durationLabel(remainingSeconds(at, nowMS)) : durationLabel(ttl);
    el.textContent = "⏱ " + text;
    const title = at ? t("disappear.remaining", { time: text }) : t("disappear.afterRead", { timer: timerLabel(ttl) });
    el.title = title;
    el.setAttribute("aria-label", title);
}

/**
 * Dialog to change the conversation timer. onSave(seconds) returns "" or an
 * error text; the dialog stays open on error.
 */
export function openDisappearingDialog({ current = 0, launcher = null, onSave }) {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const dialog = document.createElement("div");
    dialog.className = "dlg disappear-dialog";
    // The modal system makes the overlay the labelled dialog (its heading).
    const title = document.createElement("h3");
    title.id = "disappear-title";
    title.textContent = t("disappear.title");
    const help = document.createElement("p");
    help.className = "dlg-text";
    help.textContent = t("disappear.help");
    const group = document.createElement("div");
    group.className = "disappear-options";
    group.setAttribute("role", "radiogroup");
    group.setAttribute("aria-labelledby", title.id);
    let first = null;
    for (const seconds of DISAPPEAR_OPTIONS) {
        const label = document.createElement("label");
        label.className = "dlg-option";
        const input = document.createElement("input");
        input.type = "radio";
        input.name = "disappear-timer";
        input.value = String(seconds);
        input.checked = seconds === current;
        if (input.checked || !first) first = input.checked ? input : first || input;
        const text = document.createElement("span");
        text.textContent = timerLabel(seconds);
        label.append(input, text);
        group.appendChild(label);
    }
    const error = document.createElement("p");
    error.className = "set-hint warn";
    error.hidden = true;
    error.setAttribute("role", "alert");
    const buttons = document.createElement("div");
    buttons.className = "dlg-buttons";
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "dlg-cancel";
    cancel.textContent = t("desktop.cancel");
    const save = document.createElement("button");
    save.type = "button";
    save.className = "dlg-ok";
    save.textContent = t("disappear.save");
    cancel.onclick = () => closeDialog(overlay, "cancel");
    save.onclick = async () => {
        const chosen = Number(group.querySelector("input:checked")?.value ?? current);
        if (chosen === current) { closeDialog(overlay, "close"); return; }
        save.disabled = true;
        const problem = await onSave(chosen);
        save.disabled = false;
        if (!overlay.isConnected) return;
        if (problem) {
            error.textContent = t("disappear.failed", { error: problem });
            error.hidden = false;
            return;
        }
        closeDialog(overlay, "close");
    };
    buttons.append(cancel, save);
    dialog.append(title, help, group, error, buttons);
    overlay.appendChild(dialog);
    overlay.onclick = (event) => { if (event.target === overlay) closeDialog(overlay, "cancel"); };
    mountServerDialog(overlay, { launcher, initialFocus: group.querySelector("input:checked") || first });
    return overlay;
}
