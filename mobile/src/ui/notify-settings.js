// notify-settings.js — You → Notifications: the per-event matrix (sound,
// banner, push), chat level, do not disturb with snooze and quiet hours,
// sounds and spoken announcements, and automatic away. Also the channel
// notification sheet of the chat header and the Home header's DND toggle.
import { CHANNEL_LEVELS, CHAT_LEVELS, EVENTS, PUSH_EVENTS, channelLevel, matrixRow, snoozeEnd } from "../core/notify-policy.js";
import * as settings from "../core/settings.js";
import { setChannelWatch } from "../core/social.js";
import { t } from "../i18n.js";
import { notifier } from "../notifier.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { openPanel } from "./panel.js";
import "./social-strings.js";
import "./social.css";

function toggleRow(label, hint, checked, onchange) {
    return h("label", { class: "toggle" },
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, label), hint ? h("span", { class: "row-sub" }, hint) : null),
        h("input", { type: "checkbox", role: "switch", checked, onchange: event => onchange(event.target.checked) }));
}

function select(label, value, options, onchange) {
    return h("label", { class: "field notify-field" }, h("span", {}, label),
        h("select", { class: "input", onchange: event => onchange(event.target.value) },
            options.map(([key, text]) => h("option", { value: key, selected: key === value }, text))));
}

/** The event × output grid. */
function matrix() {
    const all = settings.all();
    const outputs = ["sound", "toast", "push"];
    const set = (event, output, on) => {
        const current = settings.get("matrix") || {};
        settings.set({ matrix: { ...current, [event]: { ...matrixRow(settings.all(), event), [output]: on } } });
    };
    return h("table", { class: "notify-matrix" },
        h("caption", { class: "visually-hidden" }, t("notify.matrix")),
        h("thead", {}, h("tr", {}, h("th", { scope: "col" }, t("notify.event")), outputs.map(output => h("th", { scope: "col" }, t(`notify.output.${output}`))))),
        h("tbody", {}, EVENTS.map(event => {
            const row = matrixRow(all, event);
            return h("tr", {}, h("th", { scope: "row" }, t(`notify.event.${event}`)), outputs.map(output => {
                const pushOnly = output === "push" && !PUSH_EVENTS[event];
                return h("td", {}, pushOnly ? h("span", { class: "row-meta", "aria-label": t("notify.noPush") }, "–")
                    : h("input", { type: "checkbox", checked: !!row[output], "aria-label": t("notify.cell", { output: t(`notify.output.${output}`), event: t(`notify.event.${event}`) }), onchange: e => set(event, output, e.target.checked) }));
            }));
        })));
}

