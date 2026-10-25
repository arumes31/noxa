// activity.js — the activity center (#/activity): mentions, direct
// messages, calls, invitations and pokes, newest first, in tabs.
import { excerpt } from "../core/search.js";
import { notifier } from "../notifier.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav } from "./common.js";
import "./social-strings.js";
import "./social.css";

const TABS = ["all", "mention", "dm", "call", "invite", "poke"];
const GLYPH = { mention: "@", dm: "✉", call: "☎", invite: "◇", poke: "!" };
let tab = "all";

function when(at) {
    const date = new Date(at);
    const today = new Date().toDateString() === date.toDateString();
    return today ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : date.toLocaleDateString([], { day: "numeric", month: "short" });
}

export function renderActivity(root) {
    const log = notifier.activity;
    const list = h("ul", { class: "list activity-list", "aria-live": "polite" });
    const tabs = h("div", { class: "activity-tabs", role: "tablist", "aria-label": t("activity.title") });
    const render = () => {
        tabs.replaceChildren(...TABS.map(name => {
            const unread = log.list(name).filter(e => !e.read).length;
            return h("button", { class: "activity-tab", type: "button", role: "tab", id: `activity-tab-${name}`, "aria-selected": String(tab === name), "aria-controls": "activity-panel", onclick: () => { tab = name; render(); } },
                t(`activity.tab.${name}`), unread ? h("span", { class: "badge", "aria-label": t("home.unread", { count: unread }) }, String(unread)) : null);
        }));
        const entries = log.list(tab);
        list.setAttribute("aria-labelledby", `activity-tab-${tab}`);
        list.replaceChildren(...(entries.length ? entries.map(entry => h("li", {},
            h("a", { class: `row activity-entry${entry.read ? "" : " unread"}`, href: entry.route || "#/activity", onclick: () => log.markRead(entry.id) },
                h("span", { class: "hash", "aria-hidden": "true" }, GLYPH[entry.kind] || "•"),
                h("span", { class: "row-main" },
                    h("span", { class: "row-title" }, entry.title),
                    h("span", { class: "row-sub" }, `${t(`activity.kind.${entry.kind}`)}${entry.body ? ` · ${excerpt(entry.body, "", 70)}` : ""}`)),
                h("span", { class: "row-meta" }, when(entry.at)),
                entry.read ? null : h("span", { class: "unread-dot", "aria-label": t("activity.unread") }))))
            : [h("li", { class: "hint activity-empty" }, t("activity.empty"))]));
    };
    root.replaceChildren(h("section", { class: "screen" },
        header(t("activity.title"), { back: "#/home", action: h("button", { class: "icon-btn activity-read-all", type: "button", "aria-label": t("activity.markAll"), title: t("activity.markAll"), onclick: () => log.markAllRead() }, icon("check")) }),
        h("div", { class: "screen-body" }, tabs, h("div", { id: "activity-panel", role: "tabpanel" }, list)),
        nav("home")));
    render();
    log.ready.then(render);
    log.addEventListener("change", render);
    return () => log.removeEventListener("change", render);
}
