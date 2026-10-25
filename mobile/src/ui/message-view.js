// message-view.js — pieces of a message log shared by channel chat, direct
// messages and groups: a per-message element cache, timestamps per the
// timestamps setting, day separators, encryption badges, collapsed blocked
// messages, the header menu and the "About encryption" sheet.
import { dayKey, daysBetween, relativeTime } from "../core/message-format.js";
import * as settings from "../core/settings.js";
import { lang, t } from "../i18n.js";
import { clock, h, sheet } from "./dom.js";
import { icon } from "./icons.js";
import "./rich-strings.js";
import "./rich-text.css";

/**
 * Wraps line(message) so an unchanged message keeps its element across
 * re-renders: loaded images, previews and a playing video survive new
 * messages. A message counts as changed when its JSON changes (some state is
 * patched in place, e.g. a body opened after its key arrived).
 */
export function lineCache(build) {
    let cache = new WeakMap();
    // extra: state shown with the message that lives elsewhere (e.g. "Seen by").
    const line = (message, extra) => {
        const sig = JSON.stringify(extra === undefined ? message : [message, extra]);
        const hit = cache.get(message);
        if (hit && hit.sig === sig) return hit.el;
        const el = build(message, extra);
        cache.set(message, { sig, el });
        return el;
    };
    line.clear = () => { cache = new WeakMap(); };
    line.forget = message => cache.delete(message);
    return line;
}

function timeText(seconds, mode) {
    if (mode === "off" || !seconds) return "";
    if (mode === "relative") return relativeTime(seconds, Date.now() / 1000, lang());
    return clock(seconds);
}

/** The time cell of a message line, following the timestamps setting. */
export function timeNode(seconds) {
    const mode = settings.get("timestamps");
    const node = h("time", { class: `msg-time${mode === "off" ? " off" : ""}`, dataset: { at: String(seconds || 0) } }, timeText(seconds, mode));
    if (seconds) {
        node.setAttribute("datetime", new Date(seconds * 1000).toISOString());
        node.title = new Date(seconds * 1000).toLocaleString();
    }
    return node;
}

/** Refreshes relative times in place once a minute; returns stop(). */
export function relativeTicker(root) {
    const timer = setInterval(() => {
        if (settings.get("timestamps") !== "relative") return;
        for (const node of root.querySelectorAll("time.msg-time[data-at]")) node.textContent = timeText(Number(node.dataset.at), "relative");
    }, 60000);
    return () => clearInterval(timer);
}

function dayLabel(seconds) {
    const diff = daysBetween(dayKey(seconds), dayKey(Date.now() / 1000));
    if (diff === 0) return t("chat.today");
    if (diff === 1) return t("rich.yesterday");
    return new Date(seconds * 1000).toLocaleDateString(lang(), { weekday: "short", year: "numeric", month: "short", day: "numeric" });
}

/** Message elements with a separator before each new day. */
export function withDays(messages, timeOf, line) {
    const out = [];
    let last = "";
    for (const message of messages) {
        const at = timeOf(message);
        const key = at ? dayKey(at) : last;
        if (key && key !== last) out.push(h("div", { class: "day-sep", role: "separator", "aria-label": dayLabel(at) }, h("span", {}, dayLabel(at))));
        last = key;
        out.push(line(message));
    }
    return out;
}

/** A small lock (end-to-end) or shield (channel key) after the sender. */
export function encryptionBadge(kind) {
    const label = kind === "e2e" ? t("dm.encrypted") : t("rich.channelEncrypted");
    return h("span", { class: `enc-badge enc-${kind}`, role: "img", title: label, "aria-label": label }, icon(kind === "e2e" ? "lock" : "shield"));
}

/** A blocked sender's message, folded; Show calls onShow. */
export function blockedLine(message, timeAt, onShow) {
    return h("div", { class: "msg msg-blocked", dataset: { id: String(message.id ?? message.clientMsgID ?? "") } },
        timeNode(timeAt), h("span"),
        h("div", { class: "msg-body" }, h("span", { class: "msg-locked" }, t("rich.blocked")), " · ",
            h("button", { class: "link-btn", type: "button", onclick: onShow }, t("rich.show"))));
}

export function isBlockedUser(uid) {
    return !!uid && (settings.get("blockedUsers") || []).includes(uid);
}

/** Re-renders on display setting changes; returns stop(). */
export function watchDisplaySettings(onChange) {
    const keys = ["timestamps", "linkPreviews", "inlineImages", "blockedUsers"];
    return settings.onChange(({ changed }) => { if (changed.some(name => keys.includes(name))) onChange(); });
}

/** The "About encryption" sheet, as the desktop's explanation. */
export function aboutEncryption() {
    return sheet(t("rich.encryptionTitle"), t("rich.encryptionHelp"), [{ label: t("common.close"), value: null, primary: true }]);
}

/** Several header buttons in the header's action slot. */
export function headerActions(...buttons) {
    return h("span", { class: "header-actions" }, buttons.filter(Boolean));
}

/** The header's ⋮ menu: items are {label, run, danger}. */
export function menuButton(items) {
    return h("button", { class: "icon-btn", type: "button", "aria-label": t("rich.menu"), "aria-haspopup": "dialog", onclick: async () => {
        const list = typeof items === "function" ? items() : items;
        const choice = await sheet(t("rich.menu"), null, [...list.map((item, index) => ({ label: item.label, value: index, danger: item.danger })), { label: t("link.cancel"), value: null }]);
        if (choice !== null && choice !== undefined) list[choice].run();
    } }, icon("more"));
}
