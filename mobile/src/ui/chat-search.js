// chat-search.js — search inside one conversation (channel, group or
// direct). Typing filters the loaded log in place; "Search all history"
// loads older pages one at a time, reporting progress, until the start of
// the conversation or Cancel, and lists the matches. Tapping a match closes
// the search and jumps to the message (loading history until it is there).
// Other screens can ask for a jump on open with jumpAfterOpen(tag, id).
import { excerpt, highlight, searchEntries } from "../core/search.js";
import { t } from "../i18n.js";
import { clock, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

const MAX_PAGES = 200;
let pendingJump = null;

/** Remembers a message to jump to when the conversation tag opens next. */
export function jumpAfterOpen(tag, id) {
    pendingJump = { tag, id };
}

/** Query runs as <mark>ed text. */
export function highlighted(text, query) {
    return highlight(text, query).map(run => run.hit ? h("mark", {}, run.text) : run.text);
}

/**
 * tag: the conversation tag; log: the chat log element; entries(): the
 * loaded messages as core/search.js entries; loadOlder(): loads one older
 * page and resolves whether more exist; ensure(id): loads until the message
 * is present (resolves whether found). Returns {button, bar, jump(id), stop()}.
 */
export function chatSearch({ tag, log, entries, loadOlder, ensure = async () => true }) {
    const input = h("input", { class: "input chat-search-input", type: "search", enterkeyhint: "search", "aria-label": t("search.inChat"), placeholder: t("search.inChat"), autocomplete: "off" });
    const status = h("p", { class: "chat-search-status", role: "status", "aria-live": "polite" });
    const deep = h("button", { class: "btn chat-search-deep", type: "button" }, t("search.allHistory"));
    const cancel = h("button", { class: "btn", type: "button", hidden: true }, t("link.cancel"));
    const results = h("ul", { class: "list chat-search-results", "aria-label": t("search.results") });
    const closeButton = h("button", { class: "icon-btn", type: "button", "aria-label": t("search.close") }, "×");
    const bar = h("div", { class: "chat-search", role: "search", hidden: true },
        h("div", { class: "chat-search-row" }, input, closeButton),
        h("div", { class: "chat-search-row" }, status, deep, cancel),
        results);
    const button = h("button", { class: "icon-btn", type: "button", "aria-label": t("search.inChat"), "aria-expanded": "false", onclick: () => bar.hidden ? open() : close() }, icon("search"));

    let query = "";
    let searching = null; // {cancelled}
    let listed = false;

    const found = () => searchEntries(entries(), { query }, "newest");

    function applyFilter() {
        const active = !bar.hidden && query.trim() !== "";
        log.classList.toggle("searching", active);
        if (!active) {
            for (const el of log.querySelectorAll(".search-miss")) el.classList.remove("search-miss");
            return;
        }
        const ids = new Set(found().map(entry => String(entry.id)));
        for (const el of log.querySelectorAll(".msg")) el.classList.toggle("search-miss", !ids.has(el.dataset.id));
    }

    function update() {
        query = input.value;
        applyFilter();
        const count = query.trim() ? found().length : 0;
        if (!searching) status.textContent = query.trim() ? t("search.loadedMatches", { count }) : "";
        if (listed) list();
    }

    function list() {
        listed = true;
        const matches = found();
        results.replaceChildren(...matches.slice(0, 200).map(entry => h("li", {}, h("button", { class: "row search-result", type: "button", onclick: () => { close(); jump(entry.id); } },
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, entry.from || t("dm.someone"), h("span", { class: "row-meta" }, ` · ${new Date(entry.at * 1000).toLocaleDateString()} ${clock(entry.at)}`)),
                h("span", { class: "row-sub search-excerpt" }, highlighted(excerpt(entry.text, query), query)))))));
        if (!matches.length) results.replaceChildren(h("li", { class: "hint" }, t("search.none")));
    }

    async function searchAll() {
        if (searching || !query.trim()) return;
        const run = { cancelled: false };
        searching = run;
        deep.hidden = true;
        cancel.hidden = false;
        let more = true;
        try {
            for (let page = 0; more && !run.cancelled && page < MAX_PAGES; page++) {
                status.textContent = t("search.progress", { count: entries().length });
                more = await loadOlder();
            }
        } catch (error) {
            status.textContent = t("common.error", { error: error.message });
        }
        searching = null;
        deep.hidden = false;
        cancel.hidden = true;
        if (run.cancelled) return;
        applyFilter();
        status.textContent = t(more ? "search.searchedSome" : "search.searchedAll", { count: entries().length, matches: found().length });
        list();
    }

    function open() {
        bar.hidden = false;
        button.setAttribute("aria-expanded", "true");
        input.focus();
        update();
    }

    function close() {
        if (searching) searching.cancelled = true;
        bar.hidden = true;
        button.setAttribute("aria-expanded", "false");
        input.value = "";
        listed = false;
        results.replaceChildren();
        update();
        button.focus({ preventScroll: true });
    }

    /** Scrolls to a message and flashes it, loading history first if needed. */
    async function jump(id) {
        const selector = `.msg[data-id="${CSS.escape(String(id))}"]`;
        let el = log.querySelector(selector);
        if (!el) {
            status.textContent = t("search.jumping");
            const ok = await ensure(id).catch(() => false);
            el = log.querySelector(selector);
            if (!ok || !el) {
                toast(t("search.notFound"), "warn");
                return false;
            }
        }
        el.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
        el.classList.remove("flash");
        void el.offsetWidth;
        el.classList.add("flash");
        if (!el.hasAttribute("tabindex")) el.setAttribute("tabindex", "-1");
        el.focus({ preventScroll: true });
        setTimeout(() => el.classList.remove("flash"), 2200);
        return true;
    }

    input.addEventListener("input", update);
    input.addEventListener("keydown", event => {
        if (event.key === "Enter") { event.preventDefault(); searchAll(); }
        if (event.key === "Escape") close();
    });
    deep.onclick = searchAll;
    cancel.onclick = () => {
        if (searching) searching.cancelled = true;
        status.textContent = t("search.cancelled");
    };
    closeButton.onclick = close;
    // The log re-renders on every message; keep the filter applied.
    const observer = new MutationObserver(() => { if (!bar.hidden && query.trim()) applyFilter(); });
    observer.observe(log, { childList: true });
    const onKey = event => {
        if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "f" && log.isConnected) { event.preventDefault(); open(); }
    };
    document.addEventListener("keydown", onKey);

    if (pendingJump?.tag === tag) {
        const { id } = pendingJump;
        pendingJump = null;
        setTimeout(() => jump(id), 0);
    }
    return {
        button, bar, jump,
        stop() {
            if (searching) searching.cancelled = true;
            observer.disconnect();
            document.removeEventListener("keydown", onKey);
        },
    };
}
