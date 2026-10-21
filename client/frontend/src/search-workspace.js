import { t } from "./i18n.js";
import { parseFileRef } from "./chat-parsers.js";
import { closeDialog } from "./modal.js";

const element = (tag, className, text) => { const node = document.createElement(tag); node.className = className; if (text) node.textContent = text; return node; };
export function searchSnippet(message) {
    return String(message.body || "").replace(/\[file:([^\]]+)\]/g, (_token, capture) =>
        t("midnight.searchAttachment", { name: parseFileRef(capture).name }));
}

export function searchTimestamp(message) {
    const value = message.sent_at;
    return typeof value === "number" ? value * 1000 : Date.parse(value || "") || 0;
}
export function filterSearchResults(messages, { author = "", days = 0, attachments = false, sort = "newest", query = "", now = Date.now() } = {}) {
    const score = message => (String(message.body || "").toLowerCase().split(query.toLowerCase()).length - 1);
    return messages.filter(message => (!author || (message.from_unique_id || message.from_nickname || "?") === author) &&
        (!days || searchTimestamp(message) >= now - Number(days) * 86400000) &&
        (!attachments || /\[file:|\[noxa-voice:/i.test(message.body || ""))).sort((a, b) => {
        if (sort === "relevance" && query && score(a) !== score(b)) return score(b) - score(a);
        return (searchTimestamp(a) - searchTimestamp(b)) * (sort === "oldest" ? 1 : -1);
    });
}

export function createSearchWorkspace({ query, results, summary, channels, selectedChannel, current, onSearch, onAdvanced, onOpen, loadContext, renderBody, highlight, renderBadge }) {
    const overlay = element("div", "dlg-overlay search-workspace-overlay");
    const valid = () => overlay.isConnected && current();
    const dialog = element("section", "dlg search-results search-workspace");
    const header = element("header", "search-workspace-header");
    const heading = element("div", ""); heading.append(element("h2", "", t("midnight.searchTitle")), element("p", "set-hint", summary));
    const close = element("button", "ui-button", t("common.close")); close.type = "button"; close.onclick = () => closeDialog(overlay);
    header.append(heading, close);
    const tools = element("form", "search-workspace-tools");
    const input = element("input", "dlg-input search-workspace-query"); input.type = "search"; input.value = query; input.required = true; input.setAttribute("aria-label", t("midnight.searchQuery"));
    const submit = element("button", "ui-button", t("midnight.searchSubmit")); submit.type = "submit";
    const channelSelect = element("select", "dlg-input"); channelSelect.setAttribute("aria-label", t("midnight.searchChannels"));
    for (const channel of channels) { const option = element("option", "", channel.name); option.value = String(channel.id); channelSelect.append(option); }
    channelSelect.value = String(selectedChannel);
    tools.append(input, submit, channelSelect);
    if (onAdvanced) { const advanced = element("button", "ui-button", t("midnight.advancedSearch")); advanced.type = "button"; advanced.onclick = () => { if (valid()) { closeDialog(overlay); onAdvanced(); } }; tools.append(advanced); }
    tools.onsubmit = event => { event.preventDefault(); if (valid() && input.value.trim()) onSearch(input.value.trim(), channelSelect.value); };
    channelSelect.onchange = () => { if (valid() && input.value.trim()) onSearch(input.value.trim(), channelSelect.value); };
    const filters = element("div", "search-workspace-filters");
    const select = (label, options) => {
        const wrap = element("label", "search-filter", label), control = element("select", "dlg-input");
        control.setAttribute("aria-label", label);
        for (const [value, text] of options) { const option = element("option", "", text); option.value = value; control.append(option); }
        wrap.append(control); filters.append(wrap); return control;
    };
    const authors = new Map(results.map(message => [message.from_unique_id || message.from_nickname || "?", message.from_nickname || "?"]));
    const author = select(t("midnight.searchPeople"), [["", t("midnight.searchEveryone")], ...authors]);
    const date = select(t("midnight.searchDate"), [["0", t("midnight.searchAnyDate")], ["1", t("midnight.searchDay")], ["7", t("midnight.searchWeek")], ["30", t("midnight.searchMonth")]]);
    const sorting = select(t("midnight.searchSort"), [["newest", t("midnight.searchNewest")], ["oldest", t("midnight.searchOldest")], ["relevance", t("midnight.searchRelevance")]]);
    const attachmentLabel = element("label", "search-filter-attachment");
    const attachments = element("input", ""); attachments.type = "checkbox"; attachmentLabel.append(attachments, document.createTextNode(t("midnight.searchAttachments"))); filters.append(attachmentLabel);
    const count = element("p", "search-workspace-count"); count.setAttribute("role", "status");
    const body = element("div", "search-workspace-body"), list = element("div", "search-list"), preview = element("aside", "search-preview");
    list.setAttribute("role", "region"); list.setAttribute("aria-label", t("midnight.searchTitle")); preview.setAttribute("aria-label", t("midnight.searchPreview"));
    body.append(list, preview); dialog.append(header, tools, filters, count, body); overlay.append(dialog);
    let selected = null, serial = 0;
    const messageKey = message => `${message.channel_id || 0}:${message.id || message.client_msg_id || message.seq || 0}`;
    const snippet = (message, className) => {
        const row = element("div", className);
        const meta = element("span", "search-meta", `${message.from_nickname || "?"} · ${new Date(searchTimestamp(message)).toLocaleString()}`);
        const copy = element("div", "search-snippet"); copy.textContent = searchSnippet(message); highlight(copy, query.toLowerCase());
        const badge = renderBadge?.(message); if (badge) meta.append(badge);
        row.append(meta, copy); return row;
    };
    const choose = async message => {
        if (!valid()) return;
        selected = messageKey(message); const request = ++serial;
        for (const row of list.children) row.setAttribute("aria-current", String(row.dataset.key === selected));
        preview.replaceChildren(element("h3", "", t("midnight.searchPreview")));
        const full = snippet(message, "search-preview-message");
        if (renderBody) { const copy = full.querySelector(".search-snippet"); renderBody(copy, message); highlight(copy, query.toLowerCase()); }
        const open = element("button", "ui-button search-open", t("midnight.searchOpen")); open.type = "button";
        open.onclick = () => { if (valid()) onOpen(message, overlay); };
        const context = element("div", "search-context"); context.setAttribute("aria-busy", "true");
        preview.append(full, open, element("h4", "", t("midnight.searchContext")), context);
        try {
            const messages = await loadContext(message);
            if (!overlay.isConnected || request !== serial || !valid()) return;
            for (const entry of messages) {
                const row = snippet(entry, "search-context-message"); row.classList.toggle("selected", messageKey(entry) === selected); context.append(row);
            }
        } catch {
            if (overlay.isConnected && request === serial && valid()) context.textContent = t("midnight.searchContextFailed");
        } finally { if (request === serial) context.setAttribute("aria-busy", "false"); }
    };
    const render = () => {
        const visible = filterSearchResults(results, { author: author.value, days: Number(date.value), attachments: attachments.checked, sort: sorting.value, query });
        count.textContent = t("midnight.searchFiltered", { count: visible.length, total: results.length }); list.replaceChildren();
        for (const message of visible) {
            const row = element("button", "search-hit"); row.type = "button"; row.dataset.key = messageKey(message);
            row.setAttribute("aria-current", String(row.dataset.key === selected)); row.append(...snippet(message, "").childNodes);
            const channel = channels.find(entry => String(entry.id) === String(message.channel_id || 0));
            if (channel) row.prepend(element("span", "search-channel-badge", channel.name));
            row.onclick = () => void choose(message); list.append(row);
        }
        if (!visible.length) { serial++; selected = null; preview.replaceChildren(element("p", "set-hint", t("chat.noMatches"))); list.append(element("p", "set-hint", t("chat.noMatches"))); }
        else if (!visible.some(message => messageKey(message) === selected)) void choose(visible[0]);
    };
    author.onchange = date.onchange = sorting.onchange = attachments.onchange = render;
    // Run after mounting so context responses cannot update a detached panel.
    queueMicrotask(render);
    return overlay;
}
