// global-search.js — Search (#/search): every conversation on the phone at
// once, with people, conversation, date and attachment filters and newest
// or oldest first. Results are grouped by conversation; "Search older
// history" loads more channel and group pages (direct messages are all on
// the phone already). A result opens its conversation at the message.
import { app } from "../app.js";
import { dayBounds, excerpt, groupByConversation, searchEntries } from "../core/search.js";
import { t } from "../i18n.js";
import { clock, h } from "./dom.js";
import { header } from "./common.js";
import { highlighted, jumpAfterOpen } from "./chat-search.js";
import { conversations, people } from "./tool-sources.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

const PAGES_PER_STEP = 2;

export function renderSearch(root) {
    const session = app.session;
    const query = h("input", { class: "input", type: "search", id: "search-query", enterkeyhint: "search", autocomplete: "off", placeholder: t("search.placeholder"), "aria-label": t("search.placeholder") });
    const person = h("select", { class: "input", id: "search-person" });
    const conversation = h("select", { class: "input", id: "search-conversation" });
    const after = h("input", { class: "input", type: "date", id: "search-after" });
    const before = h("input", { class: "input", type: "date", id: "search-before" });
    const sort = h("select", { class: "input", id: "search-sort" },
        h("option", { value: "newest" }, t("search.newest")), h("option", { value: "oldest" }, t("search.oldest")));
    const attachments = h("input", { type: "checkbox", id: "search-attachments" });
    const status = h("p", { class: "tool-progress", role: "status", "aria-live": "polite" });
    const results = h("div", { class: "search-groups", "aria-label": t("search.results"), role: "region" });
    const deep = h("button", { class: "btn", type: "button" }, t("search.older"));
    const cancel = h("button", { class: "btn", type: "button", hidden: true }, t("link.cancel"));
    let running = null;

    function fillSelects() {
        const list = conversations(session);
        const keepPerson = person.value;
        const keepConversation = conversation.value;
        person.replaceChildren(h("option", { value: "" }, t("search.anyone")), ...people(session, list).map(p => h("option", { value: p.uid }, p.name)));
        conversation.replaceChildren(h("option", { value: "" }, t("search.everywhere")), ...list.map(c => h("option", { value: c.tag }, c.title)));
        person.value = [...person.options].some(o => o.value === keepPerson) ? keepPerson : "";
        conversation.value = [...conversation.options].some(o => o.value === keepConversation) ? keepConversation : "";
    }

    function filter() {
        return {
            query: query.value,
            from: person.value ? [person.value] : [],
            conversations: conversation.value ? [conversation.value] : [],
            after: dayBounds(after.value),
            before: dayBounds(before.value, true),
            attachments: attachments.checked,
        };
    }

    function render() {
        const list = conversations(session);
        const titles = new Map(list.map(c => [c.tag, c]));
        const f = filter();
        const active = f.query.trim() || f.from.length || f.conversations.length || f.after || f.before || f.attachments;
        if (!active) {
            results.replaceChildren(h("p", { class: "hint" }, t("search.hint")));
            if (!running) status.textContent = "";
            return;
        }
        const found = searchEntries(list.flatMap(c => c.entries), f, sort.value);
        if (!running) status.textContent = t("search.count", { count: found.length, searched: list.reduce((n, c) => n + c.entries.length, 0) });
        const groups = groupByConversation(found);
        results.replaceChildren(...groups.map(group => {
            const info = titles.get(group.conversation);
            const open = id => { jumpAfterOpen(group.conversation, id); location.hash = info.href; };
            return h("section", { class: "card tool-group", "aria-label": info.title },
                h("div", { class: "tool-group-head" }, h("h2", {}, info.title),
                    h("a", { class: "tool-open", href: info.href }, t("search.openConversation"))),
                h("ul", { class: "list" }, group.results.slice(0, 50).map(entry => h("li", {}, h("button", { class: "row tool-row search-result", type: "button", onclick: () => open(entry.id),
                    "aria-label": t("search.resultLabel", { from: entry.from, conversation: info.title }) },
                    h("span", { class: "row-main" },
                        h("span", { class: "row-title" }, entry.from || t("dm.someone"), h("span", { class: "row-meta" }, ` · ${new Date(entry.at * 1000).toLocaleDateString()} ${clock(entry.at)}`)),
                        h("span", { class: "row-sub search-excerpt" }, highlighted(excerpt(entry.text, f.query), f.query))))))),
                group.results.length > 50 ? h("p", { class: "hint" }, t("search.moreInConversation", { count: group.results.length - 50 })) : null);
        }));
        if (!groups.length) results.replaceChildren(h("p", { class: "hint" }, t("search.none")));
    }

    /** Loads older pages of every channel and group in scope, a few at a time. */
    async function searchOlder() {
        if (running) return;
        const run = { cancelled: false };
        running = run;
        deep.hidden = true;
        cancel.hidden = false;
        const only = conversation.value;
        const targets = conversations(session).filter(c => c.kind !== "dm" && (!only || c.tag === only));
        let loaded = 0;
        for (const target of targets) {
            if (run.cancelled) break;
            try {
                if (target.kind === "channel") {
                    await session.search(target.id, "", { maxPages: PAGES_PER_STEP });
                } else {
                    for (let page = 0; page < PAGES_PER_STEP && !run.cancelled; page++) if (!await app.groups.loadOlder(target.id)) break;
                }
            } catch {
                // A conversation that cannot be read is skipped.
            }
            loaded = conversations(session).reduce((n, c) => n + c.entries.length, 0);
            status.textContent = t("search.progress", { count: loaded });
        }
        running = null;
        deep.hidden = false;
        cancel.hidden = true;
        fillSelects();
        render();
    }

    let timer = 0;
    const later = () => { clearTimeout(timer); timer = setTimeout(render, 150); };
    query.addEventListener("input", later);
    for (const control of [person, conversation, after, before, sort, attachments]) control.addEventListener("change", render);
    deep.onclick = searchOlder;
    cancel.onclick = () => { if (running) running.cancelled = true; };
    const field = (id, label, control, wide = false) => h("div", { class: `field${wide ? " wide" : ""}` }, h("label", { for: id }, label), control);
    root.replaceChildren(h("section", { class: "screen chat-tools" },
        header(t("search.title"), { back: "#/home" }),
        h("div", { class: "screen-body" },
            h("form", { role: "search", onsubmit: event => { event.preventDefault(); render(); } }, query),
            h("details", { class: "card" }, h("summary", {}, t("search.filters")),
                h("div", { class: "tool-filters", style: "margin-top:10px" },
                    field("search-person", t("search.from"), person),
                    field("search-conversation", t("search.in"), conversation),
                    field("search-after", t("search.after"), after),
                    field("search-before", t("search.before"), before),
                    field("search-sort", t("search.sort"), sort),
                    h("label", { class: "toggle" }, attachments, h("span", {}, t("search.attachments"))))),
            h("div", { class: "tool-actions" }, deep, cancel),
            status, results)));
    fillSelects();
    render();
    query.focus();
    return () => {
        clearTimeout(timer);
        if (running) running.cancelled = true;
    };
}
