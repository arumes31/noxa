// common.js — pieces shared by the signed-in screens: navigation, headers,
// scope names and unread badges.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import "./voice-alerts.js";

/** During a private call, a bar above the navigation returns to it. */
function privateCallBar() {
    const calls = app.calls;
    if (!calls?.active || calls.incoming) return null;
    const others = calls.call.participants.filter(p => p.unique_id !== calls.uid).map(p => app.session?.clients().find(c => c.unique_id === p.unique_id)?.nickname || t("dm.someone"));
    return h("div", { class: "call-bar", role: "region", "aria-label": t("call.title") },
        h("a", { class: "call-bar-main", href: `#/call/${calls.call.id}` },
            h("span", { class: "speaking-dot on", "aria-hidden": "true" }),
            h("span", { class: "row-main" }, h("span", { class: "row-title" }, others.join(", ")), h("span", { class: "row-sub" }, t("call.active")))),
        h("button", { class: "icon-btn danger", type: "button", "aria-label": t("call.hangUp"), onclick: () => calls.hangUp() }, icon("hangup")));
}

/** While in voice, a bar above the navigation returns to the call. */
function callBar(active) {
    const voice = app.voice;
    if (!voice?.active || active === "voice") return privateCallBar();
    const channel = app.session?.channels().find(c => c.id === voice.channelID);
    const bar = h("div", { class: "call-bar", role: "region", "aria-label": t("voice.callBar") },
        h("a", { class: "call-bar-main", href: "#/voice" },
            h("span", { class: "speaking-dot", "aria-hidden": "true" }),
            h("span", { class: "row-main" }, h("span", { class: "row-title" }, channel?.name || t("voice.title")), h("span", { class: "row-sub" }, t("voice.connected")))),
        h("button", { class: "icon-btn call-mute", type: "button", onclick: () => voice.setMuted(!voice.muted) }),
        h("button", { class: "icon-btn danger", type: "button", "aria-label": t("voice.leave"), onclick: () => voice.leave() }, icon("hangup")));
    updateCallBar(bar);
    return bar;
}

/** Refreshes the call bar's speaking dot and mute button. */
export function updateCallBar(bar) {
    const voice = app.voice;
    if (!voice) return;
    bar.querySelector(".speaking-dot").classList.toggle("on", voice.speaking.size > 0);
    const mute = bar.querySelector(".call-mute");
    mute.setAttribute("aria-pressed", String(voice.muted));
    mute.setAttribute("aria-label", voice.muted ? t("voice.unmute") : t("voice.mute"));
    mute.replaceChildren(icon(voice.muted ? "micOff" : "mic"));
}

export function nav(active) {
    const item = (route, name, label) => h("a", { href: `#/${route}`, "aria-current": active === route ? "page" : null, "aria-label": label }, icon(name), h("span", {}, label));
    const bar = callBar(active);
    const navigation = h("nav", { class: "nav", "aria-label": "Main" },
        item("home", "home", t("nav.home")),
        item("servers", "servers", t("nav.servers")),
        item("voice", "voice", t("nav.voice")),
        item("you", "you", t("nav.you")));
    return bar ? h("div", { class: "bottom" }, bar, navigation) : navigation;
}

export function header(title, { back, sub, action } = {}) {
    return h("header", { class: "screen-header" },
        back ? h("a", { class: "icon-btn", href: back, "aria-label": t("common.back") }, icon("back")) : h("span"),
        h("h1", {}, title, sub ? h("span", { class: "sub" }, sub) : null),
        action || h("span"));
}

export function scopeName(session, scope) {
    if (scope === 0) return t("channels.global");
    return `# ${session.channels().find(c => c.id === scope)?.name || scope}`;
}

/** Unread count badge of a conversation tag; mentions are highlighted. */
export function unreadBadge(tag) {
    const entry = app.unread.get(tag);
    if (!entry?.count) return null;
    return h("span", { class: entry.mentions ? "badge badge-mention" : "badge", "aria-label": t("home.unread", { count: entry.count }) },
        entry.mentions ? `@${entry.count}` : String(entry.count));
}

/** The route of a direct conversation. */
export function dmHref(uid) {
    return `#/dm/${encodeURIComponent(uid)}`;
}

export function lastMessage(session, scope) {
    const list = session.messages.get(scope) || [];
    return list[list.length - 1];
}