function snoozeControls(rerender) {
    const until = settings.get("snoozeUntil");
    const active = until > Date.now();
    const status = active ? t("notify.snoozedUntil", { time: new Date(until).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" }) }) : t("notify.snoozeOff");
    const choose = choice => { settings.set({ snoozeUntil: choice ? snoozeEnd(choice) : 0 }); rerender(); };
    return h("div", { class: "snooze" },
        h("p", { class: "row-sub", role: "status" }, status),
        h("div", { class: "snooze-buttons" },
            h("button", { class: "btn", type: "button", onclick: () => choose(30) }, t("notify.snooze30")),
            h("button", { class: "btn", type: "button", onclick: () => choose(60) }, t("notify.snooze60")),
            h("button", { class: "btn", type: "button", onclick: () => choose("tomorrow") }, t("notify.snoozeTomorrow")),
            active ? h("button", { class: "btn btn-primary", type: "button", onclick: () => choose(0) }, t("notify.snoozeCancel")) : null));
}

/** The You screen's notification behaviour section (beside the push card). */
export function notificationBehaviour() {
    const box = h("div", { class: "notify-settings" });
    const render = () => {
        const all = settings.all();
        box.replaceChildren(
            h("div", { class: "card" },
                toggleRow(t("notify.dnd"), t("notify.dndHint"), all.dnd, on => { settings.set({ dnd: on }); render(); }),
                snoozeControls(render),
                h("div", { class: "quiet-hours", role: "group", "aria-label": t("notify.quiet") },
                    h("span", { class: "row-title" }, t("notify.quiet")),
                    h("label", { class: "field" }, h("span", {}, t("notify.quietFrom")), h("input", { class: "input", type: "time", value: all.quietFrom, onchange: e => settings.set({ quietFrom: e.target.value }) })),
                    h("label", { class: "field" }, h("span", {}, t("notify.quietTo")), h("input", { class: "input", type: "time", value: all.quietTo, onchange: e => settings.set({ quietTo: e.target.value }) })))),
            h("div", { class: "card" },
                select(t("notify.chatLevel"), all.chatLevel, CHAT_LEVELS.map(level => [level, t(`notify.chat.${level}`)]), value => settings.set({ chatLevel: value })),
                matrix()),
            h("div", { class: "card" },
                toggleRow(t("notify.sounds"), "", all.sounds, on => settings.set({ sounds: on })),
                h("label", { class: "field notify-field" }, h("span", {}, t("notify.volume")),
                    h("input", { type: "range", min: 0, max: 1, step: 0.05, value: all.soundVolume, "aria-label": t("notify.volume"), onchange: e => settings.set({ soundVolume: Number(e.target.value) }) })),
                h("button", { class: "btn btn-block notify-test", type: "button", onclick: () => { notifier.sounds.unlock(); notifier.sounds.play("mention", { force: true }); } }, t("notify.test")),
                toggleRow(t("notify.speech"), t("notify.speechHint"), all.speech, on => {
                    settings.set({ speech: on });
                    if (on) notifier.sounds.speak("channel_join", { force: true });
                })),
            h("div", { class: "card" },
                select(t("notify.autoAway"), String(all.autoAwayMinutes), [0, 5, 10, 15, 30, 60].map(n => [String(n), n ? t("notify.minutes", { n }) : t("notify.off")]), value => settings.set({ autoAwayMinutes: Number(value) })),
                h("label", { class: "field notify-field" }, h("span", {}, t("notify.awayMessage")),
                    h("input", { class: "input", type: "text", maxlength: 200, value: all.autoAwayMessage, placeholder: t("status.messagePlaceholder"), onchange: e => settings.set({ autoAwayMessage: e.target.value.trim() }) }))));
    };
    render();
    return box;
}

/** Notification level (and member watch for voice channels) of one channel. */
export function channelNotifySheet(channel) {
    openPanel({ title: t("notify.channelTitle", { channel: channel.name }) }, close => {
        const level = channelLevel(settings.all(), channel.id);
        const options = CHANNEL_LEVELS.map(value => h("button", {
            class: "row status-option", type: "button", "aria-pressed": String(value === level),
            onclick: () => {
                const map = { ...(settings.get("channelNotify") || {}) };
                if (value === "all") delete map[String(channel.id)];
                else map[String(channel.id)] = value;
                settings.set({ channelNotify: map });
                close(value);
            },
        }, h("span", { class: "row-main" }, h("span", { class: "row-title" }, t(`notify.level.${value}`)), h("span", { class: "row-sub" }, t(`notify.level.${value}Hint`)))));
        return [
            h("h2", { class: "sheet-title" }, t("notify.channelTitle", { channel: channel.name })),
            h("div", { role: "group", "aria-label": t("notify.channelTitle", { channel: channel.name }) }, options),
            h("button", { class: "btn btn-block", type: "button", onclick: () => close(null) }, t("common.close")),
        ];
    });
}

/** "Notify me when N people are in channel" for a voice channel row. */
export function channelWatchSheet(channel) {
    const current = Number(settings.get("channelWatch")?.[String(channel.id)]) || 0;
    openPanel({ title: t("watch.title", { channel: channel.name }) }, close => {
        const count = h("input", { class: "input", type: "number", min: 1, max: 99, value: current || 2, "aria-label": t("watch.count"), autofocus: true });
        return h("form", { class: "panel-form", onsubmit: event => { event.preventDefault(); setChannelWatch(channel.id, Number(count.value) || 0); close(true); } },
            h("h2", { class: "sheet-title" }, t("watch.title", { channel: channel.name })),
            h("p", { class: "hint" }, t("watch.hint")),
            h("label", { class: "field" }, h("span", {}, t("watch.count")), count),
            h("div", { class: "panel-buttons" },
                current ? h("button", { class: "btn btn-danger", type: "button", onclick: () => { setChannelWatch(channel.id, 0); close(false); } }, t("watch.remove")) : h("button", { class: "btn", type: "button", onclick: () => close(null) }, t("home.cancel")),
                h("button", { class: "btn btn-primary", type: "submit" }, t("watch.save"))));
    });
}

/** The quick do-not-disturb toggle of the Home header. */
export function dndButton() {
    const button = h("button", { class: "icon-btn dnd-toggle", type: "button" });
    const render = () => {
        const on = settings.silenced();
        button.setAttribute("aria-pressed", String(!!settings.get("dnd")));
        button.setAttribute("aria-label", t("notify.dnd"));
        button.title = on ? t("notify.silenced") : t("notify.dnd");
        button.classList.toggle("on", on);
        button.replaceChildren(icon(on ? "moon" : "sun"));
    };
    button.onclick = () => { settings.set({ dnd: !settings.get("dnd") }); render(); };
    render();
    return button;
}
