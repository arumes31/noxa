import { t } from "./i18n.js";
import { parseFileRef } from "./chat-parsers.js";
import { closeDialog } from "./modal.js";
import { setSafeImage } from "./safe-media.js";
import { icon } from "./icons.js";
import { ALL_SCOPE_LIMIT, formatFileSize, groupUnifiedResults, normalizeScope, searchChannels, searchFiles, searchMembers, UNIFIED_SCOPES } from "./unified-search.js";

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

// `unified` (optional) turns the workspace into the one search over messages,
// files, members and channels: { members, channels: state channels, me,
// blocked, avatars, avatarColor, loadFiles: async () => entries | null, onOpenMember(hit),
// onOpenChannel(hit), onOpenFile(hit), scope }. Members and channels are
// matched here from loaded state; files come from loadFiles (the current
// channel's storage), messages from the server search the caller ran.
export function createSearchWorkspace({ query, results, summary, channels, selectedChannel, current, onSearch, onAdvanced, onOpen, loadContext, renderBody, highlight, renderBadge, unified = null }) {
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
    // Unified search: scope switch (All / Messages / Files / Members / Channels)
    // with live counts; files load once, asynchronously.
    let scope = normalizeScope(unified?.scope), fileState = { status: unified?.loadFiles ? "loading" : "unavailable", hits: [] };
    const scopeBar = element("div", "search-scope-bar"); scopeBar.setAttribute("role", "group"); scopeBar.setAttribute("aria-label", t("search.scope"));
    const scopeKeys = { all: "search.scope.all", messages: "search.scope.messages", files: "search.scope.files", members: "search.scope.members", channels: "search.scope.channels" };
    const scopeName = kind => t(scopeKeys[kind]);
    const scopeButtons = new Map();
    if (unified) {
        scopeBar.append(element("span", "search-scope-label", t("search.scope")));
        for (const kind of UNIFIED_SCOPES) {
            const button = element("button", "search-scope"); button.type = "button"; button.dataset.scope = kind;
            button.append(document.createTextNode(scopeName(kind)), element("span", "search-scope-count", ""));
            button.onclick = () => { if (!valid()) return; scope = kind; render(); };
            scopeBar.append(button); scopeButtons.set(kind, button);
        }
    }
    body.append(list, preview); dialog.append(header, tools, filters, ...(unified ? [scopeBar] : []), count, body); overlay.append(dialog);
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
    const messageRow = message => {
        const row = element("button", "search-hit"); row.type = "button"; row.dataset.key = messageKey(message);
        row.setAttribute("aria-current", String(row.dataset.key === selected)); row.append(...snippet(message, "").childNodes);
        const channel = channels.find(entry => String(entry.id) === String(message.channel_id || 0));
        if (channel) row.prepend(element("span", "search-channel-badge", channel.name));
        row.onclick = () => void choose(message); return row;
    };
    const entityRow = (hit, iconNode, meta, open) => {
        const row = element("button", "search-hit search-entity"); row.type = "button"; row.dataset.key = `${hit.kind}:${hit.uid ?? hit.id ?? hit.name}`;
        const text = element("span", "search-entity-text"); const name = element("span", "search-entity-name", hit.name); highlight(name, query.toLowerCase());
        text.append(name, element("span", "search-entity-meta", meta)); row.append(iconNode, text);
        row.setAttribute("aria-label", open.label); row.onclick = () => { if (valid()) { closeDialog(overlay); open.action(); } };
        return row;
    };
    const entityIcon = name => { const node = element("span", "search-entity-icon"); node.setAttribute("aria-hidden", "true"); node.innerHTML = icon(name); return node; };
    const memberIcon = hit => {
        const face = element("span", "search-entity-icon avatar"); face.setAttribute("aria-hidden", "true");
        if (unified.avatarColor) face.style.setProperty("--avatar-color", unified.avatarColor(hit.uid || hit.name));
        const image = unified?.avatars?.get?.(hit.uid);
        if (!image || !setSafeImage(face, image, { avatar: true })) face.textContent = String(hit.name || "?").slice(0, 1).toUpperCase();
        return face;
    };
    const renderGroup = group => {
        const title = element("div", "search-group-title"); title.append(element("span", "", `${scopeName(group.kind)} · ${group.total}`));
        if (group.truncated) {
            const more = element("button", "search-group-more", t("search.showAll", { count: group.total, scope: scopeName(group.kind) })); more.type = "button";
            more.onclick = () => { if (!valid()) return; scope = group.kind; render(); }; title.append(more);
        }
        list.append(title);
        for (const hit of group.items) {
            if (group.kind === "members") list.append(entityRow(hit, memberIcon(hit), hit.channel ? t("search.memberInChannel", { channel: hit.channel }) : t("search.memberOnline"), { label: t("search.openMember", { name: hit.name }), action: () => unified.onOpenMember?.(hit) }));
            else if (group.kind === "channels") list.append(entityRow(hit, entityIcon(hit.textOnly ? "hash" : "speaker"), hit.topic || t(hit.textOnly ? "search.textChannel" : "search.voiceChannel"), { label: t("search.openChannel", { name: hit.name }), action: () => unified.onOpenChannel?.(hit) }));
            else if (group.kind === "files") list.append(entityRow(hit, entityIcon("file"), t("search.fileMeta", { size: formatFileSize(hit.size), folder: hit.folder || "/", uploader: hit.uploader || "?" }), { label: t("search.openFile"), action: () => unified.onOpenFile?.(hit) }));
            else list.append(messageRow(hit));
        }
        if (group.kind !== "files" || fileState.status === "loaded") return;
        list.append(element("p", "search-group-note", t(fileState.status === "loading" ? "search.filesLoading" : fileState.status === "failed" ? "search.filesFailed" : "search.filesUnavailable")));
    };
    const render = () => {
        const visible = filterSearchResults(results, { author: author.value, days: Number(date.value), attachments: attachments.checked, sort: sorting.value, query });
        list.replaceChildren();
        if (unified) {
            const grouped = groupUnifiedResults({ messages: visible, files: fileState.hits,
                members: searchMembers(unified.members || [], query, { me: unified.me, blocked: unified.blocked, channels: unified.channels || [] }),
                channels: searchChannels(unified.channels || [], query) }, { scope, limit: ALL_SCOPE_LIMIT });
            for (const [kind, button] of scopeButtons) { button.setAttribute("aria-pressed", String(kind === scope)); button.querySelector(".search-scope-count").textContent = String(grouped.counts[kind] ?? 0); }
            count.textContent = t("search.scopeStatus", { scope: scopeName(scope), count: grouped.total });
            const messageGroup = grouped.groups.find(group => group.kind === "messages");
            if (!grouped.total) list.append(element("p", "set-hint", t("search.nothingInScope", { query })));
            for (const group of grouped.groups) renderGroup(group);
            if (!grouped.total && scope === "all" && fileState.status !== "loaded") renderGroup({ kind: "files", items: [], total: 0, truncated: false });
            if (!messageGroup?.items.length) { serial++; selected = null; preview.replaceChildren(element("p", "set-hint", t("search.scopeHint"))); }
            else if (!messageGroup.items.some(message => messageKey(message) === selected)) void choose(messageGroup.items[0]);
            return;
        }
        count.textContent = t("midnight.searchFiltered", { count: visible.length, total: results.length });
        for (const message of visible) list.append(messageRow(message));
        if (!visible.length) { serial++; selected = null; preview.replaceChildren(element("p", "set-hint", t("chat.noMatches"))); list.append(element("p", "set-hint", t("chat.noMatches"))); }
        else if (!visible.some(message => messageKey(message) === selected)) void choose(visible[0]);
    };
    author.onchange = date.onchange = sorting.onchange = attachments.onchange = render;
    if (unified?.loadFiles) {
        Promise.resolve().then(() => unified.loadFiles()).then(entries => {
            if (!overlay.isConnected) return;
            fileState = entries === null ? { status: "unavailable", hits: [] } : { status: "loaded", hits: searchFiles(entries || [], query) };
            render();
        }).catch(() => { if (overlay.isConnected) { fileState = { status: "failed", hits: [] }; render(); } });
    }
    // Run after mounting so context responses cannot update a detached panel.
    queueMicrotask(render);
    return overlay;
}
